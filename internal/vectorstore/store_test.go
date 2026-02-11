package vectorstore

import (
	"database/sql"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"testing"
	"testing/quick"

	_ "github.com/mattn/go-sqlite3"
)

// setupTestDB creates a temporary SQLite database with the chunks table.
func setupTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS chunks (
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
		db.Close()
		t.Fatalf("failed to create chunks table: %v", err)
	}

	cleanup := func() {
		db.Close()
		os.RemoveAll(dir)
	}
	return db, cleanup
}

func TestNewSQLiteVectorStore(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()

	store := NewSQLiteVectorStore(db)
	if store == nil {
		t.Fatal("expected non-nil store")
	}
	if store.db != db {
		t.Fatal("store.db should match provided db")
	}
}

func TestStoreAndRetrieve(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewSQLiteVectorStore(db)

	chunks := []VectorChunk{
		{ChunkText: "hello world", ChunkIndex: 0, DocumentID: "doc1", DocumentName: "test.pdf", Vector: []float64{1.0, 0.0, 0.0}},
		{ChunkText: "foo bar", ChunkIndex: 1, DocumentID: "doc1", DocumentName: "test.pdf", Vector: []float64{0.0, 1.0, 0.0}},
	}

	if err := store.Store("doc1", chunks); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	// Verify rows exist
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM chunks WHERE document_id = ?", "doc1").Scan(&count); err != nil {
		t.Fatalf("count query failed: %v", err)
	}
	if count != 2 {
		t.Errorf("expected 2 chunks, got %d", count)
	}

	// Verify chunk IDs
	var id string
	db.QueryRow("SELECT id FROM chunks WHERE chunk_index = 0 AND document_id = ?", "doc1").Scan(&id)
	if id != "doc1-0" {
		t.Errorf("expected chunk id 'doc1-0', got '%s'", id)
	}
}

func TestStoreEmptyChunks(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewSQLiteVectorStore(db)

	if err := store.Store("doc-empty", []VectorChunk{}); err != nil {
		t.Fatalf("Store with empty chunks should not fail: %v", err)
	}

	var count int
	db.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 chunks, got %d", count)
	}
}

func TestSearchReturnsTopK(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewSQLiteVectorStore(db)

	chunks := []VectorChunk{
		{ChunkText: "very relevant", ChunkIndex: 0, DocumentID: "doc1", DocumentName: "a.pdf", Vector: []float64{1.0, 0.0, 0.0}},
		{ChunkText: "somewhat relevant", ChunkIndex: 1, DocumentID: "doc1", DocumentName: "a.pdf", Vector: []float64{0.7, 0.7, 0.0}},
		{ChunkText: "not relevant", ChunkIndex: 2, DocumentID: "doc1", DocumentName: "a.pdf", Vector: []float64{0.0, 0.0, 1.0}},
	}
	if err := store.Store("doc1", chunks); err != nil {
		t.Fatalf("Store failed: %v", err)
	}

	query := []float64{1.0, 0.0, 0.0}
	results, err := store.Search(query, 2, 0.0, "")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	if len(results) > 2 {
		t.Errorf("expected at most 2 results, got %d", len(results))
	}
}

func TestSearchSortedDescending(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewSQLiteVectorStore(db)

	chunks := []VectorChunk{
		{ChunkText: "low", ChunkIndex: 0, DocumentID: "doc1", DocumentName: "a.pdf", Vector: []float64{0.0, 1.0, 0.0}},
		{ChunkText: "high", ChunkIndex: 1, DocumentID: "doc1", DocumentName: "a.pdf", Vector: []float64{1.0, 0.0, 0.0}},
		{ChunkText: "mid", ChunkIndex: 2, DocumentID: "doc1", DocumentName: "a.pdf", Vector: []float64{0.7, 0.7, 0.0}},
	}
	store.Store("doc1", chunks)

	results, err := store.Search([]float64{1.0, 0.0, 0.0}, 10, 0.0, "")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	for i := 1; i < len(results); i++ {
		if results[i].Score > results[i-1].Score {
			t.Errorf("results not sorted descending: score[%d]=%f > score[%d]=%f",
				i, results[i].Score, i-1, results[i-1].Score)
		}
	}
}

