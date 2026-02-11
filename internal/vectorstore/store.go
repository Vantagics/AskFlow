// Package vectorstore provides vector storage and similarity search using SQLite.
// It stores document embeddings and supports cosine similarity based retrieval
// with an in-memory cache for fast search and concurrent similarity computation.
//
// Performance optimizations:
// - Float32 vectors in memory (halves RAM vs float64, matches serialization format)
// - Product-partitioned index for O(product_size) instead of O(total) search
// - Pre-computed text bigrams for instant TextSearch (no per-query recomputation)
// - SIMD-friendly 4-way loop unrolling for dot product
// - Adaptive worker count to avoid goroutine overhead on small datasets
// - Query result LRU cache to skip repeated searches
package vectorstore

import (
	"database/sql"
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
	Search(queryVector []float64, topK int, threshold float64, productID string) ([]SearchResult, error)
	TextSearch(query string, topK int, threshold float64, productID string) ([]SearchResult, error)
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
	ProductID    string    `json:"product_id"`
}

// SearchResult represents a search result with similarity score.
type SearchResult struct {
	ChunkText    string  `json:"chunk_text"`
	ChunkIndex   int     `json:"chunk_index"`
	DocumentID   string  `json:"document_id"`
	DocumentName string  `json:"document_name"`
	Score        float64 `json:"score"`
	ImageURL     string  `json:"image_url,omitempty"`
	ProductID    string  `json:"product_id"`
}

// cachedChunk holds a chunk's metadata and pre-computed data for fast similarity.
// Uses float32 vectors to halve memory usage (matches serialization precision).
type cachedChunk struct {
	chunkText    string
	chunkIndex   int
	documentID   string
	documentName string
	vector       []float32 // float32 to halve memory (embedding precision is float32)
	norm         float32   // pre-computed L2 norm
	imageURL     string
	productID    string
	// Pre-computed text search data (avoids per-query recomputation)
	textLower string
	bigrams   map[string]bool
}

// queryCache provides an LRU cache for recent vector search results.
type queryCache struct {
	mu      sync.Mutex
	entries map[string]queryCacheEntry
	order   []string // LRU order (newest at end)
	maxSize int
	ttl     time.Duration
}

type queryCacheEntry struct {
	results   []SearchResult
	timestamp time.Time
}

