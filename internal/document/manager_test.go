package document

import (
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"testing/quick"

	"helpdesk/internal/chunker"
	"helpdesk/internal/db"
	"helpdesk/internal/parser"
	"helpdesk/internal/vectorstore"
)

// mockEmbeddingService implements embedding.EmbeddingService for testing.
type mockEmbeddingService struct {
	embedFunc      func(text string) ([]float64, error)
	embedBatchFunc func(texts []string) ([][]float64, error)
}

func (m *mockEmbeddingService) Embed(text string) ([]float64, error) {
	if m.embedFunc != nil {
		return m.embedFunc(text)
	}
	return []float64{0.1, 0.2, 0.3}, nil
}

func (m *mockEmbeddingService) EmbedBatch(texts []string) ([][]float64, error) {
	if m.embedBatchFunc != nil {
		return m.embedBatchFunc(texts)
	}
	result := make([][]float64, len(texts))
	for i := range texts {
		result[i] = []float64{0.1, 0.2, 0.3}
	}
	return result, nil
}

func (m *mockEmbeddingService) EmbedImageURL(imageURL string) ([]float64, error) {
	return []float64{0.1, 0.2, 0.3}, nil
}// setupTestDB creates a temporary SQLite database for testing.
func setupTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "test-doc-manager-*.db")
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	tmpFile.Close()

	database, err := db.InitDB(tmpFile.Name())
	if err != nil {
		os.Remove(tmpFile.Name())
		t.Fatalf("failed to init db: %v", err)
	}

	return database, func() {
		database.Close()
		os.Remove(tmpFile.Name())
	}
}

// newTestManager creates a DocumentManager wired with real DB, real chunker/parser,
// and a mock embedding service.
func newTestManager(t *testing.T, database *sql.DB, es *mockEmbeddingService) *DocumentManager {
	t.Helper()
	p := &parser.DocumentParser{}
	c := chunker.NewTextChunker()
	vs := vectorstore.NewSQLiteVectorStore(database)
	dm := NewDocumentManager(p, c, es, vs, database)
	// Allow localhost URLs in tests (bypass SSRF protection)
	dm.validateURL = func(string) error { return nil }
	return dm
}

func TestUploadFile_UnsupportedType(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	dm := newTestManager(t, database, &mockEmbeddingService{})

	_, err := dm.UploadFile(UploadFileRequest{
		FileName: "test.txt",
		FileData: []byte("hello"),
		FileType: "txt",
	})
	if err == nil {
		t.Fatal("expected error for unsupported file type")
	}
	if err.Error() != "不支持的文件格式" {
		t.Fatalf("unexpected error message: %s", err.Error())
	}
}

func TestUploadFile_UnsupportedTypes(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	dm := newTestManager(t, database, &mockEmbeddingService{})

	unsupported := []string{"txt", "csv", "json", "xml", "zip", "mp3", "jpg"}
	for _, ft := range unsupported {
		_, err := dm.UploadFile(UploadFileRequest{
			FileName: "test." + ft,
			FileData: []byte("data"),
			FileType: ft,
		})
		if err == nil {
			t.Errorf("expected error for file type %q, got nil", ft)
		}
	}
}

func TestUploadFile_SupportedTypesAccepted(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	dm := newTestManager(t, database, &mockEmbeddingService{})

	// These will fail at parse stage (invalid data), but should NOT fail at type validation
	supported := []string{"pdf", "word", "excel", "ppt", "PDF", "Word", "EXCEL", "PPT"}
	for _, ft := range supported {
		doc, err := dm.UploadFile(UploadFileRequest{
			FileName: "test." + ft,
			FileData: []byte("not-real-file-data"),
			FileType: ft,
		})
		// Should not get "不支持的文件格式" error
		if err != nil && err.Error() == "不支持的文件格式" {
			t.Errorf("file type %q should be supported but was rejected", ft)
		}
		// The doc should exist (even if processing failed due to invalid data)
		if err == nil && doc == nil {
			t.Errorf("expected non-nil doc for type %q", ft)
		}
	}
}

