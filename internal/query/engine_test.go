package query

import (
	"database/sql"
	"fmt"
	"strings"
	"testing"

	"helpdesk/internal/config"
	"helpdesk/internal/vectorstore"

	_ "github.com/mattn/go-sqlite3"
	"pgregory.net/rapid"
)

// --- Mock implementations ---

type mockEmbeddingService struct {
	embedFn func(text string) ([]float64, error)
}

func (m *mockEmbeddingService) Embed(text string) ([]float64, error) {
	return m.embedFn(text)
}

func (m *mockEmbeddingService) EmbedBatch(texts []string) ([][]float64, error) {
	var results [][]float64
	for _, t := range texts {
		v, err := m.embedFn(t)
		if err != nil {
			return nil, err
		}
		results = append(results, v)
	}
	return results, nil
}

func (m *mockEmbeddingService) EmbedImageURL(imageURL string) ([]float64, error) {
	return []float64{0.1, 0.2, 0.3}, nil
}

type mockVectorStore struct {
	searchFn func(queryVector []float64, topK int, threshold float64, productID string) ([]vectorstore.SearchResult, error)
}

func (m *mockVectorStore) Store(docID string, chunks []vectorstore.VectorChunk) error {
	return nil
}

func (m *mockVectorStore) Search(queryVector []float64, topK int, threshold float64, productID string) ([]vectorstore.SearchResult, error) {
	return m.searchFn(queryVector, topK, threshold, productID)
}

func (m *mockVectorStore) DeleteByDocID(docID string) error {
	return nil
}

func (m *mockVectorStore) TextSearch(query string, topK int, threshold float64, productID string) ([]vectorstore.SearchResult, error) {
	return nil, nil
}

type mockLLMService struct {
	generateFn func(prompt string, context []string, question string) (string, error)
}

func (m *mockLLMService) Generate(prompt string, context []string, question string) (string, error) {
	return m.generateFn(prompt, context, question)
}

func (m *mockLLMService) GenerateWithImage(prompt string, context []string, question string, imageDataURL string) (string, error) {
	return m.generateFn(prompt, context, question)
}

