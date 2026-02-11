package pending

import (
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"helpdesk/internal/chunker"
	"helpdesk/internal/db"
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
}

// mockLLMService implements llm.LLMService for testing.
type mockLLMService struct {
	generateFunc func(prompt string, context []string, question string) (string, error)
}

func (m *mockLLMService) Generate(prompt string, context []string, question string) (string, error) {
	if m.generateFunc != nil {
		return m.generateFunc(prompt, context, question)
	}
	return "LLM generated summary answer", nil
}

// setupTestDB creates a temporary SQLite database for testing.
func setupTestDB(t *testing.T) (*sql.DB, func()) {
	t.Helper()
	tmpFile, err := os.CreateTemp("", "test-pending-manager-*.db")
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

// newTestManager creates a PendingQuestionManager wired with real DB, real chunker,
// real vector store, and mock embedding/LLM services.
func newTestManager(t *testing.T, database *sql.DB, es *mockEmbeddingService, ls *mockLLMService) *PendingQuestionManager {
	t.Helper()
	c := chunker.NewTextChunker()
	vs := vectorstore.NewSQLiteVectorStore(database)
	return NewPendingQuestionManager(database, c, es, vs, ls)
}

func TestCreatePending_Success(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	pm := newTestManager(t, database, &mockEmbeddingService{}, &mockLLMService{})

	pq, err := pm.CreatePending("How do I reset my password?", "user-123", "")
	if err != nil {
		t.Fatalf("CreatePending failed: %v", err)
	}

	if pq.ID == "" {
		t.Error("expected non-empty ID")
	}
	if pq.Question != "How do I reset my password?" {
		t.Errorf("expected question 'How do I reset my password?', got %q", pq.Question)
	}
	if pq.UserID != "user-123" {
		t.Errorf("expected user_id 'user-123', got %q", pq.UserID)
	}
	if pq.Status != "pending" {
		t.Errorf("expected status 'pending', got %q", pq.Status)
	}

	// Verify it's in the database
	var count int
	database.QueryRow(`SELECT COUNT(*) FROM pending_questions WHERE id = ?`, pq.ID).Scan(&count)
	if count != 1 {
		t.Errorf("expected 1 row in DB, got %d", count)
	}
}

func TestCreatePending_MultipleQuestions(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	pm := newTestManager(t, database, &mockEmbeddingService{}, &mockLLMService{})

	pq1, err := pm.CreatePending("Question 1", "user-1", "")
	if err != nil {
		t.Fatalf("CreatePending 1 failed: %v", err)
	}
	pq2, err := pm.CreatePending("Question 2", "user-2", "")
	if err != nil {
		t.Fatalf("CreatePending 2 failed: %v", err)
	}

	if pq1.ID == pq2.ID {
		t.Error("expected unique IDs for different questions")
	}
}

func TestListPending_Empty(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	pm := newTestManager(t, database, &mockEmbeddingService{}, &mockLLMService{})

	questions, err := pm.ListPending("")
	if err != nil {
		t.Fatalf("ListPending failed: %v", err)
	}
	if len(questions) != 0 {
		t.Errorf("expected 0 questions, got %d", len(questions))
	}
}

func TestListPending_AllStatuses(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	pm := newTestManager(t, database, &mockEmbeddingService{}, &mockLLMService{})

	pm.CreatePending("Q1", "user-1", "")
	pm.CreatePending("Q2", "user-2", "")

	questions, err := pm.ListPending("")
	if err != nil {
		t.Fatalf("ListPending failed: %v", err)
	}
	if len(questions) != 2 {
		t.Errorf("expected 2 questions, got %d", len(questions))
	}
}

func TestListPending_FilterByStatus(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	pm := newTestManager(t, database, &mockEmbeddingService{}, &mockLLMService{})

	pq, _ := pm.CreatePending("Q1", "user-1", "")
	pm.CreatePending("Q2", "user-2", "")

	// Answer Q1 to change its status
	pm.AnswerQuestion(AdminAnswerRequest{
		QuestionID: pq.ID,
		Text:       "Here is the answer",
	})

	pending, err := pm.ListPending("pending")
	if err != nil {
		t.Fatalf("ListPending(pending) failed: %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("expected 1 pending question, got %d", len(pending))
	}

	answered, err := pm.ListPending("answered")
	if err != nil {
		t.Fatalf("ListPending(answered) failed: %v", err)
	}
	if len(answered) != 1 {
		t.Errorf("expected 1 answered question, got %d", len(answered))
	}
}

func TestListPending_OrderByCreatedAtDesc(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	pm := newTestManager(t, database, &mockEmbeddingService{}, &mockLLMService{})

	// Insert with explicit timestamps to ensure ordering
	now := time.Now().UTC()
	database.Exec(
		`INSERT INTO pending_questions (id, question, user_id, status, created_at) VALUES (?, ?, ?, ?, ?)`,
		"id-old", "Old question", "user-1", "pending", now.Add(-2*time.Hour),
	)
	database.Exec(
		`INSERT INTO pending_questions (id, question, user_id, status, created_at) VALUES (?, ?, ?, ?, ?)`,
		"id-new", "New question", "user-2", "pending", now,
	)
	database.Exec(
		`INSERT INTO pending_questions (id, question, user_id, status, created_at) VALUES (?, ?, ?, ?, ?)`,
		"id-mid", "Mid question", "user-3", "pending", now.Add(-1*time.Hour),
	)

	questions, err := pm.ListPending("")
	if err != nil {
		t.Fatalf("ListPending failed: %v", err)
	}
	if len(questions) != 3 {
		t.Fatalf("expected 3 questions, got %d", len(questions))
	}

	// Should be newest first
	if questions[0].ID != "id-new" {
		t.Errorf("expected first question to be 'id-new', got %q", questions[0].ID)
	}
	if questions[1].ID != "id-mid" {
		t.Errorf("expected second question to be 'id-mid', got %q", questions[1].ID)
	}
	if questions[2].ID != "id-old" {
		t.Errorf("expected third question to be 'id-old', got %q", questions[2].ID)
	}
}

func TestAnswerQuestion_Success(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	var llmPrompt, llmQuestion string
	var llmContext []string

	ls := &mockLLMService{
		generateFunc: func(prompt string, context []string, question string) (string, error) {
			llmPrompt = prompt
			llmContext = context
			llmQuestion = question
			return "Summary: Here is how to reset your password.", nil
		},
	}

	pm := newTestManager(t, database, &mockEmbeddingService{}, ls)

	pq, _ := pm.CreatePending("How do I reset my password?", "user-123", "")

	err := pm.AnswerQuestion(AdminAnswerRequest{
		QuestionID: pq.ID,
		Text:       "Go to Settings > Account > Reset Password and follow the instructions.",
	})
	if err != nil {
		t.Fatalf("AnswerQuestion failed: %v", err)
	}

	// Verify LLM was called with the right parameters
	if llmPrompt == "" {
		t.Error("expected LLM to be called with a prompt")
	}
	if len(llmContext) != 1 || llmContext[0] != "Go to Settings > Account > Reset Password and follow the instructions." {
		t.Errorf("unexpected LLM context: %v", llmContext)
	}
	if llmQuestion != "How do I reset my password?" {
		t.Errorf("unexpected LLM question: %q", llmQuestion)
	}

	// Verify DB was updated
	var status, answer, llmAnswer string
	var answeredAt sql.NullTime
	err = database.QueryRow(
		`SELECT status, answer, llm_answer, answered_at FROM pending_questions WHERE id = ?`, pq.ID,
	).Scan(&status, &answer, &llmAnswer, &answeredAt)
	if err != nil {
		t.Fatalf("failed to query updated record: %v", err)
	}
	if status != "answered" {
		t.Errorf("expected status 'answered', got %q", status)
	}
	if answer != "Go to Settings > Account > Reset Password and follow the instructions." {
		t.Errorf("unexpected answer in DB: %q", answer)
	}
	if llmAnswer != "Summary: Here is how to reset your password." {
		t.Errorf("unexpected llm_answer in DB: %q", llmAnswer)
	}
	if !answeredAt.Valid {
		t.Error("expected answered_at to be set")
	}
}

func TestAnswerQuestion_StoresInVectorStore(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	pm := newTestManager(t, database, &mockEmbeddingService{}, &mockLLMService{})

	pq, _ := pm.CreatePending("How to configure email?", "user-456", "")

	err := pm.AnswerQuestion(AdminAnswerRequest{
		QuestionID: pq.ID,
		Text:       "Open the admin panel, go to Email Settings, and enter your SMTP server details.",
	})
	if err != nil {
		t.Fatalf("AnswerQuestion failed: %v", err)
	}

	// Verify chunks were stored in the vector store
	var count int
	docID := "pending-answer-" + pq.ID
	database.QueryRow(`SELECT COUNT(*) FROM chunks WHERE document_id = ?`, docID).Scan(&count)
	if count == 0 {
		t.Error("expected answer chunks to be stored in vector store")
	}
}

func TestAnswerQuestion_NotFound(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	pm := newTestManager(t, database, &mockEmbeddingService{}, &mockLLMService{})

	err := pm.AnswerQuestion(AdminAnswerRequest{
		QuestionID: "nonexistent-id",
		Text:       "Some answer",
	})
	if err == nil {
		t.Fatal("expected error for nonexistent question")
	}
}

func TestAnswerQuestion_AlreadyAnswered(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	pm := newTestManager(t, database, &mockEmbeddingService{}, &mockLLMService{})

	pq, _ := pm.CreatePending("Q1", "user-1", "")
	pm.AnswerQuestion(AdminAnswerRequest{QuestionID: pq.ID, Text: "Answer 1"})

	err := pm.AnswerQuestion(AdminAnswerRequest{QuestionID: pq.ID, Text: "Answer 2"})
	if err == nil {
		t.Fatal("expected error for already answered question")
	}
}

func TestAnswerQuestion_EmbeddingError(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	es := &mockEmbeddingService{
		embedBatchFunc: func(texts []string) ([][]float64, error) {
			return nil, fmt.Errorf("embedding API unavailable")
		},
	}

	pm := newTestManager(t, database, es, &mockLLMService{})

	pq, _ := pm.CreatePending("Q1", "user-1", "")

	err := pm.AnswerQuestion(AdminAnswerRequest{QuestionID: pq.ID, Text: "Some answer text"})
	if err == nil {
		t.Fatal("expected error when embedding fails")
	}
}

func TestAnswerQuestion_LLMError(t *testing.T) {
	database, cleanup := setupTestDB(t)
	defer cleanup()

	ls := &mockLLMService{
		generateFunc: func(prompt string, context []string, question string) (string, error) {
			return "", fmt.Errorf("LLM service unavailable")
		},
	}

	pm := newTestManager(t, database, &mockEmbeddingService{}, ls)

	pq, _ := pm.CreatePending("Q1", "user-1", "")

	err := pm.AnswerQuestion(AdminAnswerRequest{QuestionID: pq.ID, Text: "Some answer"})
	if err == nil {
		t.Fatal("expected error when LLM fails")
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input    string
		maxLen   int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a longer string", 10, "this is a ..."},
		{"", 5, ""},
		{"你好世界测试", 4, "你好世界..."},
	}

	for _, tt := range tests {
		result := truncate(tt.input, tt.maxLen)
		if result != tt.expected {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.maxLen, result, tt.expected)
		}
	}
}