func TestUploadFile_FailedParseRecordsStatus(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	dm := newTestManager(t, database, &mockEmbeddingService{})

	doc, err := dm.UploadFile(UploadFileRequest{
		FileName: "bad.pdf",
		FileData: []byte("not-a-pdf"),
		FileType: "pdf",
	})
	if err != nil {
		t.Fatalf("UploadFile should not return error for parse failure, got: %v", err)
	}
	if doc.Status != "failed" {
		t.Fatalf("expected status 'failed', got %q", doc.Status)
	}
	if doc.Error == "" {
		t.Fatal("expected non-empty error message")
	}

	// Verify the DB record also shows failed
	docs, err := dm.ListDocuments("")
	if err != nil {
		t.Fatalf("ListDocuments error: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 document, got %d", len(docs))
	}
	if docs[0].Status != "failed" {
		t.Fatalf("expected DB status 'failed', got %q", docs[0].Status)
	}
}

func TestUploadFile_EmbeddingError(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	es := &mockEmbeddingService{
		embedBatchFunc: func(texts []string) ([][]float64, error) {
			return nil, fmt.Errorf("embedding API down")
		},
	}
	dm := newTestManager(t, database, es)

	// We need valid file data that the parser can handle. Since we can't easily
	// create real PDF data in tests, we'll test the embedding error path by
	// injecting a custom processFile. Instead, let's test via UploadURL with a mock server.
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Some document content for testing purposes"))
	}))
	defer ts.Close()

	doc, err := dm.UploadURL(UploadURLRequest{URL: ts.URL})
	if err != nil {
		t.Fatalf("UploadURL should not return error, got: %v", err)
	}
	if doc.Status != "failed" {
		t.Fatalf("expected status 'failed', got %q", doc.Status)
	}
	if doc.Error == "" {
		t.Fatal("expected non-empty error for embedding failure")
	}
}

func TestUploadURL_Success(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("This is test content from a URL for the helpdesk knowledge base."))
	}))
	defer ts.Close()

	dm := newTestManager(t, database, &mockEmbeddingService{})

	doc, err := dm.UploadURL(UploadURLRequest{URL: ts.URL})
	if err != nil {
		t.Fatalf("UploadURL error: %v", err)
	}
	if doc.Status != "success" {
		t.Fatalf("expected status 'success', got %q (error: %s)", doc.Status, doc.Error)
	}
	if doc.Type != "url" {
		t.Fatalf("expected type 'url', got %q", doc.Type)
	}
	if doc.Name != ts.URL {
		t.Fatalf("expected name %q, got %q", ts.URL, doc.Name)
	}
}

func TestUploadURL_EmptyURL(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	dm := newTestManager(t, database, &mockEmbeddingService{})

	_, err := dm.UploadURL(UploadURLRequest{URL: ""})
	if err == nil {
		t.Fatal("expected error for empty URL")
	}
}

func TestUploadURL_EmptyContent(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("   "))
	}))
	defer ts.Close()

	dm := newTestManager(t, database, &mockEmbeddingService{})

	doc, err := dm.UploadURL(UploadURLRequest{URL: ts.URL})
	if err != nil {
		t.Fatalf("UploadURL should not return error, got: %v", err)
	}
	if doc.Status != "failed" {
		t.Fatalf("expected status 'failed' for empty content, got %q", doc.Status)
	}
}

func TestUploadURL_ServerError(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	dm := newTestManager(t, database, &mockEmbeddingService{})

	doc, err := dm.UploadURL(UploadURLRequest{URL: ts.URL})
	if err != nil {
		t.Fatalf("UploadURL should not return error, got: %v", err)
	}
	if doc.Status != "failed" {
		t.Fatalf("expected status 'failed', got %q", doc.Status)
	}
}

