// Package sqlitevec provides high-performance vector storage and similarity search
// backed by SQLite. It stores document embeddings and supports cosine similarity
// based retrieval with an in-memory cache for fast search and concurrent
// similarity computation.
//
// Performance optimizations:
// - Contiguous float32 vector arena for CPU cache-friendly sequential access
// - Partition index for O(partition_size) instead of O(total) search
// - Pre-computed text bigrams for instant TextSearch (no per-query recomputation)
// - 8-way loop unrolling for dot product (maximizes ILP on modern CPUs)
// - SIMD acceleration: AVX-512 / AVX2+FMA / NEON / SSE with automatic detection
// - Adaptive worker count to avoid goroutine overhead on small datasets
// - Query result LRU cache to skip repeated searches
// - Per-worker top-K heap to reduce final merge cost
package sqlitevec

import (
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"runtime"
	"sort"
	"strings"
	"sync"
	"time"

	_ "github.com/mattn/go-sqlite3"
)

// VectorStore defines the interface for storing and searching document embeddings.
type VectorStore interface {
	Store(docID string, chunks []VectorChunk) error
	Search(queryVector []float64, topK int, threshold float64, partitionID string) ([]SearchResult, error)
	TextSearch(query string, topK int, threshold float64, partitionID string) ([]SearchResult, error)
	DeleteByDocID(docID string) error
}

// VectorChunk represents a document chunk with its embedding vector.
type VectorChunk struct {
	ChunkText    string    `json:"chunk_text"`
	ChunkIndex   int       `json:"chunk_index"`
	DocumentID   string    `json:"document_id"`
	DocumentName string    `json:"document_name"`
	Vector       []float64 `json:"vector"`
	ImageURL     string    `json:"image_url,omitempty"`
	PartitionID  string    `json:"partition_id"`
}

// SearchResult represents a search result with similarity score.
type SearchResult struct {
	ChunkText    string  `json:"chunk_text"`
	ChunkIndex   int     `json:"chunk_index"`
	DocumentID   string  `json:"document_id"`
	DocumentName string  `json:"document_name"`
	Score        float64 `json:"score"`
	ImageURL     string  `json:"image_url,omitempty"`
	PartitionID  string  `json:"partition_id"`
	StartTime    float64 `json:"start_time,omitempty"`
	EndTime      float64 `json:"end_time,omitempty"`
}

// chunkMeta holds a chunk's metadata (no vector — vectors live in the arena).
type chunkMeta struct {
	chunkText    string
	chunkIndex   int
	documentID   string
	documentName string
	imageURL     string
	partitionID  string
	textLower    string
	bigrams      map[string]bool
}

// vectorArena stores all vectors contiguously in a single []float32 for
// CPU cache-friendly sequential access.
type vectorArena struct {
	data []float32
	dim  int
}

func (a *vectorArena) getVector(idx int) []float32 {
	if a.dim == 0 {
		return nil
	}
	start := idx * a.dim
	end := start + a.dim
	if end > len(a.data) {
		return nil
	}
	return a.data[start:end]
}

func (a *vectorArena) append(vec []float32) int {
	idx := len(a.data) / a.dim
	a.data = append(a.data, vec...)
	return idx
}

// queryCache provides an LRU cache for recent vector search results.
type queryCache struct {
	mu      sync.Mutex
	entries map[uint64]queryCacheEntry
	order   []uint64
	maxSize int
	ttl     time.Duration
}

type queryCacheEntry struct {
	results   []SearchResult
	timestamp time.Time
}