func TestSearchThresholdFiltering(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewSQLiteVectorStore(db)

	chunks := []VectorChunk{
		{ChunkText: "exact match", ChunkIndex: 0, DocumentID: "doc1", DocumentName: "a.pdf", Vector: []float64{1.0, 0.0}},
		{ChunkText: "orthogonal", ChunkIndex: 1, DocumentID: "doc1", DocumentName: "a.pdf", Vector: []float64{0.0, 1.0}},
	}
	store.Store("doc1", chunks)

	results, err := store.Search([]float64{1.0, 0.0}, 10, 0.5, "")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}

	for _, r := range results {
		if r.Score < 0.5 {
			t.Errorf("result below threshold: score=%f, text=%s", r.Score, r.ChunkText)
		}
	}

	// Only the exact match should pass threshold 0.5
	if len(results) != 1 {
		t.Errorf("expected 1 result above threshold, got %d", len(results))
	}
}

func TestSearchEmptyStore(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewSQLiteVectorStore(db)

	results, err := store.Search([]float64{1.0, 0.0, 0.0}, 5, 0.0, "")
	if err != nil {
		t.Fatalf("Search on empty store failed: %v", err)
	}
	if len(results) != 0 {
		t.Errorf("expected 0 results from empty store, got %d", len(results))
	}
}

func TestSearchResultFields(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewSQLiteVectorStore(db)

	chunks := []VectorChunk{
		{ChunkText: "test content", ChunkIndex: 3, DocumentID: "doc-abc", DocumentName: "report.pdf", Vector: []float64{1.0, 0.0}},
	}
	store.Store("doc-abc", chunks)

	results, err := store.Search([]float64{1.0, 0.0}, 5, 0.0, "")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result, got %d", len(results))
	}

	r := results[0]
	if r.ChunkText != "test content" {
		t.Errorf("ChunkText: got %q, want %q", r.ChunkText, "test content")
	}
	if r.ChunkIndex != 3 {
		t.Errorf("ChunkIndex: got %d, want 3", r.ChunkIndex)
	}
	if r.DocumentID != "doc-abc" {
		t.Errorf("DocumentID: got %q, want %q", r.DocumentID, "doc-abc")
	}
	if r.DocumentName != "report.pdf" {
		t.Errorf("DocumentName: got %q, want %q", r.DocumentName, "report.pdf")
	}
	if math.Abs(r.Score-1.0) > 1e-10 {
		t.Errorf("Score: got %f, want 1.0", r.Score)
	}
}

func TestDeleteByDocID(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewSQLiteVectorStore(db)

	// Store two documents
	store.Store("doc1", []VectorChunk{
		{ChunkText: "doc1 chunk", ChunkIndex: 0, DocumentID: "doc1", DocumentName: "a.pdf", Vector: []float64{1.0, 0.0}},
	})
	store.Store("doc2", []VectorChunk{
		{ChunkText: "doc2 chunk", ChunkIndex: 0, DocumentID: "doc2", DocumentName: "b.pdf", Vector: []float64{0.0, 1.0}},
	})

	if err := store.DeleteByDocID("doc1"); err != nil {
		t.Fatalf("DeleteByDocID failed: %v", err)
	}

	// doc1 should be gone
	var count int
	db.QueryRow("SELECT COUNT(*) FROM chunks WHERE document_id = ?", "doc1").Scan(&count)
	if count != 0 {
		t.Errorf("expected 0 chunks for doc1 after delete, got %d", count)
	}

	// doc2 should still exist
	db.QueryRow("SELECT COUNT(*) FROM chunks WHERE document_id = ?", "doc2").Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 chunk for doc2, got %d", count)
	}
}

func TestDeleteByDocIDNonExistent(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewSQLiteVectorStore(db)

	// Deleting a non-existent doc should not error
	if err := store.DeleteByDocID("nonexistent"); err != nil {
		t.Fatalf("DeleteByDocID for non-existent doc should not fail: %v", err)
	}
}

func TestStorePersistence(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "persist.db")

	// Open, create table, store data, close
	db1, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open db1: %v", err)
	}
	db1.Exec(`CREATE TABLE IF NOT EXISTS chunks (
		id TEXT PRIMARY KEY, document_id TEXT NOT NULL, document_name TEXT NOT NULL,
		chunk_index INTEGER NOT NULL, chunk_text TEXT NOT NULL, embedding BLOB NOT NULL,
		image_url TEXT DEFAULT '', product_id TEXT DEFAULT '', created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`)

	store1 := NewSQLiteVectorStore(db1)
	store1.Store("doc1", []VectorChunk{
		{ChunkText: "persistent data", ChunkIndex: 0, DocumentID: "doc1", DocumentName: "p.pdf", Vector: []float64{0.5, 0.5}},
	})
	db1.Close()

	// Reopen and verify data persists
	db2, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("open db2: %v", err)
	}
	defer db2.Close()

	store2 := NewSQLiteVectorStore(db2)
	results, err := store2.Search([]float64{0.5, 0.5}, 5, 0.0, "")
	if err != nil {
		t.Fatalf("Search after reopen: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result after reopen, got %d", len(results))
	}
	if results[0].ChunkText != "persistent data" {
		t.Errorf("expected 'persistent data', got %q", results[0].ChunkText)
	}
}

