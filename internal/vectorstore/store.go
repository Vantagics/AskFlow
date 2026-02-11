// Package vectorstore provides vector storage and similarity search using SQLite.
// This package is a thin wrapper around the sqlite-vec library, maintaining
// backward compatibility with the existing helpdesk codebase.
package vectorstore

import (
	"database/sql"

	sqlitevec "github.com/nicexipi/sqlite-vec"
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
	StartTime    float64 `json:"start_time,omitempty"`
	EndTime      float64 `json:"end_time,omitempty"`
}

// SQLiteVectorStore wraps the sqlite-vec library's implementation.
type SQLiteVectorStore struct {
	inner *sqlitevec.SQLiteVectorStore
}

// SIMDCapability returns a human-readable string describing the active SIMD
// acceleration path for vector operations.
func SIMDCapability() string {
	return sqlitevec.SIMDCapability()
}

// NewSQLiteVectorStore creates a new SQLiteVectorStore with the given database connection.
func NewSQLiteVectorStore(db *sql.DB) *SQLiteVectorStore {
	return &SQLiteVectorStore{
		inner: sqlitevec.NewSQLiteVectorStore(db),
	}
}

// toLibChunks converts local VectorChunk slice to library VectorChunk slice.
func toLibChunks(chunks []VectorChunk) []sqlitevec.VectorChunk {
	out := make([]sqlitevec.VectorChunk, len(chunks))
	for i, c := range chunks {
		out[i] = sqlitevec.VectorChunk{
			ChunkText:    c.ChunkText,
			ChunkIndex:   c.ChunkIndex,
			DocumentID:   c.DocumentID,
			DocumentName: c.DocumentName,
			Vector:       c.Vector,
			ImageURL:     c.ImageURL,
			PartitionID:  c.ProductID,
		}
	}
	return out
}

// fromLibResults converts library SearchResult slice to local SearchResult slice.
func fromLibResults(results []sqlitevec.SearchResult) []SearchResult {
	out := make([]SearchResult, len(results))
	for i, r := range results {
		out[i] = SearchResult{
			ChunkText:    r.ChunkText,
			ChunkIndex:   r.ChunkIndex,
			DocumentID:   r.DocumentID,
			DocumentName: r.DocumentName,
			Score:        r.Score,
			ImageURL:     r.ImageURL,
			ProductID:    r.PartitionID,
			StartTime:    r.StartTime,
			EndTime:      r.EndTime,
		}
	}
	return out
}

// Store inserts a batch of VectorChunks into the chunks table and updates the cache.
func (s *SQLiteVectorStore) Store(docID string, chunks []VectorChunk) error {
	return s.inner.Store(docID, toLibChunks(chunks))
}

// Search performs cosine similarity search against stored vectors.
func (s *SQLiteVectorStore) Search(queryVector []float64, topK int, threshold float64, productID string) ([]SearchResult, error) {
	results, err := s.inner.Search(queryVector, topK, threshold, productID)
	if err != nil {
		return nil, err
	}
	return fromLibResults(results), nil
}

// TextSearch performs text-based similarity search.
func (s *SQLiteVectorStore) TextSearch(query string, topK int, threshold float64, productID string) ([]SearchResult, error) {
	results, err := s.inner.TextSearch(query, topK, threshold, productID)
	if err != nil {
		return nil, err
	}
	return fromLibResults(results), nil
}

// DeleteByDocID removes all chunks for the given document.
func (s *SQLiteVectorStore) DeleteByDocID(docID string) error {
	return s.inner.DeleteByDocID(docID)
}