// --- Test helpers ---

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatalf("failed to open test db: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS pending_questions (
		id TEXT PRIMARY KEY,
		question TEXT NOT NULL,
		user_id TEXT NOT NULL,
		status TEXT NOT NULL,
		answer TEXT,
		llm_answer TEXT,
		image_data TEXT,
		product_id TEXT DEFAULT '',
		created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
		answered_at DATETIME
	)`)
	if err != nil {
		t.Fatalf("failed to create pending_questions table: %v", err)
	}
	_, err = db.Exec(`CREATE TABLE IF NOT EXISTS video_segments (
		id           TEXT PRIMARY KEY,
		document_id  TEXT NOT NULL,
		segment_type TEXT NOT NULL,
		start_time   REAL NOT NULL,
		end_time     REAL NOT NULL,
		content      TEXT NOT NULL,
		chunk_id     TEXT NOT NULL
	)`)
	if err != nil {
		t.Fatalf("failed to create video_segments table: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func defaultConfig() *config.Config {
	return &config.Config{
		Vector: config.VectorConfig{
			TopK:      5,
			Threshold: 0.7,
		},
	}
}

// --- Tests ---

func TestQuery_SuccessfulAnswer(t *testing.T) {
	db := setupTestDB(t)

	es := &mockEmbeddingService{
		embedFn: func(text string) ([]float64, error) {
			return []float64{0.1, 0.2, 0.3}, nil
		},
	}
	vs := &mockVectorStore{
		searchFn: func(queryVector []float64, topK int, threshold float64, productID string) ([]vectorstore.SearchResult, error) {
			return []vectorstore.SearchResult{
				{ChunkText: "Go is a statically typed language.", ChunkIndex: 0, DocumentID: "doc1", DocumentName: "go-intro.pdf", Score: 0.95},
				{ChunkText: "Go supports concurrency with goroutines.", ChunkIndex: 1, DocumentID: "doc1", DocumentName: "go-intro.pdf", Score: 0.85},
			}, nil
		},
	}
	ls := &mockLLMService{
		generateFn: func(prompt string, context []string, question string) (string, error) {
			return "Go is a statically typed language that supports concurrency.", nil
		},
	}

	qe := NewQueryEngine(es, vs, ls, db, defaultConfig())
	resp, err := qe.Query(QueryRequest{Question: "What is Go?", UserID: "user1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.IsPending {
		t.Error("expected IsPending=false")
	}
	if resp.Answer == "" {
		t.Error("expected non-empty answer")
	}
	if len(resp.Sources) != 2 {
		t.Errorf("expected 2 sources, got %d", len(resp.Sources))
	}
	if resp.Sources[0].DocumentName != "go-intro.pdf" {
		t.Errorf("expected document name 'go-intro.pdf', got %q", resp.Sources[0].DocumentName)
	}
	if resp.Sources[0].ChunkIndex != 0 {
		t.Errorf("expected chunk index 0, got %d", resp.Sources[0].ChunkIndex)
	}
}

func TestQuery_PendingWhenNoResults(t *testing.T) {
	db := setupTestDB(t)

	es := &mockEmbeddingService{
		embedFn: func(text string) ([]float64, error) {
			return []float64{0.1, 0.2, 0.3}, nil
		},
	}
	vs := &mockVectorStore{
		searchFn: func(queryVector []float64, topK int, threshold float64, productID string) ([]vectorstore.SearchResult, error) {
			return []vectorstore.SearchResult{}, nil
		},
	}
	intentCalled := false
	ls := &mockLLMService{
		generateFn: func(prompt string, context []string, question string) (string, error) {
			if !intentCalled {
				// First call is intent classification — allow it
				intentCalled = true
				return `{"intent":"product"}`, nil
			}
			// Second call is translation of the pending message — return the original Chinese
			return "该问题已转交人工处理，请稍后查看回复", nil
		},
	}

	qe := NewQueryEngine(es, vs, ls, db, defaultConfig())
	resp, err := qe.Query(QueryRequest{Question: "Unknown topic?", UserID: "user1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !resp.IsPending {
		t.Error("expected IsPending=true")
	}
	if resp.Message != "该问题已转交人工处理，请稍后查看回复" {
		t.Errorf("unexpected message: %q", resp.Message)
	}
	if resp.Answer != "" {
		t.Error("expected empty answer for pending response")
	}

	// Verify pending question was created in DB
	var count int
	err = db.QueryRow(`SELECT COUNT(*) FROM pending_questions WHERE user_id = ? AND status = 'pending'`, "user1").Scan(&count)
	if err != nil {
		t.Fatalf("failed to query pending_questions: %v", err)
	}
	if count != 1 {
		t.Errorf("expected 1 pending question, got %d", count)
	}
}

func TestQuery_SnippetTruncation(t *testing.T) {
	db := setupTestDB(t)

	longText := strings.Repeat("a", 200)
	es := &mockEmbeddingService{
		embedFn: func(text string) ([]float64, error) {
			return []float64{0.5}, nil
		},
	}
	vs := &mockVectorStore{
		searchFn: func(queryVector []float64, topK int, threshold float64, productID string) ([]vectorstore.SearchResult, error) {
			return []vectorstore.SearchResult{
				{ChunkText: longText, ChunkIndex: 0, DocumentID: "doc1", DocumentName: "doc.pdf", Score: 0.9},
			}, nil
		},
	}
	ls := &mockLLMService{
		generateFn: func(prompt string, context []string, question string) (string, error) {
			return "answer", nil
		},
	}

	qe := NewQueryEngine(es, vs, ls, db, defaultConfig())
	resp, err := qe.Query(QueryRequest{Question: "test", UserID: "user1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resp.Sources[0].Snippet) != 100 {
		t.Errorf("expected snippet length 100, got %d", len(resp.Sources[0].Snippet))
	}
}

func TestQuery_SnippetShortText(t *testing.T) {
	db := setupTestDB(t)

	shortText := "short"
	es := &mockEmbeddingService{
		embedFn: func(text string) ([]float64, error) {
			return []float64{0.5}, nil
		},
	}
	vs := &mockVectorStore{
		searchFn: func(queryVector []float64, topK int, threshold float64, productID string) ([]vectorstore.SearchResult, error) {
			return []vectorstore.SearchResult{
				{ChunkText: shortText, ChunkIndex: 0, DocumentID: "doc1", DocumentName: "doc.pdf", Score: 0.9},
			}, nil
		},
	}
	ls := &mockLLMService{
		generateFn: func(prompt string, context []string, question string) (string, error) {
			return "answer", nil
		},
	}

	qe := NewQueryEngine(es, vs, ls, db, defaultConfig())
	resp, err := qe.Query(QueryRequest{Question: "test", UserID: "user1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if resp.Sources[0].Snippet != shortText {
		t.Errorf("expected snippet %q, got %q", shortText, resp.Sources[0].Snippet)
	}
}

func TestQuery_EmbeddingError(t *testing.T) {
	db := setupTestDB(t)

	es := &mockEmbeddingService{
		embedFn: func(text string) ([]float64, error) {
			return nil, fmt.Errorf("embedding API down")
		},
	}
	vs := &mockVectorStore{
		searchFn: func(queryVector []float64, topK int, threshold float64, productID string) ([]vectorstore.SearchResult, error) {
			return nil, nil
		},
	}
	ls := &mockLLMService{
		generateFn: func(prompt string, context []string, question string) (string, error) {
			return "", nil
		},
	}

	qe := NewQueryEngine(es, vs, ls, db, defaultConfig())
	_, err := qe.Query(QueryRequest{Question: "test", UserID: "user1"})
	if err == nil {
		t.Fatal("expected error from embedding failure")
	}
	if !strings.Contains(err.Error(), "embed question") {
		t.Errorf("expected embedding error, got: %v", err)
	}
}

func TestQuery_VectorSearchError(t *testing.T) {
	db := setupTestDB(t)

	es := &mockEmbeddingService{
		embedFn: func(text string) ([]float64, error) {
			return []float64{0.1}, nil
		},
	}
	vs := &mockVectorStore{
		searchFn: func(queryVector []float64, topK int, threshold float64, productID string) ([]vectorstore.SearchResult, error) {
			return nil, fmt.Errorf("db error")
		},
	}
	ls := &mockLLMService{
		generateFn: func(prompt string, context []string, question string) (string, error) {
			return "", nil
		},
	}

	qe := NewQueryEngine(es, vs, ls, db, defaultConfig())
	_, err := qe.Query(QueryRequest{Question: "test", UserID: "user1"})
	if err == nil {
		t.Fatal("expected error from vector search failure")
	}
	if !strings.Contains(err.Error(), "search vector store") {
		t.Errorf("expected vector search error, got: %v", err)
	}
}

func TestQuery_LLMError(t *testing.T) {
	db := setupTestDB(t)

	es := &mockEmbeddingService{
		embedFn: func(text string) ([]float64, error) {
			return []float64{0.1}, nil
		},
	}
	vs := &mockVectorStore{
		searchFn: func(queryVector []float64, topK int, threshold float64, productID string) ([]vectorstore.SearchResult, error) {
			return []vectorstore.SearchResult{
				{ChunkText: "some text", ChunkIndex: 0, DocumentID: "doc1", DocumentName: "doc.pdf", Score: 0.9},
			}, nil
		},
	}
	ls := &mockLLMService{
		generateFn: func(prompt string, context []string, question string) (string, error) {
			return "", fmt.Errorf("LLM unavailable")
		},
	}

	qe := NewQueryEngine(es, vs, ls, db, defaultConfig())
	_, err := qe.Query(QueryRequest{Question: "test", UserID: "user1"})
	if err == nil {
		t.Fatal("expected error from LLM failure")
	}
	if !strings.Contains(err.Error(), "generate answer") {
		t.Errorf("expected LLM error, got: %v", err)
	}
}

func TestQuery_UsesConfigTopKAndThreshold(t *testing.T) {
	db := setupTestDB(t)

	var capturedTopK int
	var capturedThreshold float64
	firstCall := true

	es := &mockEmbeddingService{
		embedFn: func(text string) ([]float64, error) {
			return []float64{0.1}, nil
		},
	}
	vs := &mockVectorStore{
		searchFn: func(queryVector []float64, topK int, threshold float64, productID string) ([]vectorstore.SearchResult, error) {
			if firstCall {
				capturedTopK = topK
				capturedThreshold = threshold
				firstCall = false
			}
			return []vectorstore.SearchResult{}, nil
		},
	}
	ls := &mockLLMService{
		generateFn: func(prompt string, context []string, question string) (string, error) {
			return `{"intent":"product"}`, nil
		},
	}

	cfg := &config.Config{
		Vector: config.VectorConfig{
			TopK:      10,
			Threshold: 0.85,
		},
	}

	qe := NewQueryEngine(es, vs, ls, db, cfg)
	_, _ = qe.Query(QueryRequest{Question: "test", UserID: "user1"})

	if capturedTopK != 10 {
		t.Errorf("expected topK=10, got %d", capturedTopK)
	}
	if capturedThreshold != 0.85 {
		t.Errorf("expected threshold=0.85, got %f", capturedThreshold)
	}
}

func TestQuery_ContextPassedToLLM(t *testing.T) {
	db := setupTestDB(t)

	var capturedContext []string
	var capturedQuestion string

	es := &mockEmbeddingService{
		embedFn: func(text string) ([]float64, error) {
			return []float64{0.1}, nil
		},
	}
	vs := &mockVectorStore{
		searchFn: func(queryVector []float64, topK int, threshold float64, productID string) ([]vectorstore.SearchResult, error) {
			return []vectorstore.SearchResult{
				{ChunkText: "chunk1 text", ChunkIndex: 0, DocumentID: "doc1", DocumentName: "doc.pdf", Score: 0.9},
				{ChunkText: "chunk2 text", ChunkIndex: 1, DocumentID: "doc1", DocumentName: "doc.pdf", Score: 0.8},
			}, nil
		},
	}
	ls := &mockLLMService{
		generateFn: func(prompt string, context []string, question string) (string, error) {
			capturedContext = context
			capturedQuestion = question
			return "answer", nil
		},
	}

	qe := NewQueryEngine(es, vs, ls, db, defaultConfig())
	_, err := qe.Query(QueryRequest{Question: "my question", UserID: "user1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(capturedContext) != 2 {
		t.Fatalf("expected 2 context chunks, got %d", len(capturedContext))
	}
	if capturedContext[0] != "chunk1 text" {
		t.Errorf("expected first context 'chunk1 text', got %q", capturedContext[0])
	}
	if capturedContext[1] != "chunk2 text" {
		t.Errorf("expected second context 'chunk2 text', got %q", capturedContext[1])
	}
	if capturedQuestion != "my question" {
		t.Errorf("expected question 'my question', got %q", capturedQuestion)
	}
}

func TestEnrichVideoTimeInfo(t *testing.T) {
	db := setupTestDB(t)

	// Insert video_segments records for two chunks
	_, err := db.Exec(
		`INSERT INTO video_segments (id, document_id, segment_type, start_time, end_time, content, chunk_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"seg1", "doc1", "transcript", 10.5, 25.3, "some transcript text", "doc1-0",
	)
	if err != nil {
		t.Fatalf("failed to insert video_segment: %v", err)
	}
	_, err = db.Exec(
		`INSERT INTO video_segments (id, document_id, segment_type, start_time, end_time, content, chunk_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"seg2", "doc1", "keyframe", 30.0, 30.0, "/tmp/frame.jpg", "doc1-1",
	)
	if err != nil {
		t.Fatalf("failed to insert video_segment: %v", err)
	}

	qe := NewQueryEngine(nil, nil, nil, db, defaultConfig())

	t.Run("enriches results with matching video_segments", func(t *testing.T) {
		results := []vectorstore.SearchResult{
			{DocumentID: "doc1", ChunkIndex: 0, ChunkText: "transcript chunk", Score: 0.9},
			{DocumentID: "doc1", ChunkIndex: 1, ChunkText: "keyframe chunk", Score: 0.8},
		}
		enriched := qe.enrichVideoTimeInfo(results)

		if enriched[0].StartTime != 10.5 {
			t.Errorf("expected StartTime=10.5, got %f", enriched[0].StartTime)
		}
		if enriched[0].EndTime != 25.3 {
			t.Errorf("expected EndTime=25.3, got %f", enriched[0].EndTime)
		}
		if enriched[1].StartTime != 30.0 {
			t.Errorf("expected StartTime=30.0, got %f", enriched[1].StartTime)
		}
		if enriched[1].EndTime != 30.0 {
			t.Errorf("expected EndTime=30.0, got %f", enriched[1].EndTime)
		}
	})

	t.Run("leaves non-video results unchanged", func(t *testing.T) {
		results := []vectorstore.SearchResult{
			{DocumentID: "doc2", ChunkIndex: 0, ChunkText: "regular doc chunk", Score: 0.9},
		}
		enriched := qe.enrichVideoTimeInfo(results)

		if enriched[0].StartTime != 0 {
			t.Errorf("expected StartTime=0, got %f", enriched[0].StartTime)
		}
		if enriched[0].EndTime != 0 {
			t.Errorf("expected EndTime=0, got %f", enriched[0].EndTime)
		}
	})

	t.Run("handles empty results", func(t *testing.T) {
		results := []vectorstore.SearchResult{}
		enriched := qe.enrichVideoTimeInfo(results)
		if len(enriched) != 0 {
			t.Errorf("expected empty results, got %d", len(enriched))
		}
	})

	t.Run("handles nil db gracefully", func(t *testing.T) {
		qeNilDB := NewQueryEngine(nil, nil, nil, nil, defaultConfig())
		results := []vectorstore.SearchResult{
			{DocumentID: "doc1", ChunkIndex: 0, ChunkText: "chunk", Score: 0.9},
		}
		enriched := qeNilDB.enrichVideoTimeInfo(results)
		if enriched[0].StartTime != 0 {
			t.Errorf("expected StartTime=0 with nil db, got %f", enriched[0].StartTime)
		}
	})
}

func TestQuery_VideoTimeInfoPropagatedToSourceRef(t *testing.T) {
	db := setupTestDB(t)

	// Insert a video_segments record
	_, err := db.Exec(
		`INSERT INTO video_segments (id, document_id, segment_type, start_time, end_time, content, chunk_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		"seg1", "video-doc", "transcript", 5.0, 15.0, "video transcript", "video-doc-0",
	)
	if err != nil {
		t.Fatalf("failed to insert video_segment: %v", err)
	}

	es := &mockEmbeddingService{
		embedFn: func(text string) ([]float64, error) {
			return []float64{0.1, 0.2, 0.3}, nil
		},
	}
	vs := &mockVectorStore{
		searchFn: func(queryVector []float64, topK int, threshold float64, productID string) ([]vectorstore.SearchResult, error) {
			return []vectorstore.SearchResult{
				{ChunkText: "video transcript text", ChunkIndex: 0, DocumentID: "video-doc", DocumentName: "intro.mp4", Score: 0.95},
			}, nil
		},
	}
	ls := &mockLLMService{
		generateFn: func(prompt string, context []string, question string) (string, error) {
			return "Here is the answer from the video.", nil
		},
	}

	qe := NewQueryEngine(es, vs, ls, db, defaultConfig())
	resp, err := qe.Query(QueryRequest{Question: "What does the video say?", UserID: "user1"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(resp.Sources) == 0 {
		t.Fatal("expected at least 1 source")
	}
	if resp.Sources[0].StartTime != 5.0 {
		t.Errorf("expected SourceRef.StartTime=5.0, got %f", resp.Sources[0].StartTime)
	}
	if resp.Sources[0].EndTime != 15.0 {
		t.Errorf("expected SourceRef.EndTime=15.0, got %f", resp.Sources[0].EndTime)
	}
}

// TestProperty7_VideoSearchTimeInfo 验证视频检索结果包含时间定位。
// 对于任意检索结果，若该结果来源于视频文档（通过 video_segments 表关联），
// 则结果中的 StartTime 和 EndTime 字段应包含有效的时间值（非零）。
//
// **Feature: video-retrieval, Property 7: 视频检索结果包含时间定位**
// **Validates: Requirements 5.1, 5.2**
func TestProperty7_VideoSearchTimeInfo(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		segCount := rapid.IntRange(1, 10).Draw(rt, "segment_count")
		docID := fmt.Sprintf("video-doc-%d", rapid.IntRange(1, 999999).Draw(rt, "doc_id"))

		db := setupTestDB(t)

		// 插入随机数量的 video_segments 记录
		for i := 0; i < segCount; i++ {
			startTime := rapid.Float64Range(0, 3600).Draw(rt, fmt.Sprintf("start_%d", i))
			duration := rapid.Float64Range(0.1, 60).Draw(rt, fmt.Sprintf("dur_%d", i))
			segType := "transcript"
			if i%2 == 0 {
				segType = "keyframe"
			}
			chunkID := fmt.Sprintf("%s-%d", docID, i)
			_, err := db.Exec(
				`INSERT INTO video_segments (id, document_id, segment_type, start_time, end_time, content, chunk_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				fmt.Sprintf("seg-%s-%d", docID, i), docID, segType, startTime, startTime+duration, "content", chunkID,
			)
			if err != nil {
				rt.Fatalf("insert video_segment: %v", err)
			}
		}

		qe := NewQueryEngine(nil, nil, nil, db, defaultConfig())

		// 构造与 video_segments 匹配的搜索结果
		results := make([]vectorstore.SearchResult, segCount)
		for i := 0; i < segCount; i++ {
			results[i] = vectorstore.SearchResult{
				DocumentID:   docID,
				ChunkIndex:   i,
				ChunkText:    "video chunk",
				DocumentName: "test.mp4",
				Score:        0.9,
			}
		}

		enriched := qe.enrichVideoTimeInfo(results)

		// 验证每个结果都有有效的时间信息
		for i, r := range enriched {
			if r.StartTime == 0 && r.EndTime == 0 {
				rt.Errorf("result[%d]: expected non-zero time info for video chunk", i)
			}
			if r.EndTime < r.StartTime {
				rt.Errorf("result[%d]: EndTime (%f) < StartTime (%f)", i, r.EndTime, r.StartTime)
			}
		}

		// 验证非视频文档的结果不受影响
		nonVideoResults := []vectorstore.SearchResult{
			{DocumentID: "non-video-doc", ChunkIndex: 0, ChunkText: "text", Score: 0.9},
		}
		nonVideoEnriched := qe.enrichVideoTimeInfo(nonVideoResults)
		if nonVideoEnriched[0].StartTime != 0 || nonVideoEnriched[0].EndTime != 0 {
			rt.Errorf("non-video result should have zero time info, got start=%f end=%f",
				nonVideoEnriched[0].StartTime, nonVideoEnriched[0].EndTime)
		}
	})
}