func newQueryCache(maxSize int, ttl time.Duration) *queryCache {
	return &queryCache{
		entries: make(map[uint64]queryCacheEntry, maxSize),
		order:   make([]uint64, 0, maxSize),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

func (qc *queryCache) get(key uint64) ([]SearchResult, bool) {
	qc.mu.Lock()
	defer qc.mu.Unlock()
	entry, ok := qc.entries[key]
	if !ok {
		return nil, false
	}
	if time.Since(entry.timestamp) > qc.ttl {
		delete(qc.entries, key)
		return nil, false
	}
	return entry.results, true
}

func (qc *queryCache) put(key uint64, results []SearchResult) {
	qc.mu.Lock()
	defer qc.mu.Unlock()
	if _, ok := qc.entries[key]; !ok {
		if len(qc.order) >= qc.maxSize {
			oldest := qc.order[0]
			qc.order = qc.order[1:]
			delete(qc.entries, oldest)
		}
		qc.order = append(qc.order, key)
	}
	qc.entries[key] = queryCacheEntry{results: results, timestamp: time.Now()}
}

func (qc *queryCache) invalidate() {
	qc.mu.Lock()
	defer qc.mu.Unlock()
	qc.entries = make(map[uint64]queryCacheEntry, qc.maxSize)
	qc.order = qc.order[:0]
}

// scoredItem is used by the per-worker min-heap to track top-K results efficiently.
type scoredItem struct {
	score float32
	idx   int
}

type topKHeap []scoredItem

func (h topKHeap) Len() int            { return len(h) }
func (h topKHeap) Less(i, j int) bool   { return h[i].score < h[j].score }
func (h topKHeap) Swap(i, j int)        { h[i], h[j] = h[j], h[i] }
func (h *topKHeap) Push(x interface{})  { *h = append(*h, x.(scoredItem)) }
func (h *topKHeap) Pop() interface{} {
	old := *h
	n := len(old)
	item := old[n-1]
	*h = old[:n-1]
	return item
}

// SQLiteVectorStore implements VectorStore using SQLite for persistence
// with an in-memory vector cache for fast similarity search.
type SQLiteVectorStore struct {
	db             *sql.DB
	mu             sync.RWMutex
	meta           []chunkMeta
	norms          []float32
	arena          vectorArena
	partitionIndex map[string][]int
	loaded         bool
	searchCache    *queryCache
}

// SIMDCapability returns a human-readable string describing the active SIMD
// acceleration path for vector operations. Used for startup diagnostics.
func SIMDCapability() string {
	return simdCapability()
}

// NewSQLiteVectorStore creates a new SQLiteVectorStore with the given database connection.
// The database must already have a "chunks" table with the expected schema.
// Use EnsureTable to create the table if needed.
func NewSQLiteVectorStore(db *sql.DB) *SQLiteVectorStore {
	return &SQLiteVectorStore{
		db:             db,
		partitionIndex: make(map[string][]int),
		searchCache:    newQueryCache(256, 5*time.Minute),
	}
}

// EnsureTable creates the chunks table and indexes if they don't exist.
func EnsureTable(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS chunks (
		id            TEXT PRIMARY KEY,
		document_id   TEXT NOT NULL,
		document_name TEXT NOT NULL,
		chunk_index   INTEGER NOT NULL,
		chunk_text    TEXT NOT NULL,
		embedding     BLOB NOT NULL,
		image_url     TEXT DEFAULT '',
		product_id    TEXT DEFAULT '',
		created_at    DATETIME DEFAULT CURRENT_TIMESTAMP
	)`)
	if err != nil {
		return fmt.Errorf("failed to create chunks table: %w", err)
	}
	indexes := []string{
		`CREATE INDEX IF NOT EXISTS idx_chunks_document_id ON chunks(document_id)`,
		`CREATE INDEX IF NOT EXISTS idx_chunks_product_id ON chunks(product_id)`,
	}
	for _, idx := range indexes {
		if _, err := db.Exec(idx); err != nil {
			return fmt.Errorf("failed to create index: %w", err)
		}
	}
	return nil
}

// loadCache reads all chunks from the database into memory.
func (s *SQLiteVectorStore) loadCache() error {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM chunks`).Scan(&count)
	if err != nil {
		return fmt.Errorf("failed to count chunks: %w", err)
	}
	if count == 0 {
		s.meta = nil
		s.norms = nil
		s.arena = vectorArena{}
		s.partitionIndex = make(map[string][]int)
		s.loaded = true
		return nil
	}

	rows, err := s.db.Query(`SELECT document_id, document_name, chunk_index, chunk_text, embedding, COALESCE(image_url,''), COALESCE(product_id,'') FROM chunks`)
	if err != nil {
		return fmt.Errorf("failed to query chunks: %w", err)
	}
	defer rows.Close()

	meta := make([]chunkMeta, 0, count)
	norms := make([]float32, 0, count)
	partitionIndex := make(map[string][]int)
	dimDetected := false
	var arenaData []float32

	for rows.Next() {
		var docID, docName, chunkText, imageURL, partitionID string
		var chunkIndex int
		var embeddingBytes []byte

		if err := rows.Scan(&docID, &docName, &chunkIndex, &chunkText, &embeddingBytes, &imageURL, &partitionID); err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}

		vec32 := DeserializeVectorF32(embeddingBytes)

		if !dimDetected && len(vec32) > 0 {
			s.arena.dim = len(vec32)
			arenaData = make([]float32, 0, count*len(vec32))
			dimDetected = true
		}

		textLower := strings.ToLower(chunkText)
		idx := len(meta)

		norm := vectorNormSIMD(vec32)
		var invNorm float32
		if norm > 0 {
			invNorm = 1.0 / norm
		}

		meta = append(meta, chunkMeta{
			chunkText:    chunkText,
			chunkIndex:   chunkIndex,
			documentID:   docID,
			documentName: docName,
			imageURL:     imageURL,
			partitionID:  partitionID,
			textLower:    textLower,
			bigrams:      charBigrams(textLower),
		})
		norms = append(norms, invNorm)
		arenaData = append(arenaData, vec32...)
		partitionIndex[partitionID] = append(partitionIndex[partitionID], idx)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating rows: %w", err)
	}

	s.meta = meta
	s.norms = norms
	s.arena.data = arenaData
	s.partitionIndex = partitionIndex
	s.loaded = true
	return nil
}

