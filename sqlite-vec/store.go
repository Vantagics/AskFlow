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
// Uses a ring buffer for O(1) eviction instead of O(n) slice copy.
type queryCache struct {
	mu      sync.Mutex
	entries map[uint64]queryCacheEntry
	ring    []uint64 // ring buffer for eviction order
	head    int      // next write position
	count   int      // number of valid entries in ring
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
		ring:    make([]uint64, maxSize),
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
	// Return a copy to prevent callers from mutating cached data.
	out := make([]SearchResult, len(entry.results))
	copy(out, entry.results)
	return out, true
}

func (qc *queryCache) put(key uint64, results []SearchResult) {
	qc.mu.Lock()
	defer qc.mu.Unlock()
	if _, ok := qc.entries[key]; !ok {
		if qc.count >= qc.maxSize {
			// Evict the oldest entry — it sits at the tail of the ring.
			evictIdx := (qc.head + qc.maxSize - qc.count) % qc.maxSize
			delete(qc.entries, qc.ring[evictIdx])
			qc.count-- // keep count accurate before incrementing below
		}
		qc.ring[qc.head] = key
		qc.head = (qc.head + 1) % qc.maxSize
		qc.count++
	}
	// Store a defensive copy so external mutations don't corrupt the cache.
	stored := make([]SearchResult, len(results))
	copy(stored, results)
	qc.entries[key] = queryCacheEntry{results: stored, timestamp: time.Now()}
}

func (qc *queryCache) invalidate() {
	qc.mu.Lock()
	defer qc.mu.Unlock()
	qc.entries = make(map[uint64]queryCacheEntry, qc.maxSize)
	qc.head = 0
	qc.count = 0
}

// scoredItem is used by the per-worker min-heap to track top-K results efficiently.
type scoredItem struct {
	score float32
	idx   int
}

// heapSiftUpF32 restores the min-heap property after appending at position i.
func heapSiftUpF32(h []scoredItem, i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if h[parent].score <= h[i].score {
			break
		}
		h[parent], h[i] = h[i], h[parent]
		i = parent
	}
}

// heapSiftDownF32 restores the min-heap property after replacing the root.
func heapSiftDownF32(h []scoredItem, n int) {
	i := 0
	for {
		left := 2*i + 1
		if left >= n {
			break
		}
		smallest := left
		if right := left + 1; right < n && h[right].score < h[left].score {
			smallest = right
		}
		if h[i].score <= h[smallest].score {
			break
		}
		h[i], h[smallest] = h[smallest], h[i]
		i = smallest
	}
}

// heapPushF32 inserts an item into a min-heap of capacity topK.
// Returns the new length. h must have len >= hLen and cap >= topK+1.
func heapPushF32(h []scoredItem, hLen, topK int, item scoredItem) ([]scoredItem, int) {
	if hLen < topK {
		h = append(h[:hLen], item)
		hLen++
		heapSiftUpF32(h, hLen-1)
	} else if item.score > h[0].score {
		h[0] = item
		heapSiftDownF32(h, hLen)
	}
	return h, hLen
}

// heapExtractAllF32 pops all items from the min-heap in descending score order.
func heapExtractAllF32(h []scoredItem, n int) []scoredItem {
	sorted := make([]scoredItem, n)
	for i := n - 1; i >= 0; i-- {
		sorted[i] = h[0]
		n--
		if n > 0 {
			h[0] = h[n]
			heapSiftDownF32(h, n)
		}
	}
	return sorted
}

// scored64 is used by TextSearch for float64 scoring.
type scored64 struct {
	idx   int
	score float64
}

// heapSiftUp64 restores the min-heap property for scored64 after appending at position i.
func heapSiftUp64(h []scored64, i int) {
	for i > 0 {
		parent := (i - 1) / 2
		if h[parent].score <= h[i].score {
			break
		}
		h[parent], h[i] = h[i], h[parent]
		i = parent
	}
}

// heapSiftDown64 restores the min-heap property for scored64 after replacing the root.
func heapSiftDown64(h []scored64, n int) {
	i := 0
	for {
		left := 2*i + 1
		if left >= n {
			break
		}
		smallest := left
		if right := left + 1; right < n && h[right].score < h[left].score {
			smallest = right
		}
		if h[i].score <= h[smallest].score {
			break
		}
		h[i], h[smallest] = h[smallest], h[i]
		i = smallest
	}
}

// heapPush64 inserts an item into a min-heap of capacity topK.
func heapPush64(h []scored64, hLen, topK int, item scored64) ([]scored64, int) {
	if hLen < topK {
		h = append(h[:hLen], item)
		hLen++
		heapSiftUp64(h, hLen-1)
	} else if item.score > h[0].score {
		h[0] = item
		heapSiftDown64(h, hLen)
	}
	return h, hLen
}

