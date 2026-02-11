package main

import (
	"database/sql"
	"os"
	"testing"

	"helpdesk/internal/auth"
	"helpdesk/internal/chunker"
	"helpdesk/internal/config"
	"helpdesk/internal/document"
	"helpdesk/internal/email"
	"helpdesk/internal/pending"
	"helpdesk/internal/query"
	"helpdesk/internal/vectorstore"

	_ "github.com/mattn/go-sqlite3"
)

// --- mock services ---

type mockEmbeddingService struct{}

func (m *mockEmbeddingService) Embed(text string) ([]float64, error) {
	return []float64{0.1, 0.2, 0.3}, nil
}
func (m *mockEmbeddingService) EmbedBatch(texts []string) ([][]float64, error) {
	result := make([][]float64, len(texts))
	for i := range texts {
		result[i] = []float64{0.1, 0.2, 0.3}
	}
	return result, nil
}
func (m *mockEmbeddingService) EmbedImageURL(imageURL string) ([]float64, error) {
	return []float64{0.1, 0.2, 0.3}, nil
}

type mockLLMService struct{}

func (m *mockLLMService) Generate(prompt string, context []string, question string) (string, error) {
	return "mock answer", nil
}

// --- helpers ---

func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON"); err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE IF NOT EXISTS documents (id TEXT PRIMARY KEY, name TEXT, type TEXT, status TEXT, error TEXT, created_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS chunks (id TEXT PRIMARY KEY, document_id TEXT, document_name TEXT, chunk_index INTEGER, chunk_text TEXT, embedding BLOB, image_url TEXT DEFAULT '', created_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS pending_questions (id TEXT PRIMARY KEY, question TEXT, user_id TEXT, status TEXT, answer TEXT, llm_answer TEXT, created_at DATETIME, answered_at DATETIME)`,
		`CREATE TABLE IF NOT EXISTS users (id TEXT PRIMARY KEY, email TEXT, name TEXT, provider TEXT NOT NULL, provider_id TEXT NOT NULL, password_hash TEXT, email_verified INTEGER DEFAULT 0, created_at DATETIME, last_login DATETIME)`,
		`CREATE TABLE IF NOT EXISTS email_tokens (id TEXT PRIMARY KEY, user_id TEXT NOT NULL, token TEXT NOT NULL UNIQUE, type TEXT NOT NULL, expires_at DATETIME NOT NULL, created_at DATETIME DEFAULT CURRENT_TIMESTAMP, FOREIGN KEY (user_id) REFERENCES users(id))`,
		`CREATE TABLE IF NOT EXISTS sessions (id TEXT PRIMARY KEY, user_id TEXT, expires_at TEXT, created_at TEXT, FOREIGN KEY (user_id) REFERENCES users(id))`,
		`CREATE TABLE IF NOT EXISTS admin_users (id TEXT PRIMARY KEY, username TEXT NOT NULL UNIQUE, password_hash TEXT NOT NULL, role TEXT NOT NULL DEFAULT 'editor', created_at DATETIME DEFAULT CURRENT_TIMESTAMP)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func setupTestApp(t *testing.T) (*App, *sql.DB) {
	t.Helper()
	db := setupTestDB(t)

	es := &mockEmbeddingService{}
	ls := &mockLLMService{}
	vs := vectorstore.NewSQLiteVectorStore(db)
	tc := &chunker.TextChunker{ChunkSize: 512, Overlap: 128}

	qe := query.NewQueryEngine(es, vs, ls, db, config.DefaultConfig())
	dm := document.NewDocumentManager(nil, tc, es, vs, db)
	pm := pending.NewPendingQuestionManager(db, tc, es, vs, ls)
	oc := auth.NewOAuthClient(map[string]config.OAuthProviderConfig{
		"google": {
			ClientID:     "test-client-id",
			ClientSecret: "test-secret",
			AuthURL:      "https://accounts.google.com/o/oauth2/auth",
			TokenURL:     "https://oauth2.googleapis.com/token",
			RedirectURL:  "http://localhost:8080/callback",
			Scopes:       []string{"openid", "email"},
		},
	})
	sm := auth.NewSessionManager(db, 0)

	tmpFile, err := os.CreateTemp("", "config-*.json")
	if err != nil {
		t.Fatal(err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	os.Remove(tmpPath) // Remove so Load() creates defaults
	t.Cleanup(func() { os.Remove(tmpPath) })

	cm, err := config.NewConfigManager(tmpPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := cm.Load(); err != nil {
		t.Fatal(err)
	}

	app := NewApp(db, qe, dm, pm, oc, sm, cm, email.NewService(func() config.SMTPConfig { return config.SMTPConfig{} }))
	return app, db
}

func TestQuery_EmptyVectorStore(t *testing.T) {
	app, _ := setupTestApp(t)

	resp, err := app.Query("What is Go?")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.IsPending {
		t.Error("expected IsPending=true when vector store is empty")
	}
	if resp.Message == "" {
		t.Error("expected a message for pending questions")
	}
}

func TestListDocuments_Empty(t *testing.T) {
	app, _ := setupTestApp(t)

	docs, err := app.ListDocuments()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(docs) != 0 {
		t.Errorf("expected 0 documents, got %d", len(docs))
	}
}

func TestListPendingQuestions_Empty(t *testing.T) {
	app, _ := setupTestApp(t)

	questions, err := app.ListPendingQuestions("")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(questions) != 0 {
		t.Errorf("expected 0 pending questions, got %d", len(questions))
	}
}

func TestGetOAuthURL(t *testing.T) {
	app, _ := setupTestApp(t)

	url, err := app.GetOAuthURL("google")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if url == "" {
		t.Error("expected non-empty OAuth URL")
	}
}

func TestGetOAuthURL_UnsupportedProvider(t *testing.T) {
	app, _ := setupTestApp(t)

	_, err := app.GetOAuthURL("unknown")
	if err == nil {
		t.Error("expected error for unsupported provider")
	}
}

func TestAdminLogin_WrongPassword(t *testing.T) {
	app, _ := setupTestApp(t)

	// Setup admin first
	_, err := app.AdminSetup("admin", "admin123")
	if err != nil {
		t.Fatal(err)
	}

	_, err = app.AdminLogin("admin", "wrong-password")
	if err == nil {
		t.Error("expected error for wrong admin password")
	}
}

func TestAdminLogin_Success(t *testing.T) {
	app, _ := setupTestApp(t)

	// Setup admin first
	_, err := app.AdminSetup("admin", "admin123")
	if err != nil {
		t.Fatal(err)
	}

	resp, err := app.AdminLogin("admin", "admin123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Session == nil {
		t.Error("expected session in response")
	}
	if resp.Session.UserID != "admin" {
		t.Errorf("expected session user_id='admin', got %q", resp.Session.UserID)
	}
}

func TestAdminSetup_FirstTime(t *testing.T) {
	app, _ := setupTestApp(t)

	if app.IsAdminConfigured() {
		t.Error("expected admin not configured initially")
	}

	resp, err := app.AdminSetup("myadmin", "mypassword")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Session == nil {
		t.Error("expected session in response")
	}

	if !app.IsAdminConfigured() {
		t.Error("expected admin to be configured after setup")
	}

	// Second setup should fail
	_, err = app.AdminSetup("another", "password")
	if err == nil {
		t.Error("expected error on second admin setup")
	}
}

func TestAdminLogin_NotConfigured(t *testing.T) {
	app, _ := setupTestApp(t)

	_, err := app.AdminLogin("admin", "admin123")
	if err == nil {
		t.Error("expected error when admin not configured")
	}
}

func TestGetConfig_MasksSecrets(t *testing.T) {
	app, _ := setupTestApp(t)

	cfg := app.GetConfig()
	if cfg == nil {
		t.Fatal("expected non-nil config")
	}

	// Default config has API keys set — they should be masked
	if cfg.LLM.APIKey != "***" && cfg.LLM.APIKey != "" {
		t.Errorf("expected LLM API key to be masked, got %q", cfg.LLM.APIKey)
	}
	if cfg.Embedding.APIKey != "***" && cfg.Embedding.APIKey != "" {
		t.Errorf("expected Embedding API key to be masked, got %q", cfg.Embedding.APIKey)
	}
}

func TestUpdateConfig(t *testing.T) {
	app, _ := setupTestApp(t)

	err := app.UpdateConfig(map[string]interface{}{
		"llm.model_name": "gpt-4o",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the update took effect (via the raw config, not masked)
	raw := app.configManager.Get()
	if raw.LLM.ModelName != "gpt-4o" {
		t.Errorf("expected model_name='gpt-4o', got %q", raw.LLM.ModelName)
	}
}

func TestMaskSecret(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"some-api-key", "***"},
		{"", ""},
		{"   ", ""},
	}
	for _, tt := range tests {
		got := maskSecret(tt.input)
		if got != tt.expected {
			t.Errorf("maskSecret(%q) = %q, want %q", tt.input, got, tt.expected)
		}
	}
}

func TestDeleteDocument_NonExistent(t *testing.T) {
	app, _ := setupTestApp(t)

	// Deleting a non-existent document should not error (no rows affected)
	err := app.DeleteDocument("non-existent-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}