func (s *SQLiteVectorStore) ensureCache() error {
	if s.loaded {
		return nil
	}
	return s.loadCache()
}

// vectorNormF32 computes the L2 norm of a float32 vector.
func vectorNormF32(v []float32) float32 {
	var sum float32
	n := len(v)
	i := 0
	for ; i <= n-8; i += 8 {
		sum += v[i]*v[i] + v[i+1]*v[i+1] + v[i+2]*v[i+2] + v[i+3]*v[i+3] +
			v[i+4]*v[i+4] + v[i+5]*v[i+5] + v[i+6]*v[i+6] + v[i+7]*v[i+7]
	}
	for ; i < n; i++ {
		sum += v[i] * v[i]
	}
	return float32(math.Sqrt(float64(sum)))
}

// vectorNorm computes the L2 norm of a float64 vector.
func vectorNorm(v []float64) float64 {
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	return math.Sqrt(sum)
}

func toFloat32(v []float64) []float32 {
	out := make([]float32, len(v))
	for i, x := range v {
		out[i] = float32(x)
	}
	return out
}

// Store inserts a batch of VectorChunks into the chunks table and updates the cache.
func (s *SQLiteVectorStore) Store(docID string, chunks []VectorChunk) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	tx, err := s.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}

	stmt, err := tx.Prepare(`INSERT INTO chunks (id, document_id, document_name, chunk_index, chunk_text, embedding, image_url, product_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`)
	if err != nil {
		tx.Rollback()
		return fmt.Errorf("failed to prepare statement: %w", err)
	}
	defer stmt.Close()

	type newEntry struct {
		meta        chunkMeta
		invNorm     float32
		vec32       []float32
		partitionID string
	}
	var newEntries []newEntry

	for _, chunk := range chunks {
		chunkID := fmt.Sprintf("%s-%d", docID, chunk.ChunkIndex)
		embeddingBytes := SerializeVector(chunk.Vector)

		_, err := stmt.Exec(chunkID, docID, chunk.DocumentName, chunk.ChunkIndex, chunk.ChunkText, embeddingBytes, chunk.ImageURL, chunk.PartitionID)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to insert chunk %s: %w", chunkID, err)
		}

		vec32 := toFloat32(chunk.Vector)
		textLower := strings.ToLower(chunk.ChunkText)
		norm := vectorNormSIMD(vec32)
		var invNorm float32
		if norm > 0 {
			invNorm = 1.0 / norm
		}
		newEntries = append(newEntries, newEntry{
			meta: chunkMeta{
				chunkText:    chunk.ChunkText,
				chunkIndex:   chunk.ChunkIndex,
				documentID:   chunk.DocumentID,
				documentName: chunk.DocumentName,
				imageURL:     chunk.ImageURL,
				partitionID:  chunk.PartitionID,
				textLower:    textLower,
				bigrams:      charBigrams(textLower),
			},
			invNorm:     invNorm,
			vec32:       vec32,
			partitionID: chunk.PartitionID,
		})
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	if s.loaded {
		for _, ne := range newEntries {
			idx := len(s.meta)
			s.meta = append(s.meta, ne.meta)
			s.norms = append(s.norms, ne.invNorm)
			if s.arena.dim == 0 && len(ne.vec32) > 0 {
				s.arena.dim = len(ne.vec32)
			}
			s.arena.data = append(s.arena.data, ne.vec32...)
			s.partitionIndex[ne.partitionID] = append(s.partitionIndex[ne.partitionID], idx)
		}
	} else {
		if err := s.loadCache(); err != nil {
			return err
		}
	}

	s.searchCache.invalidate()
	return nil
}