// TestProperty8_ProductIsolationSearch verifies that when searching with a productID,
// results only contain chunks matching that productID or the public library (empty productID).
// Chunks belonging to other products must never appear.
//
// **Feature: multi-product-support, Property 8: 产品隔离检索**
// **Validates: Requirements 8.1, 8.2, 8.3**
func TestProperty8_ProductIsolationSearch(t *testing.T) {
	counter := 0
	f := func(seed uint8) bool {
		db, cleanup := setupTestDB(t)
		defer cleanup()
		store := NewSQLiteVectorStore(db)

		counter++

		// Create chunks for 3 products + public library
		productA := fmt.Sprintf("product-a-%d", counter)
		productB := fmt.Sprintf("product-b-%d", counter)
		productC := fmt.Sprintf("product-c-%d", counter)

		// Use a simple 3D vector space; all vectors point in similar directions
		// so they'll all be returned (above threshold 0)
		chunksA := []VectorChunk{
			{ChunkText: "chunk A1", ChunkIndex: 0, DocumentID: "docA", DocumentName: "a.pdf", Vector: []float64{1.0, 0.1, 0.0}, ProductID: productA},
			{ChunkText: "chunk A2", ChunkIndex: 1, DocumentID: "docA", DocumentName: "a.pdf", Vector: []float64{0.9, 0.2, 0.0}, ProductID: productA},
		}
		chunksB := []VectorChunk{
			{ChunkText: "chunk B1", ChunkIndex: 0, DocumentID: "docB", DocumentName: "b.pdf", Vector: []float64{0.8, 0.3, 0.0}, ProductID: productB},
		}
		chunksC := []VectorChunk{
			{ChunkText: "chunk C1", ChunkIndex: 0, DocumentID: "docC", DocumentName: "c.pdf", Vector: []float64{0.7, 0.4, 0.0}, ProductID: productC},
		}
		chunksPublic := []VectorChunk{
			{ChunkText: "chunk public", ChunkIndex: 0, DocumentID: "docPub", DocumentName: "pub.pdf", Vector: []float64{0.6, 0.5, 0.0}, ProductID: ""},
		}

		for _, batch := range []struct {
			docID  string
			chunks []VectorChunk
		}{
			{"docA", chunksA},
			{"docB", chunksB},
			{"docC", chunksC},
			{"docPub", chunksPublic},
		} {
			if err := store.Store(batch.docID, batch.chunks); err != nil {
				t.Logf("Store failed: %v", err)
				return false
			}
		}

		// Search with productA scope - should only get productA + public
		query := []float64{1.0, 0.0, 0.0}
		results, err := store.Search(query, 100, 0.0, productA)
		if err != nil {
			t.Logf("Search failed: %v", err)
			return false
		}
		for _, r := range results {
			if r.ProductID != productA && r.ProductID != "" {
				t.Logf("Search(productA): got result with productID=%q, expected %q or empty", r.ProductID, productA)
				return false
			}
		}

		// Search with productB scope - should only get productB + public
		results, err = store.Search(query, 100, 0.0, productB)
		if err != nil {
			t.Logf("Search(productB) failed: %v", err)
			return false
		}
		for _, r := range results {
			if r.ProductID != productB && r.ProductID != "" {
				t.Logf("Search(productB): got result with productID=%q, expected %q or empty", r.ProductID, productB)
				return false
			}
		}

		// TextSearch with productA scope - same isolation
		textResults, err := store.TextSearch("chunk", 100, 0.0, productA)
		if err != nil {
			t.Logf("TextSearch failed: %v", err)
			return false
		}
		for _, r := range textResults {
			if r.ProductID != productA && r.ProductID != "" {
				t.Logf("TextSearch(productA): got result with productID=%q, expected %q or empty", r.ProductID, productA)
				return false
			}
		}

		// Search with empty productID - should return ALL chunks
		allResults, err := store.Search(query, 100, 0.0, "")
		if err != nil {
			t.Logf("Search(empty) failed: %v", err)
			return false
		}
		if len(allResults) != 5 {
			t.Logf("Search(empty): expected 5 results (all chunks), got %d", len(allResults))
			return false
		}

		return true
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 100}); err != nil {
		t.Error(err)
	}
}
