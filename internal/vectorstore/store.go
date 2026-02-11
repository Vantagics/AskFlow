// Package vectorstore provides vector storage and similarity search using SQLite.
// It stores document embeddings and supports cosine similarity based retrieval
// with an in-memory cache for fast search and concurrent similarity computation.
package vectorstore

import (
	"database/sql"
	"fmt"
	"math"
	"runtime"
	"sort"
	"strings"
	"sync"

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


// cachedChunk holds a chunk's metadata and pre-computed norm for fast similarity.
type cachedChunk struct {
	chunkText    string
	chunkIndex   int
	documentID   string
	documentName string
	vector       []float64
	norm         float64
	imageURL     string
	productID    string
}


// SQLiteVectorStore implements VectorStore using SQLite for persistence
// with an in-memory vector cache for fast similarity search.
type SQLiteVectorStore struct {
	db    *sql.DB
	mu    sync.RWMutex
	cache []cachedChunk // in-memory vector index
	loaded bool
}

// NewSQLiteVectorStore creates a new SQLiteVectorStore with the given database connection.
func NewSQLiteVectorStore(db *sql.DB) *SQLiteVectorStore {
	return &SQLiteVectorStore{db: db}
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
	for rows.Next() {
		var docID, docName, chunkText, imageURL, productID string
		var chunkIndex int
		var embeddingBytes []byte

		if err := rows.Scan(&docID, &docName, &chunkIndex, &chunkText, &embeddingBytes, &imageURL, &productID); err != nil {
			return fmt.Errorf("failed to scan row: %w", err)
		}

		vec := DeserializeVector(embeddingBytes)
		cache = append(cache, cachedChunk{
			chunkText:    chunkText,
			chunkIndex:   chunkIndex,
			documentID:   docID,
			documentName: docName,
			vector:       vec,
			norm:         vectorNorm(vec),
			imageURL:     imageURL,
			productID:    productID,
		})
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("error iterating rows: %w", err)
	}

	s.cache = cache
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

// vectorNorm computes the L2 norm of a vector.
func vectorNorm(v []float64) float64 {
	var sum float64
	for _, x := range v {
		sum += x * x
	}
	return math.Sqrt(sum)
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

	var newCached []cachedChunk
	for _, chunk := range chunks {
		chunkID := fmt.Sprintf("%s-%d", docID, chunk.ChunkIndex)
		embeddingBytes := SerializeVector(chunk.Vector)

		_, err := stmt.Exec(chunkID, docID, chunk.DocumentName, chunk.ChunkIndex, chunk.ChunkText, embeddingBytes, chunk.ImageURL, chunk.ProductID)
		if err != nil {
			tx.Rollback()
			return fmt.Errorf("failed to insert chunk %s: %w", chunkID, err)
		}

		newCached = append(newCached, cachedChunk{
			chunkText:    chunk.ChunkText,
			chunkIndex:   chunk.ChunkIndex,
			documentID:   chunk.DocumentID,
			documentName: chunk.DocumentName,
			vector:       chunk.Vector,
			norm:         vectorNorm(chunk.Vector),
			imageURL:     chunk.ImageURL,
			productID:    chunk.ProductID,
		})
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit transaction: %w", err)
	}

	// Update cache
	if s.loaded {
		// Cache already loaded — just append new entries
		s.cache = append(s.cache, newCached...)
	} else {
		// First access — load everything from DB (includes just-inserted rows)
		if err := s.loadCache(); err != nil {
			return err
		}
	}
	return nil
}

// Search uses the in-memory cache with concurrent cosine similarity computation.
// It partitions the cache across goroutines, filters by threshold, and returns top-K.
// When productID is non-empty, only chunks matching that productID or the public library (empty productID) are returned.
func (s *SQLiteVectorStore) Search(queryVector []float64, topK int, threshold float64, productID string) ([]SearchResult, error) {
	// Ensure cache is loaded
	s.mu.Lock()
	if err := s.ensureCache(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	// Copy cache reference under lock, then release
	cache := s.cache
	s.mu.Unlock()

	if len(cache) == 0 {
		return nil, nil
	}

	queryNorm := vectorNorm(queryVector)
	if queryNorm == 0 {
		return nil, nil
	}

	// Determine concurrency level
	numWorkers := runtime.NumCPU()
	if numWorkers > len(cache) {
		numWorkers = len(cache)
	}
	if numWorkers < 1 {
		numWorkers = 1
	}

	// Partition work
	chunkSize := (len(cache) + numWorkers - 1) / numWorkers
	type partialResult struct {
		results []SearchResult
	}
	resultsCh := make(chan partialResult, numWorkers)

	for w := 0; w < numWorkers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if end > len(cache) {
			end = len(cache)
		}
		go func(items []cachedChunk) {
			var local []SearchResult
			for i := range items {
				c := &items[i]
				// Product isolation: skip chunks not matching the requested product
				if productID != "" && c.productID != productID && c.productID != "" {
					continue
				}
				if c.norm == 0 || len(c.vector) != len(queryVector) {
					continue
				}
				// Inline dot product for speed
				var dot float64
				for j := range queryVector {
					dot += queryVector[j] * c.vector[j]
				}
				score := dot / (queryNorm * c.norm)
				if score >= threshold {
					local = append(local, SearchResult{
						ChunkText:    c.chunkText,
						ChunkIndex:   c.chunkIndex,
						DocumentID:   c.documentID,
						DocumentName: c.documentName,
						Score:        score,
						ImageURL:     c.imageURL,
						ProductID:    c.productID,
					})
				}
			}
			resultsCh <- partialResult{results: local}
		}(cache[start:end])
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

	return allResults, nil
}

// TextSearch performs a text-based similarity search against the in-memory cache
// using keyword overlap and character bigram Jaccard similarity.
// This is Level 1 of the 3-level matching: zero API cost.
// Returns results sorted by text similarity score descending.
// Uses concurrent workers for large caches.
// When productID is non-empty, only chunks matching that productID or the public library (empty productID) are returned.
func (s *SQLiteVectorStore) TextSearch(query string, topK int, threshold float64, productID string) ([]SearchResult, error) {
	s.mu.Lock()
	if err := s.ensureCache(); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	cache := s.cache
	s.mu.Unlock()

	if len(cache) == 0 {
		return nil, nil
	}

	queryLower := strings.ToLower(query)
	queryBigrams := charBigrams(queryLower)
	queryKeywords := extractKeywords(queryLower)

	type scored struct {
		idx   int
		score float64
	}

	// Use concurrent workers for large caches
	numWorkers := runtime.NumCPU()
	if numWorkers > len(cache) {
		numWorkers = len(cache)
	}
	if numWorkers < 1 {
		numWorkers = 1
	}

	chunkSize := (len(cache) + numWorkers - 1) / numWorkers
	type partialHits struct {
		hits []scored
	}
	hitsCh := make(chan partialHits, numWorkers)

	for w := 0; w < numWorkers; w++ {
		start := w * chunkSize
		end := start + chunkSize
		if end > len(cache) {
			end = len(cache)
		}
		go func(items []cachedChunk, baseIdx int) {
			var local []scored
			for i := range items {
				c := &items[i]
				// Product isolation: skip chunks not matching the requested product
				if productID != "" && c.productID != productID && c.productID != "" {
					continue
				}
				chunkLower := strings.ToLower(c.chunkText)

				kwScore := keywordOverlap(queryKeywords, chunkLower)
				bigramScore := jaccardBigrams(queryBigrams, charBigrams(chunkLower))

				score := kwScore*0.6 + bigramScore*0.4

				if score >= threshold {
					local = append(local, scored{idx: baseIdx + i, score: score})
				}
			}
			hitsCh <- partialHits{hits: local}
		}(cache[start:end], start)
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
	result := make(map[string]bool)
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
	seen := make(map[string]bool)
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

	// Update cache: build a new slice to allow GC of removed entries' vectors
	if s.loaded {
		newCache := make([]cachedChunk, 0, len(s.cache))
		for _, c := range s.cache {
			if c.documentID != docID {
				newCache = append(newCache, c)
			}
		}
		s.cache = newCache
	}

	return nil
}