func hashQueryVector(qv []float32, topK int, threshold float64, partitionID string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	n := len(qv)
	if n > 8 {
		n = 8
	}
	for i := 0; i < n; i++ {
		bits := math.Float32bits(qv[i])
		h ^= uint64(bits)
		h *= prime64
		h ^= uint64(bits >> 16)
		h *= prime64
	}
	h ^= uint64(topK)
	h *= prime64
	h ^= math.Float64bits(threshold)
	h *= prime64
	for i := 0; i < len(partitionID); i++ {
		h ^= uint64(partitionID[i])
		h *= prime64
	}
	return h
}

// dotProductF32x8 computes dot product with 8-way loop unrolling.
func dotProductF32x8(a, b []float32) float32 {
	n := len(a)
	var s0, s1, s2, s3, s4, s5, s6, s7 float32
	i := 0
	for ; i <= n-8; i += 8 {
		s0 += a[i] * b[i]
		s1 += a[i+1] * b[i+1]
		s2 += a[i+2] * b[i+2]
		s3 += a[i+3] * b[i+3]
		s4 += a[i+4] * b[i+4]
		s5 += a[i+5] * b[i+5]
		s6 += a[i+6] * b[i+6]
		s7 += a[i+7] * b[i+7]
	}
	for ; i < n; i++ {
		s0 += a[i] * b[i]
	}
	return (s0 + s1 + s2 + s3) + (s4 + s5 + s6 + s7)
}

const minWorkersThreshold = 500

func adaptiveWorkers(n int) int {
	if n < minWorkersThreshold {
		return 1
	}
	w := n / minWorkersThreshold
	cpus := runtime.NumCPU()
	if w > cpus {
		w = cpus
	}
	if w < 1 {
		w = 1
	}
	return w
}

// Search uses the in-memory arena with concurrent cosine similarity computation.
func (s *SQLiteVectorStore) Search(queryVector []float64, topK int, threshold float64, partitionID string) ([]SearchResult, error) {
	queryF32 := toFloat32(queryVector)

	cacheKey := hashQueryVector(queryF32, topK, threshold, partitionID)
	if cached, ok := s.searchCache.get(cacheKey); ok {
		return cached, nil
	}

	s.mu.Lock()
	if err := s.ensureCache(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	meta := s.meta
	normsArr := s.norms
	arena := s.arena
	indices := s.getRelevantIndices(partitionID)
	s.mu.Unlock()

	if len(meta) == 0 || len(indices) == 0 || arena.dim == 0 {
		return nil, nil
	}

	queryNorm := vectorNormSIMD(queryF32)
	if queryNorm == 0 {
		return nil, nil
	}

	invQueryNorm := float32(1.0) / queryNorm
	thresholdF32 := float32(threshold)
	dim := arena.dim

	numWorkers := adaptiveWorkers(len(indices))
	chunkSize := (len(indices) + numWorkers - 1) / numWorkers
	type partialResult struct {
		items []scoredItem
	}
	resultsCh := make(chan partialResult, numWorkers)

	for w := 0; w < numWorkers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if end > len(indices) {
			end = len(indices)
		}
		go func(idxSlice []int) {
			h := make([]scoredItem, 0, topK+1)
			hLen := 0

			arenaData := arena.data
			norms := normsArr
			for _, idx := range idxSlice {
				invNorm := norms[idx]
				if invNorm == 0 {
					continue
				}
				vecStart := idx * dim
				vecEnd := vecStart + dim
				if vecEnd > len(arenaData) {
					continue
				}
				vec := arenaData[vecStart:vecEnd]

				dot := dotProductSIMD(queryF32, vec)
				score := dot * invQueryNorm * invNorm

				if score >= thresholdF32 {
					if hLen < topK {
						h = append(h, scoredItem{score: score, idx: idx})
						hLen++
						i := hLen - 1
						for i > 0 {
							parent := (i - 1) / 2
							if h[parent].score <= h[i].score {
								break
							}
							h[parent], h[i] = h[i], h[parent]
							i = parent
						}
					} else if score > h[0].score {
						h[0] = scoredItem{score: score, idx: idx}
						i := 0
						for {
							left := 2*i + 1
							if left >= hLen {
								break
							}
							smallest := left
							right := left + 1
							if right < hLen && h[right].score < h[left].score {
								smallest = right
							}
							if h[i].score <= h[smallest].score {
								break
							}
							h[i], h[smallest] = h[smallest], h[i]
							i = smallest
						}
					}
				}
			}
			resultsCh <- partialResult{items: h[:hLen]}
		}(indices[start:end])
	}

	merged := make([]scoredItem, 0, topK+1)
	mergedLen := 0
	for w := 0; w < numWorkers; w++ {
		pr := <-resultsCh
		for _, item := range pr.items {
			if mergedLen < topK {
				merged = append(merged, item)
				mergedLen++
				i := mergedLen - 1
				for i > 0 {
					parent := (i - 1) / 2
					if merged[parent].score <= merged[i].score {
						break
					}
					merged[parent], merged[i] = merged[i], merged[parent]
					i = parent
				}
			} else if item.score > merged[0].score {
				merged[0] = item
				i := 0
				for {
					left := 2*i + 1
					if left >= mergedLen {
						break
					}
					smallest := left
					right := left + 1
					if right < mergedLen && merged[right].score < merged[left].score {
						smallest = right
					}
					if merged[i].score <= merged[smallest].score {
						break
					}
					merged[i], merged[smallest] = merged[smallest], merged[i]
					i = smallest
				}
			}
		}
	}

	allResults := make([]SearchResult, mergedLen)
	for i := mergedLen - 1; i >= 0; i-- {
		item := merged[0]
		mergedLen--
		if mergedLen > 0 {
			merged[0] = merged[mergedLen]
			j := 0
			for {
				left := 2*j + 1
				if left >= mergedLen {
					break
				}
				smallest := left
				right := left + 1
				if right < mergedLen && merged[right].score < merged[left].score {
					smallest = right
				}
				if merged[j].score <= merged[smallest].score {
					break
				}
				merged[j], merged[smallest] = merged[smallest], merged[j]
				j = smallest
			}
		}
		m := &meta[item.idx]
		allResults[i] = SearchResult{
			ChunkText:    m.chunkText,
			ChunkIndex:   m.chunkIndex,
			DocumentID:   m.documentID,
			DocumentName: m.documentName,
			Score:        float64(item.score),
			ImageURL:     m.imageURL,
			PartitionID:  m.partitionID,
		}
	}

	s.searchCache.put(cacheKey, allResults)
	return allResults, nil
}