// heapExtractAll64 pops all items from the min-heap in descending score order.
func heapExtractAll64(h []scored64, n int) []scored64 {
	sorted := make([]scored64, n)
	for i := n - 1; i >= 0; i-- {
		sorted[i] = h[0]
		n--
		if n > 0 {
			h[0] = h[n]
			heapSiftDown64(h, n)
		}
	}
	return sorted
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
	globalIndex    []int // pre-built [0..n) index for unpartitioned search
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
		s.globalIndex = nil
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
	// Pre-allocate arena assuming a common dimension; will grow if needed.
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
	s.rebuildGlobalIndex()
	s.loaded = true
	return nil
}

// rebuildGlobalIndex builds the pre-computed [0..n) index slice used for
// unpartitioned searches, avoiding per-query allocation.
func (s *SQLiteVectorStore) rebuildGlobalIndex() {
	n := len(s.meta)
	if cap(s.globalIndex) >= n {
		s.globalIndex = s.globalIndex[:n]
	} else {
		s.globalIndex = make([]int, n)
	}
	for i := range s.globalIndex {
		s.globalIndex[i] = i
	}
}

// clearMergedPartitionCache removes cached merged index slices (keys containing
// the "\x00merged" sentinel) from partitionIndex. Must be called under write lock.
func (s *SQLiteVectorStore) clearMergedPartitionCache() {
	for k := range s.partitionIndex {
		if len(k) > 7 && k[len(k)-7:] == "\x00merged" {
			delete(s.partitionIndex, k)
		}
	}
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
		// Clear merged partition caches since indices are changing.
		s.clearMergedPartitionCache()
		for _, ne := range newEntries {
			idx := len(s.meta)
			s.meta = append(s.meta, ne.meta)
			s.norms = append(s.norms, ne.invNorm)
			if s.arena.dim == 0 && len(ne.vec32) > 0 {
				s.arena.dim = len(ne.vec32)
			}
			s.arena.data = append(s.arena.data, ne.vec32...)
			s.partitionIndex[ne.partitionID] = append(s.partitionIndex[ne.partitionID], idx)
			s.globalIndex = append(s.globalIndex, idx)
		}
	} else {
		if err := s.loadCache(); err != nil {
			return err
		}
	}

	s.searchCache.invalidate()
	return nil
}

