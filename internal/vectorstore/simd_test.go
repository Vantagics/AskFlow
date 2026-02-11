package vectorstore

import (
	"database/sql"
	"fmt"
	"math/rand"
	"testing"

	_ "github.com/mattn/go-sqlite3"
)

// TestSIMDCapability verifies that SIMDCapability returns a non-empty string.
func TestSIMDCapability(t *testing.T) {
	cap := SIMDCapability()
	if cap == "" {
		t.Error("SIMDCapability returned empty string")
	}
	t.Logf("SIMD capability: %s", cap)
}

// --- End-to-end Search benchmarks ---

func setupBenchStore(b *testing.B, numChunks, dim int) (*SQLiteVectorStore, []float64) {
	b.Helper()
	dir := b.TempDir()
	dbPath := dir + "/bench.db"
	db, err := sql.Open("sqlite3", dbPath)
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { db.Close() })
	db.Exec(`CREATE TABLE IF NOT EXISTS chunks (
		id TEXT PRIMARY KEY, document_id TEXT NOT NULL, document_name TEXT NOT NULL,
		chunk_index INTEGER NOT NULL, chunk_text TEXT NOT NULL, embedding BLOB NOT NULL,
		image_url TEXT DEFAULT '', product_id TEXT DEFAULT '', created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`)

	store := NewSQLiteVectorStore(db)
	rng := rand.New(rand.NewSource(42))

	batchSize := 100
	for start := 0; start < numChunks; start += batchSize {
		end := start + batchSize
		if end > numChunks {
			end = numChunks
		}
		chunks := make([]VectorChunk, 0, end-start)
		for i := start; i < end; i++ {
			vec := make([]float64, dim)
			for j := range vec {
				vec[j] = rng.Float64()*2 - 1
			}
			chunks = append(chunks, VectorChunk{
				ChunkText:    fmt.Sprintf("chunk %d text content for benchmarking", i),
				ChunkIndex:   i,
				DocumentID:   fmt.Sprintf("doc-%d", i/10),
				DocumentName: fmt.Sprintf("doc-%d.pdf", i/10),
				Vector:       vec,
				ProductID:    "",
			})
		}
		if err := store.Store(fmt.Sprintf("doc-%d", start/10), chunks); err != nil {
			b.Fatal(err)
		}
	}

	query := make([]float64, dim)
	for i := range query {
		query[i] = rng.Float64()*2 - 1
	}

	// Warm up cache
	store.Search(query, 5, 0.0, "")

	return store, query
}

func BenchmarkSearch_1000x1536(b *testing.B) {
	store, query := setupBenchStore(b, 1000, 1536)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.Search(query, 5, 0.0, "")
	}
}

func BenchmarkSearch_5000x1536(b *testing.B) {
	store, query := setupBenchStore(b, 5000, 1536)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.Search(query, 5, 0.0, "")
	}
}

func BenchmarkSearch_10000x768(b *testing.B) {
	store, query := setupBenchStore(b, 10000, 768)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		store.Search(query, 5, 0.0, "")
	}
}