func (s *SQLiteVectorStore) getRelevantIndices(partitionID string) []int {
	if partitionID == "" {
		indices := make([]int, len(s.meta))
		for i := range indices {
			indices[i] = i
		}
		return indices
	}
	partChunks := s.partitionIndex[partitionID]
	publicChunks := s.partitionIndex[""]
	total := len(partChunks) + len(publicChunks)
	if total == 0 {
		return nil
	}
	indices := make([]int, 0, total)
	indices = append(indices, partChunks...)
	indices = append(indices, publicChunks...)
	return indices
}

// TextSearch performs a text-based similarity search using keyword overlap
// and pre-computed character bigram Jaccard similarity.
func (s *SQLiteVectorStore) TextSearch(query string, topK int, threshold float64, partitionID string) ([]SearchResult, error) {
	s.mu.Lock()
	if err := s.ensureCache(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	meta := s.meta
	indices := s.getRelevantIndices(partitionID)
	s.mu.Unlock()

	if len(meta) == 0 || len(indices) == 0 {
		return nil, nil
	}

	queryLower := strings.ToLower(query)
	queryBigrams := charBigrams(queryLower)
	queryKeywords := extractKeywords(queryLower)

	type scored struct {
		idx   int
		score float64
	}

	numWorkers := adaptiveWorkers(len(indices))
	chunkSize := (len(indices) + numWorkers - 1) / numWorkers
	type partialHits struct {
		hits []scored
	}
	hitsCh := make(chan partialHits, numWorkers)

	for w := 0; w < numWorkers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if end > len(indices) {
			end = len(indices)
		}
		go func(idxSlice []int) {
			var local []scored
			for _, idx := range idxSlice {
				m := &meta[idx]
				kwScore := keywordOverlap(queryKeywords, m.textLower)
				bigramScore := jaccardBigrams(queryBigrams, m.bigrams)
				score := kwScore*0.6 + bigramScore*0.4
				if score >= threshold {
					local = append(local, scored{idx: idx, score: score})
				}
			}
			hitsCh <- partialHits{hits: local}
		}(indices[start:end])
	}

	var hits []scored
	for w := 0; w < numWorkers; w++ {
		ph := <-hitsCh
		hits = append(hits, ph.hits...)
	}

	sort.Slice(hits, func(i, j int) bool {
		return hits[i].score > hits[j].score
	})
	if len(hits) > topK {
		hits = hits[:topK]
	}

	results := make([]SearchResult, len(hits))
	for i, h := range hits {
		m := &meta[h.idx]
		results[i] = SearchResult{
			ChunkText:    m.chunkText,
			ChunkIndex:   m.chunkIndex,
			DocumentID:   m.documentID,
			DocumentName: m.documentName,
			Score:        h.score,
			ImageURL:     m.imageURL,
			PartitionID:  m.partitionID,
		}
	}
	return results, nil
}