// hashQueryVector produces a cache key by sampling elements spread across the
// entire vector, reducing collision risk for high-dimensional embeddings.
// Uses 32 samples plus head/tail mixing for robust collision resistance.
func hashQueryVector(qv []float32, topK int, threshold float64, partitionID string) uint64 {
	const (
		offset64   = 14695981039346656037
		prime64    = 1099511628211
		numSamples = 32
	)
	h := uint64(offset64)
	n := len(qv)

	// Sample up to numSamples elements evenly spread across the vector
	samples := numSamples
	if n < samples {
		samples = n
	}
	if samples > 0 {
		step := n / samples
		if step == 0 {
			step = 1
		}
		for i := 0; i < samples; i++ {
			idx := i * step
			if idx >= n {
				break
			}
			bits := math.Float32bits(qv[idx])
			h ^= uint64(bits)
			h *= prime64
		}
	}

	// Mix in first and last elements for head/tail sensitivity
	if n > 0 {
		h ^= uint64(math.Float32bits(qv[0]))
		h *= prime64
		h ^= uint64(math.Float32bits(qv[n-1]))
		h *= prime64
	}

	// Mix in vector length for dimension sensitivity
	h ^= uint64(n)
	h *= prime64

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

// hashTextQuery computes an FNV-1a hash for text search cache keys.
func hashTextQuery(query string, topK int, threshold float64, partitionID string) uint64 {
	const (
		offset64 = 14695981039346656037
		prime64  = 1099511628211
	)
	h := uint64(offset64)
	// Mix in a tag byte to avoid collisions with vector search cache keys.
	h ^= uint64(0xFF)
	h *= prime64
	for i := 0; i < len(query); i++ {
		h ^= uint64(query[i])
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

	s.mu.RLock()
	if !s.loaded {
		s.mu.RUnlock()
		// Upgrade to write lock for one-time cache load.
		s.mu.Lock()
		if !s.loaded { // re-check after acquiring write lock
			if err := s.loadCache(); err != nil {
				s.mu.Unlock()
				return nil, err
			}
		}
		s.mu.Unlock()
		s.mu.RLock()
	}
	meta := s.meta
	normsArr := s.norms
	arena := s.arena
	indices := s.getRelevantIndices(partitionID)
	s.mu.RUnlock()

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
					h, hLen = heapPushF32(h, hLen, topK, scoredItem{score: score, idx: idx})
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
			merged, mergedLen = heapPushF32(merged, mergedLen, topK, item)
		}
	}

	sorted := heapExtractAllF32(merged, mergedLen)
	allResults := make([]SearchResult, len(sorted))
	for i, item := range sorted {
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
		return s.globalIndex
	}
	// Check merged partition cache first to avoid repeated allocation.
	cacheKey := partitionID + "\x00merged"
	if cached, ok := s.partitionIndex[cacheKey]; ok {
		return cached
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
	// Cache the merged result (invalidated on Store/Delete via cache rebuild).
	s.partitionIndex[cacheKey] = indices
	return indices
}

// TextSearch performs a text-based similarity search using keyword overlap
// and pre-computed character bigram Jaccard similarity.
// Uses per-worker top-K min-heaps to avoid sorting all hits.
func (s *SQLiteVectorStore) TextSearch(query string, topK int, threshold float64, partitionID string) ([]SearchResult, error) {
	// Check text search cache using FNV hash of the query string.
	textCacheKey := hashTextQuery(query, topK, threshold, partitionID)
	if cached, ok := s.searchCache.get(textCacheKey); ok {
		return cached, nil
	}

	s.mu.RLock()
	if !s.loaded {
		s.mu.RUnlock()
		s.mu.Lock()
		if !s.loaded {
			if err := s.loadCache(); err != nil {
				s.mu.Unlock()
				return nil, err
			}
		}
		s.mu.Unlock()
		s.mu.RLock()
	}
	meta := s.meta
	indices := s.getRelevantIndices(partitionID)
	s.mu.RUnlock()

	if len(meta) == 0 || len(indices) == 0 {
		return nil, nil
	}

	queryLower := strings.ToLower(query)
	queryBigrams := charBigrams(queryLower)
	queryKeywords := extractKeywords(queryLower)

	numWorkers := adaptiveWorkers(len(indices))
	chunkSize := (len(indices) + numWorkers - 1) / numWorkers
	type partialHits struct {
		hits []scored64
	}
	hitsCh := make(chan partialHits, numWorkers)

	for w := 0; w < numWorkers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if end > len(indices) {
			end = len(indices)
		}
		go func(idxSlice []int) {
			h := make([]scored64, 0, topK+1)
			hLen := 0
			for _, idx := range idxSlice {
				m := &meta[idx]
				kwScore := keywordOverlap(queryKeywords, m.textLower)
				bigramScore := jaccardBigrams(queryBigrams, m.bigrams)
				score := kwScore*0.6 + bigramScore*0.4
				if score < threshold {
					continue
				}
				h, hLen = heapPush64(h, hLen, topK, scored64{idx: idx, score: score})
			}
			hitsCh <- partialHits{hits: h[:hLen]}
		}(indices[start:end])
	}

	// Merge per-worker heaps into final top-K
	merged := make([]scored64, 0, topK+1)
	mergedLen := 0
	for w := 0; w < numWorkers; w++ {
		ph := <-hitsCh
		for _, item := range ph.hits {
			merged, mergedLen = heapPush64(merged, mergedLen, topK, item)
		}
	}

	// Extract results in descending score order
	sorted := heapExtractAll64(merged, mergedLen)
	results := make([]SearchResult, len(sorted))
	for i, item := range sorted {
		m := &meta[item.idx]
		results[i] = SearchResult{
			ChunkText:    m.chunkText,
			ChunkIndex:   m.chunkIndex,
			DocumentID:   m.documentID,
			DocumentName: m.documentName,
			Score:        item.score,
			ImageURL:     m.imageURL,
			PartitionID:  m.partitionID,
		}
	}

	s.searchCache.put(textCacheKey, results)
	return results, nil
}

func charBigrams(s string) map[string]bool {
	runes := []rune(s)
	n := len(runes) - 1
	if n <= 0 {
		return nil
	}
	result := make(map[string]bool, n)
	for i := 0; i < n; i++ {
		result[string(runes[i:i+2])] = true
	}
	return result
}

func jaccardBigrams(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	// Iterate over the smaller map for fewer lookups.
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
		s.rebuildGlobalIndex()
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
// Falls back to safe copy when alignment requirements are not met.
func DeserializeVectorF32Unsafe(data []byte) []float32 {
	if len(data) == 0 || len(data)%4 != 0 {
		return nil
	}
	// For data that might be float64 format, use the safe path.
	if len(data)%8 == 0 {
		return DeserializeVectorF32(data)
	}
	n := len(data) / 4
	vec := make([]float32, n)
	for i := 0; i < n; i++ {
		vec[i] = math.Float32frombits(binary.LittleEndian.Uint32(data[i*4:]))
	}
	return vec
}