func TestDeleteDocument(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	dm := newTestManager(t, database, &mockEmbeddingService{})

	// Upload a URL doc first
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("Content to be deleted later from the knowledge base."))
	}))
	defer ts.Close()

	doc, err := dm.UploadURL(UploadURLRequest{URL: ts.URL})
	if err != nil {
		t.Fatalf("UploadURL error: %v", err)
	}
	if doc.Status != "success" {
		t.Fatalf("expected success, got %q (error: %s)", doc.Status, doc.Error)
	}

	// Verify it's listed
	docs, err := dm.ListDocuments("")
	if err != nil {
		t.Fatalf("ListDocuments error: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 document, got %d", len(docs))
	}

	// Delete it
	if err := dm.DeleteDocument(doc.ID); err != nil {
		t.Fatalf("DeleteDocument error: %v", err)
	}

	// Verify it's gone
	docs, err = dm.ListDocuments("")
	if err != nil {
		t.Fatalf("ListDocuments error: %v", err)
	}
	if len(docs) != 0 {
		t.Fatalf("expected 0 documents after delete, got %d", len(docs))
	}
}

func TestListDocuments_Empty(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	dm := newTestManager(t, database, &mockEmbeddingService{})

	docs, err := dm.ListDocuments("")
	if err != nil {
		t.Fatalf("ListDocuments error: %v", err)
	}
	if docs != nil && len(docs) != 0 {
		t.Fatalf("expected empty list, got %d documents", len(docs))
	}
}

func TestListDocuments_OrderByCreatedAtDesc(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	dm := newTestManager(t, database, &mockEmbeddingService{})

	// Insert documents with different timestamps directly
	for i, name := range []string{"first", "second", "third"} {
		_, err := database.Exec(
			`INSERT INTO documents (id, name, type, status, created_at) VALUES (?, ?, ?, ?, datetime('2024-01-01', ?))`,
			fmt.Sprintf("doc-%d", i), name, "url", "success", fmt.Sprintf("+%d hours", i),
		)
		if err != nil {
			t.Fatalf("insert error: %v", err)
		}
	}

	docs, err := dm.ListDocuments("")
	if err != nil {
		t.Fatalf("ListDocuments error: %v", err)
	}
	if len(docs) != 3 {
		t.Fatalf("expected 3 documents, got %d", len(docs))
	}
	// Should be in descending order: third, second, first
	if docs[0].Name != "third" {
		t.Fatalf("expected first item 'third', got %q", docs[0].Name)
	}
	if docs[1].Name != "second" {
		t.Fatalf("expected second item 'second', got %q", docs[1].Name)
	}
	if docs[2].Name != "first" {
		t.Fatalf("expected third item 'first', got %q", docs[2].Name)
	}
}

func TestListDocuments_ContainsAllFields(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	dm := newTestManager(t, database, &mockEmbeddingService{})

	_, err := database.Exec(
		`INSERT INTO documents (id, name, type, status, error, created_at) VALUES (?, ?, ?, ?, ?, datetime('now'))`,
		"doc-1", "test.pdf", "pdf", "success", "",
	)
	if err != nil {
		t.Fatalf("insert error: %v", err)
	}

	docs, err := dm.ListDocuments("")
	if err != nil {
		t.Fatalf("ListDocuments error: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 document, got %d", len(docs))
	}

	d := docs[0]
	if d.ID != "doc-1" {
		t.Errorf("expected ID 'doc-1', got %q", d.ID)
	}
	if d.Name != "test.pdf" {
		t.Errorf("expected Name 'test.pdf', got %q", d.Name)
	}
	if d.Type != "pdf" {
		t.Errorf("expected Type 'pdf', got %q", d.Type)
	}
	if d.Status != "success" {
		t.Errorf("expected Status 'success', got %q", d.Status)
	}
}