func charBigrams(s string) map[string]bool {
	runes := []rune(s)
	result := make(map[string]bool, len(runes))
	for i := 0; i < len(runes)-1; i++ {
		result[string(runes[i:i+2])] = true
	}
	return result
}

func jaccardBigrams(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	if len(a) > len(b) {
		a, b = b, a
	}
	intersection := 0
	for bg := range a {
		if b[bg] {
			intersection++
		}
	}
	union := len(a) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func extractKeywords(s string) []string {
	fields := strings.FieldsFunc(s, func(r rune) bool {
		return r == ' ' || r == '\t' || r == '\n' || r == ',' || r == '.' ||
			r == '?' || r == '!' || r == '\u3002' || r == '\uff0c' || r == '\uff1f' ||
			r == '\uff01' || r == '\u3001' || r == '\uff1a' || r == '\uff1b' ||
			r == '\u201c' || r == '\u201d' || r == '\uff08' || r == '\uff09' ||
			r == '(' || r == ')' || r == '[' || r == ']' || r == '{' || r == '}'
	})
	seen := make(map[string]bool, len(fields))
	var kw []string
	for _, f := range fields {
		if len([]rune(f)) < 2 {
			continue
		}
		lower := strings.ToLower(f)
		if !seen[lower] {
			seen[lower] = true
			kw = append(kw, lower)
		}
	}
	return kw
}

func keywordOverlap(queryKeywords []string, chunkLower string) float64 {
	if len(queryKeywords) == 0 {
		return 0
	}
	matched := 0
	for _, kw := range queryKeywords {
		if strings.Contains(chunkLower, kw) {
			matched++
		}
	}
	return float64(matched) / float64(len(queryKeywords))
}

// DeleteByDocID removes all chunks for the given document from DB and cache.
func (s *SQLiteVectorStore) DeleteByDocID(docID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	_, err := s.db.Exec(`DELETE FROM chunks WHERE document_id = ?`, docID)
	if err != nil {
		return fmt.Errorf("failed to delete chunks for document %s: %w", docID, err)
	}

	if s.loaded {
		dim := s.arena.dim
		newMeta := make([]chunkMeta, 0, len(s.meta))
		newNorms := make([]float32, 0, len(s.norms))
		var newArenaData []float32
		if dim > 0 {
			newArenaData = make([]float32, 0, len(s.arena.data))
		}
		newPartitionIndex := make(map[string][]int)

		for i, m := range s.meta {
			if m.documentID != docID {
				idx := len(newMeta)
				newMeta = append(newMeta, m)
				if i < len(s.norms) {
					newNorms = append(newNorms, s.norms[i])
				}
				if dim > 0 {
					vecStart := i * dim
					vecEnd := vecStart + dim
					if vecEnd <= len(s.arena.data) {
						newArenaData = append(newArenaData, s.arena.data[vecStart:vecEnd]...)
					}
				}
				newPartitionIndex[m.partitionID] = append(newPartitionIndex[m.partitionID], idx)
			}
		}
		s.meta = newMeta
		s.norms = newNorms
		s.arena.data = newArenaData
		s.partitionIndex = newPartitionIndex
	}

	s.searchCache.invalidate()
	return nil
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// DeserializeVectorF32Unsafe performs zero-copy deserialization for float32 format data.
func DeserializeVectorF32Unsafe(data []byte) []float32 {
	if len(data) == 0 || len(data)%4 != 0 {
		return nil
	}
	if len(data)%8 != 0 {
		n := len(data) / 4
		vec := make([]float32, n)
		for i := 0; i < n; i++ {
			vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
		}
		return vec
	}
	return DeserializeVectorF32(data)
}
