package sqlitevec

import (
	"database/sql"
	"math"
	"os"
	"path/filepath"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

func setupTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")

	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}

	if err := EnsureTable(db); err != nil {
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
}

func TestStoreAndSearch(t *testing.T) {
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

	results, err := store.Search([]float64{1.0, 0.0, 0.0}, 5, 0.0, "")
	if err != nil {
		t.Fatalf("Search failed: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].ChunkText != "hello world" {
		t.Errorf("expected first result 'hello world', got %q", results[0].ChunkText)
	}
	if math.Abs(results[0].Score-1.0) > 1e-6 {
		t.Errorf("expected score ~1.0, got %f", results[0].Score)
	}
}

func TestDeleteByDocID(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewSQLiteVectorStore(db)

	store.Store("doc1", []VectorChunk{
		{ChunkText: "doc1 chunk", ChunkIndex: 0, DocumentID: "doc1", DocumentName: "a.pdf", Vector: []float64{1.0, 0.0}},
	})
	store.Store("doc2", []VectorChunk{
		{ChunkText: "doc2 chunk", ChunkIndex: 0, DocumentID: "doc2", DocumentName: "b.pdf", Vector: []float64{0.0, 1.0}},
	})

	if err := store.DeleteByDocID("doc1"); err != nil {
		t.Fatalf("DeleteByDocID failed: %v", err)
	}

	results, _ := store.Search([]float64{1.0, 0.0}, 10, 0.0, "")
	if len(results) != 1 {
		t.Fatalf("expected 1 result after delete, got %d", len(results))
	}
	if results[0].DocumentID != "doc2" {
		t.Errorf("expected doc2, got %s", results[0].DocumentID)
	}
}

func TestTextSearch(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewSQLiteVectorStore(db)

	store.Store("doc1", []VectorChunk{
		{ChunkText: "machine learning algorithms", ChunkIndex: 0, DocumentID: "doc1", DocumentName: "ml.pdf", Vector: []float64{1.0, 0.0}},
		{ChunkText: "deep learning neural networks", ChunkIndex: 1, DocumentID: "doc1", DocumentName: "ml.pdf", Vector: []float64{0.0, 1.0}},
	})

	results, err := store.TextSearch("learning", 5, 0.0, "")
	if err != nil {
		t.Fatalf("TextSearch failed: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("expected 2 results, got %d", len(results))
	}
}

func TestPartitionIsolation(t *testing.T) {
	db, cleanup := setupTestDB(t)
	defer cleanup()
	store := NewSQLiteVectorStore(db)

	store.Store("docA", []VectorChunk{
		{ChunkText: "partition A", ChunkIndex: 0, DocumentID: "docA", DocumentName: "a.pdf", Vector: []float64{1.0, 0.0}, PartitionID: "partA"},
	})
	store.Store("docB", []VectorChunk{
		{ChunkText: "partition B", ChunkIndex: 0, DocumentID: "docB", DocumentName: "b.pdf", Vector: []float64{0.9, 0.1}, PartitionID: "partB"},
	})

	results, _ := store.Search([]float64{1.0, 0.0}, 10, 0.0, "partA")
	for _, r := range results {
		if r.PartitionID != "partA" && r.PartitionID != "" {
			t.Errorf("expected partA or empty, got %q", r.PartitionID)
		}
	}
}

func TestEnsureTable(t *testing.T) {
	dir := t.TempDir()
	dbPath := filepath.Join(dir, "test.db")
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()

	if err := EnsureTable(db); err != nil {
		t.Fatalf("EnsureTable failed: %v", err)
	}

	// Call again - should be idempotent
	if err := EnsureTable(db); err != nil {
		t.Fatalf("EnsureTable second call failed: %v", err)
	}

	// Verify table exists
	var count int
	err = db.QueryRow("SELECT COUNT(*) FROM chunks").Scan(&count)
	if err != nil {
		t.Fatalf("chunks table not created: %v", err)
	}
}