func TestGenerateID_Uniqueness(t *testing.T) {
	ids := make(map[string]bool)
	for i := 0; i < 100; i++ {
		id, err := generateID()
		if err != nil {
			t.Fatalf("generateID error: %v", err)
		}
		if ids[id] {
			t.Fatalf("duplicate ID generated: %s", id)
		}
		ids[id] = true
		if len(id) != 32 {
			t.Fatalf("expected 32-char hex ID, got %d chars: %s", len(id), id)
		}
	}
}


// TestProperty6_ContentProductAssociation verifies that documents created with a product_id
// retain that product_id when listed back, and documents without a product_id have empty string.
//
// **Feature: multi-product-support, Property 6: 内容产品关联一致性**
// **Validates: Requirements 4.1, 4.3, 5.1, 5.2, 9.2**
func TestProperty6_ContentProductAssociation(t *testing.T) {
	counter := 0
	f := func(seed uint8) bool {
		database, cleanup := setupTestDB(t)
		defer cleanup()

		counter++

		// Create a mock HTTP server returning text content
		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("Test document content for property testing of product association."))
		}))
		defer ts.Close()

		dm := newTestManager(t, database, &mockEmbeddingService{})

		productID := fmt.Sprintf("prod-%d", counter)

		// Upload with product_id
		doc1, err := dm.UploadURL(UploadURLRequest{URL: ts.URL + fmt.Sprintf("/doc1-%d", counter), ProductID: productID})
		if err != nil {
			t.Logf("UploadURL with productID failed: %v", err)
			return false
		}
		if doc1.ProductID != productID {
			t.Logf("Returned doc product_id=%q, want %q", doc1.ProductID, productID)
			return false
		}

		// Upload without product_id (public library)
		doc2, err := dm.UploadURL(UploadURLRequest{URL: ts.URL + fmt.Sprintf("/doc2-%d", counter), ProductID: ""})
		if err != nil {
			t.Logf("UploadURL without productID failed: %v", err)
			return false
		}
		if doc2.ProductID != "" {
			t.Logf("Public doc product_id=%q, want empty", doc2.ProductID)
			return false
		}

		// List all documents and verify product_id round-trip
		docs, err := dm.ListDocuments("")
		if err != nil {
			t.Logf("ListDocuments failed: %v", err)
			return false
		}

		found1, found2 := false, false
		for _, d := range docs {
			if d.ID == doc1.ID {
				found1 = true
				if d.ProductID != productID {
					t.Logf("Listed doc1 product_id=%q, want %q", d.ProductID, productID)
					return false
				}
			}
			if d.ID == doc2.ID {
				found2 = true
				if d.ProductID != "" {
					t.Logf("Listed doc2 product_id=%q, want empty", d.ProductID)
					return false
				}
			}
		}
		if !found1 || !found2 {
			t.Logf("Not all documents found in list: found1=%v found2=%v", found1, found2)
			return false
		}

		return true
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 100}); err != nil {
		t.Error(err)
	}
}

// TestProperty7_ChunkProductIDInheritance verifies that all vector chunks of a document
// inherit the same product_id as the document itself.
//
// **Feature: multi-product-support, Property 7: 向量分块产品 ID 继承**
// **Validates: Requirements 9.3**
func TestProperty7_ChunkProductIDInheritance(t *testing.T) {
	counter := 0
	f := func(seed uint8) bool {
		database, cleanup := setupTestDB(t)
		defer cleanup()

		counter++

		ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte("Longer document content that should produce chunks for testing product ID inheritance in the vector store."))
		}))
		defer ts.Close()

		dm := newTestManager(t, database, &mockEmbeddingService{})

		productID := fmt.Sprintf("prod-%d", counter)

		doc, err := dm.UploadURL(UploadURLRequest{URL: ts.URL + fmt.Sprintf("/doc-%d", counter), ProductID: productID})
		if err != nil {
			t.Logf("UploadURL failed: %v", err)
			return false
		}
		if doc.Status != "success" {
			t.Logf("Doc status=%q, error=%q", doc.Status, doc.Error)
			return false
		}

		// Query chunks for this document and verify all have the same product_id
		rows, err := database.Query("SELECT product_id FROM chunks WHERE document_id = ?", doc.ID)
		if err != nil {
			t.Logf("Query chunks failed: %v", err)
			return false
		}
		defer rows.Close()

		chunkCount := 0
		for rows.Next() {
			var chunkProductID string
			if err := rows.Scan(&chunkProductID); err != nil {
				t.Logf("Scan chunk failed: %v", err)
				return false
			}
			chunkCount++
			if chunkProductID != productID {
				t.Logf("Chunk product_id=%q, want %q (doc %s)", chunkProductID, productID, doc.ID)
				return false
			}
		}

		if chunkCount == 0 {
			t.Logf("No chunks found for document %s", doc.ID)
			return false
		}

		return true
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 100}); err != nil {
		t.Error(err)
	}
}