func newQueryCache(maxSize int, ttl time.Duration) *queryCache {
	return &queryCache{
		entries: make(map[string]queryCacheEntry, maxSize),
		order:   make([]string, 0, maxSize),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

func (qc *queryCache) get(key string) ([]SearchResult, bool) {
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

func (qc *queryCache) put(key string, results []SearchResult) {
	qc.mu.Lock()
	defer qc.mu.Unlock()
	if _, ok := qc.entries[key]; !ok {
		if len(qc.order) >= qc.maxSize {
			// Evict oldest
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
	qc.entries = make(map[string]queryCacheEntry, qc.maxSize)
	qc.order = qc.order[:0]
}

// SQLiteVectorStore implements VectorStore using SQLite for persistence
// with an in-memory vector cache for fast similarity search.
type SQLiteVectorStore struct {
	db    *sql.DB
	mu    sync.RWMutex
	cache []cachedChunk // flat cache for backward compat
	// Product-partitioned index: productID -> indices into cache.
	// Empty string key ("") holds chunks with no product (public library).
	productIndex map[string][]int
	loaded       bool
	searchCache  *queryCache // LRU cache for recent search results
}

// NewSQLiteVectorStore creates a new SQLiteVectorStore with the given database connection.
func NewSQLiteVectorStore(db *sql.DB) *SQLiteVectorStore {
	return &SQLiteVectorStore{
		db:           db,
		productIndex: make(map[string][]int),
		searchCache:  newQueryCache(256, 5*time.Minute),
	}
}

// loadCache reads all chunks from the database into memory.
// Must be called with mu held for writing.
func (s *SQLiteVectorStore) loadCache() error {
	rows, err := s.db.Query(`SELECT document_id, document_name, chunk_index, chunk_text, embedding, COALESCE(image_url,''), COALESCE(product_id,'') FROM chunks`)
	if err != nil {
		return fmt.Errorf("failed to query chunks: %w", err)
	}
	defer rows.Close()

	var cache []cachedChunk
	productIndex := make(map[string][]int)

	for rows.Next() {
		var docID, docName, chunkText, imageURL, productID string
		var chunkIndex int
		var embeddingBytes []byte

		if err := rows.Scan(&docID, &docName, &chunkIndex, &chunkText, &embeddingBytes, &imageURL, &productID); err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}

		vec32 := DeserializeVectorF32(embeddingBytes)
		textLower := strings.ToLower(chunkText)

		idx := len(cache)
		cache = append(cache, cachedChunk{
			chunkText:    chunkText,
			chunkIndex:   chunkIndex,
			documentID:   docID,
			documentName: docName,
			vector:       vec32,
			norm:         vectorNormF32(vec32),
			imageURL:     imageURL,
			productID:    productID,
			textLower:    textLower,
			bigrams:      charBigrams(textLower),
		})
		productIndex[productID] = append(productIndex[productID], idx)
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating rows: %w", err)
	}

	s.cache = cache
	s.productIndex = productIndex
	s.loaded = true
	return nil
}

// ensureCache loads the cache if not already loaded.
func (s *SQLiteVectorStore) ensureCache() error {
	if s.loaded {
		return nil
	}
	return s.loadCache()
}

// vectorNormF32 computes the L2 norm of a float32 vector.
func vectorNormF32(v []float32) float32 {
	var sum float32
	for _, x := range v {
		sum += x * x
	}
	return float32(math.Sqrt(float64(sum)))
}

// vectorNorm computes the L2 norm of a float64 vector (kept for API compat).
func vectorNorm(v []float64) float64 {
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	return math.Sqrt(sum)
}

// toFloat32 converts a float64 slice to float32 for cache-compatible search.
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
		cached cachedChunk
		productID string
	}
	var newEntries []newEntry

	for _, chunk := range chunks {
		chunkID := fmt.Sprintf("%s-%d", docID, chunk.ChunkIndex)
		embeddingBytes := SerializeVector(chunk.Vector)

		_, err := stmt.Exec(chunkID, docID, chunk.DocumentName, chunk.ChunkIndex, chunk.ChunkText, embeddingBytes, chunk.ImageURL, chunk.ProductID)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to insert chunk %s: %w", chunkID, err)
		}

		vec32 := toFloat32(chunk.Vector)
		textLower := strings.ToLower(chunk.ChunkText)
		newEntries = append(newEntries, newEntry{
			cached: cachedChunk{
				chunkText:    chunk.ChunkText,
				chunkIndex:   chunk.ChunkIndex,
				documentID:   chunk.DocumentID,
				documentName: chunk.DocumentName,
				vector:       vec32,
				norm:         vectorNormF32(vec32),
				imageURL:     chunk.ImageURL,
				productID:    chunk.ProductID,
				textLower:    textLower,
				bigrams:      charBigrams(textLower),
			},
			productID: chunk.ProductID,
		})
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Update cache
	if s.loaded {
		for _, ne := range newEntries {
			idx := len(s.cache)
			s.cache = append(s.cache, ne.cached)
			s.productIndex[ne.productID] = append(s.productIndex[ne.productID], idx)
		}
	} else {
		if err := s.loadCache(); err != nil {
			return err
		}
	}

	// Invalidate search cache since data changed
	s.searchCache.invalidate()
	return nil
}

// getRelevantIndices returns cache indices relevant for the given productID.
// If productID is empty, returns all indices. Otherwise returns the union of
// the product's chunks and the public library (empty productID).
func (s *SQLiteVectorStore) getRelevantIndices(productID string) []int {
	if productID == "" {
		// No filter — return all
		indices := make([]int, len(s.cache))
		for i := range indices {
			indices[i] = i
		}
		return indices
	}
	// Union of requested product + public library
	productChunks := s.productIndex[productID]
	publicChunks := s.productIndex[""]
	total := len(productChunks) + len(publicChunks)
	if total == 0 {
		return nil
	}
	indices := make([]int, 0, total)
	indices = append(indices, productChunks...)
	indices = append(indices, publicChunks...)
	return indices
}

// dotProductF32Unrolled computes dot product with 4-way loop unrolling for better ILP.
func dotProductF32Unrolled(a, b []float32) float32 {
	n := len(a)
	var sum0, sum1, sum2, sum3 float32
	i := 0
	// 4-way unrolled loop
	for ; i <= n-4; i += 4 {
		sum0 += a[i] * b[i]
		sum1 += a[i+1] * b[i+1]
		sum2 += a[i+2] * b[i+2]
		sum3 += a[i+3] * b[i+3]
	}
	// Handle remainder
	for ; i < n; i++ {
		sum0 += a[i] * b[i]
	}
	return sum0 + sum1 + sum2 + sum3
}

// minWorkersThreshold is the minimum number of items per worker to avoid goroutine overhead.
const minWorkersThreshold = 500

// Search uses the in-memory cache with concurrent cosine similarity computation.
// Optimizations over baseline:
// - Product-partitioned index skips irrelevant chunks before similarity computation
// - Float32 dot product with 4-way unrolling for better CPU throughput
// - Adaptive worker count avoids goroutine overhead on small datasets
// - LRU cache returns instant results for repeated queries
func (s *SQLiteVectorStore) Search(queryVector []float64, topK int, threshold float64, productID string) ([]SearchResult, error) {
	// Check LRU cache first
	cacheKey := fmt.Sprintf("v:%x:k%d:t%.4f:p%s", queryVector[:min(4, len(queryVector))], topK, threshold, productID)
	if cached, ok := s.searchCache.get(cacheKey); ok {
		return cached, nil
	}

	// Ensure cache is loaded
	s.mu.Lock()
	if err := s.ensureCache(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	cache := s.cache
	indices := s.getRelevantIndices(productID)
	s.mu.Unlock()

	if len(cache) == 0 || len(indices) == 0 {
		return nil, nil
	}

	// Convert query to float32 for cache-compatible computation
	queryF32 := toFloat32(queryVector)
	queryNorm := vectorNormF32(queryF32)
	if queryNorm == 0 {
		return nil, nil
	}

	thresholdF32 := float32(threshold)

	// Adaptive concurrency: avoid goroutine overhead for small datasets
	numWorkers := runtime.NumCPU()
	if len(indices) < minWorkersThreshold {
		numWorkers = 1
	} else if numWorkers > len(indices)/minWorkersThreshold {
		numWorkers = len(indices) / minWorkersThreshold
		if numWorkers < 1 {
			numWorkers = 1
		}
	}

	// Partition work over relevant indices only
	chunkSize := (len(indices) + numWorkers - 1) / numWorkers
	type partialResult struct {
		results []SearchResult
	}
	resultsCh := make(chan partialResult, numWorkers)

	for w := 0; w < numWorkers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if end > len(indices) {
			end = len(indices)
		}
		go func(idxSlice []int) {
			var local []SearchResult
			for _, idx := range idxSlice {
				c := &cache[idx]
				if c.norm == 0 || len(c.vector) != len(queryF32) {
					continue
				}
				dot := dotProductF32Unrolled(queryF32, c.vector)
				score := dot / (queryNorm * c.norm)
				if score >= thresholdF32 {
					local = append(local, SearchResult{
						ChunkText:    c.chunkText,
						ChunkIndex:   c.chunkIndex,
						DocumentID:   c.documentID,
						DocumentName: c.documentName,
						Score:        float64(score),
						ImageURL:     c.imageURL,
						ProductID:    c.productID,
					})
				}
			}
			resultsCh <- partialResult{results: local}
		}(indices[start:end])
	}

	// Collect results
	var allResults []SearchResult
	for w := 0; w < numWorkers; w++ {
		pr := <-resultsCh
		allResults = append(allResults, pr.results...)
	}

	sort.Slice(allResults, func(i, j int) bool {
		return allResults[i].Score > allResults[j].Score
	})

	if len(allResults) > topK {
		allResults = allResults[:topK]
	}

	// Store in LRU cache
	s.searchCache.put(cacheKey, allResults)

	return allResults, nil
}

// TextSearch performs a text-based similarity search against the in-memory cache
// using keyword overlap and pre-computed character bigram Jaccard similarity.
// This is Level 1 of the 3-level matching: zero API cost.
//
// Optimization: bigrams are pre-computed at index time, so TextSearch only
// computes bigrams for the query (once), not for every chunk.
func (s *SQLiteVectorStore) TextSearch(query string, topK int, threshold float64, productID string) ([]SearchResult, error) {
	s.mu.Lock()
	if err := s.ensureCache(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	cache := s.cache
	indices := s.getRelevantIndices(productID)
	s.mu.Unlock()

	if len(cache) == 0 || len(indices) == 0 {
		return nil, nil
	}

	queryLower := strings.ToLower(query)
	queryBigrams := charBigrams(queryLower)
	queryKeywords := extractKeywords(queryLower)

	type scored struct {
		idx   int
		score float64
	}

	// Adaptive concurrency
	numWorkers := runtime.NumCPU()
	if len(indices) < minWorkersThreshold {
		numWorkers = 1
	} else if numWorkers > len(indices)/minWorkersThreshold {
		numWorkers = len(indices) / minWorkersThreshold
		if numWorkers < 1 {
			numWorkers = 1
		}
	}

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
				c := &cache[idx]
				// Use pre-computed textLower and bigrams (no per-query recomputation)
				kwScore := keywordOverlap(queryKeywords, c.textLower)
				bigramScore := jaccardBigrams(queryBigrams, c.bigrams)

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
		c := &cache[h.idx]
		results[i] = SearchResult{
			ChunkText:    c.chunkText,
			ChunkIndex:   c.chunkIndex,
			DocumentID:   c.documentID,
			DocumentName: c.documentName,
			Score:        h.score,
			ImageURL:     c.imageURL,
			ProductID:    c.productID,
		}
	}
	return results, nil
}

// charBigrams extracts character bigrams from a string.
func charBigrams(s string) map[string]bool {
	runes := []rune(s)
	result := make(map[string]bool, len(runes))
	for i := 0; i < len(runes)-1; i++ {
		result[string(runes[i:i+2])] = true
	}
	return result
}

// jaccardBigrams computes Jaccard similarity between two bigram sets.
func jaccardBigrams(a, b map[string]bool) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	// Iterate over the smaller set for efficiency
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

// extractKeywords splits text into meaningful tokens (≥2 runes), deduped.
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

// keywordOverlap computes the fraction of query keywords found in the chunk text.
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

	// Rebuild cache and product index excluding deleted document
	if s.loaded {
		newCache := make([]cachedChunk, 0, len(s.cache))
		newProductIndex := make(map[string][]int)
		for _, c := range s.cache {
			if c.documentID != docID {
				idx := len(newCache)
				newCache = append(newCache, c)
				newProductIndex[c.productID] = append(newProductIndex[c.productID], idx)
			}
		}
		s.cache = newCache
		s.productIndex = newProductIndex
	}

	// Invalidate search cache since data changed
	s.searchCache.invalidate()
	return nil
}

// min returns the smaller of two ints.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