// TestProperty9_DocumentListProductFiltering verifies that ListDocuments with a product_id
// returns only documents matching that product_id or the public library (empty product_id).
//
// **Feature: multi-product-support, Property 9: 文档列表产品过滤**
// **Validates: Requirements 6.1, 10.5**
func TestProperty9_DocumentListProductFiltering(t *testing.T) {
	counter := 0
	f := func(seed uint8) bool {
		database, cleanup := setupTestDB(t)
		defer cleanup()

		counter++
		dm := newTestManager(t, database, &mockEmbeddingService{})

		productA := fmt.Sprintf("prodA-%d", counter)
		productB := fmt.Sprintf("prodB-%d", counter)

		// Insert documents directly for speed (bypass parsing/embedding)
		docs := []struct {
			id        string
			name      string
			productID string
		}{
			{fmt.Sprintf("docA1-%d", counter), "a1.pdf", productA},
			{fmt.Sprintf("docA2-%d", counter), "a2.pdf", productA},
			{fmt.Sprintf("docB1-%d", counter), "b1.pdf", productB},
			{fmt.Sprintf("docPub-%d", counter), "pub.pdf", ""},
		}

		for _, d := range docs {
			_, err := database.Exec(
				"INSERT INTO documents (id, name, type, status, product_id, created_at) VALUES (?, ?, 'pdf', 'success', ?, datetime('now'))",
				d.id, d.name, d.productID,
			)
			if err != nil {
				t.Logf("Insert document failed: %v", err)
				return false
			}
		}

		// Filter by productA - should get productA docs + public
		filtered, err := dm.ListDocuments(productA)
		if err != nil {
			t.Logf("ListDocuments(productA) failed: %v", err)
			return false
		}
		if len(filtered) != 3 {
			t.Logf("ListDocuments(productA): expected 3 (2 productA + 1 public), got %d", len(filtered))
			return false
		}
		for _, d := range filtered {
			if d.ProductID != productA && d.ProductID != "" {
				t.Logf("ListDocuments(productA): doc %s has product_id=%q, expected %q or empty", d.ID, d.ProductID, productA)
				return false
			}
		}

		// Filter by productB - should get productB docs + public
		filtered, err = dm.ListDocuments(productB)
		if err != nil {
			t.Logf("ListDocuments(productB) failed: %v", err)
			return false
		}
		if len(filtered) != 2 {
			t.Logf("ListDocuments(productB): expected 2 (1 productB + 1 public), got %d", len(filtered))
			return false
		}
		for _, d := range filtered {
			if d.ProductID != productB && d.ProductID != "" {
				t.Logf("ListDocuments(productB): doc %s has product_id=%q, expected %q or empty", d.ID, d.ProductID, productB)
				return false
			}
		}

		// No filter - should get all 4
		all, err := dm.ListDocuments("")
		if err != nil {
			t.Logf("ListDocuments('') failed: %v", err)
			return false
		}
		if len(all) != 4 {
			t.Logf("ListDocuments(''): expected 4, got %d", len(all))
			return false
		}

		return true
	}

	if err := quick.Check(f, &quick.Config{MaxCount: 100}); err != nil {
		t.Error(err)
	}
}
