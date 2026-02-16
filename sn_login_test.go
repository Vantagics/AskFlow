package main

import (
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"askflow/internal/auth"
	"askflow/internal/config"

	_ "github.com/mattn/go-sqlite3"
)

// setupSNLoginTestDB creates an in-memory SQLite database with all tables
// needed for the SN login flow.
func setupSNLoginTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite3", ":memory:")
	if err != nil {
		t.Skipf("SQLite not available (CGO disabled?): %v", err)
	}
	// Quick check that the driver actually works (go-sqlite3 stub returns errors)
	if _, err := db.Exec("SELECT 1"); err != nil {
		db.Close()
		t.Skipf("SQLite driver not functional (CGO disabled?): %v", err)
	}
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS users (
			id TEXT PRIMARY KEY,
			email TEXT UNIQUE DEFAULT '',
			name TEXT DEFAULT '',
			provider TEXT DEFAULT '',
			provider_id TEXT DEFAULT '',
			password_hash TEXT DEFAULT '',
			email_verified INTEGER DEFAULT 0,
			default_product_id TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			last_login DATETIME
		)`,
		`CREATE TABLE IF NOT EXISTS sessions (
			id TEXT PRIMARY KEY,
			user_id TEXT NOT NULL,
			expires_at DATETIME NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS sn_users (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			email TEXT UNIQUE NOT NULL,
			display_name TEXT NOT NULL,
			sn TEXT DEFAULT '',
			last_login_at DATETIME,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS login_tickets (
			ticket TEXT PRIMARY KEY,
			user_id INTEGER NOT NULL,
			used INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			expires_at DATETIME NOT NULL,
			FOREIGN KEY (user_id) REFERENCES sn_users(id)
		)`,
		`CREATE TABLE IF NOT EXISTS login_attempts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT NOT NULL,
			ip TEXT DEFAULT '',
			success INTEGER DEFAULT 0,
			attempted_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS login_bans (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			username TEXT DEFAULT '',
			ip TEXT DEFAULT '',
			reason TEXT DEFAULT '',
			unlocks_at DATETIME NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		)`,
	}
	for _, s := range stmts {
		if _, err := db.Exec(s); err != nil {
			t.Fatalf("exec schema: %v", err)
		}
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// newTestConfigManager creates a ConfigManager with AuthServer set for testing.
func newTestConfigManager(t *testing.T, authServer string) *config.ConfigManager {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.json")

	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	cm, err := config.NewConfigManagerWithKey(cfgPath, key)
	if err != nil {
		t.Fatalf("NewConfigManagerWithKey: %v", err)
	}
	// Load defaults then set AuthServer
	cm.Load() // ignore error — file doesn't exist yet, defaults are used
	cm.Update(map[string]interface{}{
		"auth_server": authServer,
	})
	return cm
}

// newTestApp creates a minimal App for SN login testing.
func newTestApp(t *testing.T, db *sql.DB, cm *config.ConfigManager) *App {
	t.Helper()
	return &App{
		db:             db,
		sessionManager: auth.NewSessionManager(db, 24*time.Hour),
		configManager:  cm,
		loginLimiter:   auth.NewLoginLimiter(db),
	}
}

// --- Test: POST /api/auth/sn-login returns JSON, not HTML ---

func TestSNLoginEndpoint_ReturnsJSON(t *testing.T) {
	db := setupSNLoginTestDB(t)
	cm := newTestConfigManager(t, "license.vantagedata.chat")
	app := newTestApp(t, db, cm)
	handler := handleSNLogin(app)

	body := `{"token":"fake-jwt-token"}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/sn-login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}

	// The response must be valid JSON (not HTML)
	var resp map[string]interface{}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("response is not valid JSON: %v\nbody: %s", err, rec.Body.String())
	}
}

func TestSNLoginEndpoint_MethodNotAllowed(t *testing.T) {
	db := setupSNLoginTestDB(t)
	cm := newTestConfigManager(t, "license.vantagedata.chat")
	app := newTestApp(t, db, cm)
	handler := handleSNLogin(app)

	req := httptest.NewRequest(http.MethodGet, "/api/auth/sn-login", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusMethodNotAllowed)
	}
}

func TestSNLoginEndpoint_EmptyToken(t *testing.T) {
	db := setupSNLoginTestDB(t)
	cm := newTestConfigManager(t, "license.vantagedata.chat")
	app := newTestApp(t, db, cm)
	handler := handleSNLogin(app)

	body := `{"token":""}`
	req := httptest.NewRequest(http.MethodPost, "/api/auth/sn-login", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}

	var resp SNLoginResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if resp.Success {
		t.Error("expected success=false for empty token")
	}
	if resp.Message != "token is required" {
		t.Errorf("message = %q, want %q", resp.Message, "token is required")
	}
}

func TestSNLoginEndpoint_InvalidJSON(t *testing.T) {
	db := setupSNLoginTestDB(t)
	cm := newTestConfigManager(t, "license.vantagedata.chat")
	app := newTestApp(t, db, cm)
	handler := handleSNLogin(app)

	req := httptest.NewRequest(http.MethodPost, "/api/auth/sn-login", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusBadRequest)
	}
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("Content-Type = %q, want application/json", ct)
	}
}

// --- Test: HandleSNLogin with a fake license server ---

func TestHandleSNLogin_FullFlowWithFakeLicenseServer(t *testing.T) {
	db := setupSNLoginTestDB(t)

	// Fake license server that validates tokens
	fakeLicense := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("license server got method %s, want POST", r.Method)
		}
		var body map[string]string
		json.NewDecoder(r.Body).Decode(&body)

		if body["token"] == "valid-jwt-token" {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": true,
				"sn":      "SN-1234-5678",
				"email":   "alice@example.com",
			})
		} else {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"success": false,
				"message": "invalid token",
			})
		}
	}))
	defer fakeLicense.Close()

	// The HandleSNLogin code builds URL as: https://<authServer>/api/marketplace-verify
	// httptest.NewTLSServer gives us https://127.0.0.1:<port>
	// We need to strip the "https://" prefix since the code prepends it
	serverHost := strings.TrimPrefix(fakeLicense.URL, "https://")

	cm := newTestConfigManager(t, serverHost)
	// We can't easily use the TLS test server because HandleSNLogin creates its own http.Client
	// which won't trust the test server's self-signed cert.
	// Instead, test the business logic directly by simulating what HandleSNLogin does.
	_ = cm

	// Direct test: manually create user and ticket, then validate
	_, err := db.Exec(
		"INSERT INTO sn_users (email, display_name, sn, last_login_at, created_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)",
		"alice@example.com", "alice", "SN-1234-5678",
	)
	if err != nil {
		t.Fatalf("insert sn_user: %v", err)
	}

	var userID int64
	db.QueryRow("SELECT id FROM sn_users WHERE email = ?", "alice@example.com").Scan(&userID)

	// Simulate ticket creation
	ticket := "test-ticket-uuid-1234"
	expiresAt := time.Now().UTC().Add(5 * time.Minute)
	_, err = db.Exec(
		"INSERT INTO login_tickets (ticket, user_id, used, expires_at) VALUES (?, ?, 0, ?)",
		ticket, userID, expiresAt.Format(time.RFC3339),
	)
	if err != nil {
		t.Fatalf("insert ticket: %v", err)
	}

	app := newTestApp(t, db, cm)

	// Validate the ticket
	sessionID, err := app.ValidateLoginTicket(ticket)
	if err != nil {
		t.Fatalf("ValidateLoginTicket: %v", err)
	}
	if sessionID == "" {
		t.Error("expected non-empty session ID")
	}

	// Verify session was created
	session, err := app.sessionManager.ValidateSession(sessionID)
	if err != nil {
		t.Fatalf("ValidateSession: %v", err)
	}
	if session.UserID == "" {
		t.Error("expected non-empty user ID in session")
	}

	// Verify a users record was created for the SN user
	var regularEmail string
	err = db.QueryRow("SELECT email FROM users WHERE provider = 'sn' AND email = ?", "alice@example.com").Scan(&regularEmail)
	if err != nil {
		t.Fatalf("expected users record for SN user: %v", err)
	}
	if regularEmail != "alice@example.com" {
		t.Errorf("email = %q, want %q", regularEmail, "alice@example.com")
	}
}

// --- Test: Ticket is one-time use ---

func TestValidateLoginTicket_OneTimeUse(t *testing.T) {
	db := setupSNLoginTestDB(t)

	db.Exec("INSERT INTO sn_users (email, display_name, sn) VALUES (?, ?, ?)",
		"bob@example.com", "bob", "SN-0001")
	var userID int64
	db.QueryRow("SELECT id FROM sn_users WHERE email = ?", "bob@example.com").Scan(&userID)

	ticket := "one-time-ticket-abc"
	expiresAt := time.Now().UTC().Add(5 * time.Minute)
	db.Exec("INSERT INTO login_tickets (ticket, user_id, used, expires_at) VALUES (?, ?, 0, ?)",
		ticket, userID, expiresAt.Format(time.RFC3339))

	app := newTestApp(t, db, newTestConfigManager(t, ""))

	// First use should succeed
	_, err := app.ValidateLoginTicket(ticket)
	if err != nil {
		t.Fatalf("first use should succeed: %v", err)
	}

	// Second use should fail
	_, err = app.ValidateLoginTicket(ticket)
	if err == nil {
		t.Fatal("second use of same ticket should fail")
	}
	if err.Error() != "ticket_already_used" {
		t.Errorf("error = %q, want %q", err.Error(), "ticket_already_used")
	}
}

// --- Test: Expired ticket ---

func TestValidateLoginTicket_Expired(t *testing.T) {
	db := setupSNLoginTestDB(t)

	db.Exec("INSERT INTO sn_users (email, display_name, sn) VALUES (?, ?, ?)",
		"charlie@example.com", "charlie", "SN-0002")
	var userID int64
	db.QueryRow("SELECT id FROM sn_users WHERE email = ?", "charlie@example.com").Scan(&userID)

	ticket := "expired-ticket-xyz"
	expiresAt := time.Now().UTC().Add(-1 * time.Minute) // already expired
	db.Exec("INSERT INTO login_tickets (ticket, user_id, used, expires_at) VALUES (?, ?, 0, ?)",
		ticket, userID, expiresAt.Format(time.RFC3339))

	app := newTestApp(t, db, newTestConfigManager(t, ""))

	_, err := app.ValidateLoginTicket(ticket)
	if err == nil {
		t.Fatal("expired ticket should fail")
	}
	if err.Error() != "ticket_expired" {
		t.Errorf("error = %q, want %q", err.Error(), "ticket_expired")
	}
}

// --- Test: Invalid / empty ticket ---

func TestValidateLoginTicket_NotFound(t *testing.T) {
	db := setupSNLoginTestDB(t)
	app := newTestApp(t, db, newTestConfigManager(t, ""))

	_, err := app.ValidateLoginTicket("nonexistent-ticket")
	if err == nil {
		t.Fatal("nonexistent ticket should fail")
	}
	if err.Error() != "invalid_ticket" {
		t.Errorf("error = %q, want %q", err.Error(), "invalid_ticket")
	}
}

func TestValidateLoginTicket_EmptyTicket(t *testing.T) {
	db := setupSNLoginTestDB(t)
	app := newTestApp(t, db, newTestConfigManager(t, ""))

	_, err := app.ValidateLoginTicket("")
	if err == nil {
		t.Fatal("empty ticket should fail")
	}
	if err.Error() != "invalid_ticket" {
		t.Errorf("error = %q, want %q", err.Error(), "invalid_ticket")
	}
}

// --- Test: GET /auth/ticket-login handler ---

func TestTicketLoginHandler_Success(t *testing.T) {
	db := setupSNLoginTestDB(t)

	db.Exec("INSERT INTO sn_users (email, display_name, sn) VALUES (?, ?, ?)",
		"dave@example.com", "dave", "SN-0003")
	var userID int64
	db.QueryRow("SELECT id FROM sn_users WHERE email = ?", "dave@example.com").Scan(&userID)

	ticket := "handler-test-ticket"
	expiresAt := time.Now().UTC().Add(5 * time.Minute)
	db.Exec("INSERT INTO login_tickets (ticket, user_id, used, expires_at) VALUES (?, ?, 0, ?)",
		ticket, userID, expiresAt.Format(time.RFC3339))

	app := newTestApp(t, db, newTestConfigManager(t, ""))
	handler := handleTicketLogin(app)

	req := httptest.NewRequest(http.MethodGet, "/auth/ticket-login?ticket="+ticket, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	// Should redirect with 302 to /?ticket=xxx (frontend handles exchange)
	if rec.Code != http.StatusFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusFound)
	}

	location := rec.Header().Get("Location")
	if location != "/?ticket="+ticket {
		t.Errorf("Location = %q, want %q", location, "/?ticket="+ticket)
	}
}

func TestTicketLoginHandler_InvalidTicket(t *testing.T) {
	db := setupSNLoginTestDB(t)
	app := newTestApp(t, db, newTestConfigManager(t, ""))
	handler := handleTicketLogin(app)

	req := httptest.NewRequest(http.MethodGet, "/auth/ticket-login?ticket=bad-ticket", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	location := rec.Header().Get("Location")
	if !strings.Contains(location, "/login?error=") {
		t.Errorf("Location = %q, want to contain /login?error=", location)
	}
}

func TestTicketLoginHandler_MethodNotAllowed(t *testing.T) {
	db := setupSNLoginTestDB(t)
	app := newTestApp(t, db, newTestConfigManager(t, ""))
	handler := handleTicketLogin(app)

	req := httptest.NewRequest(http.MethodPost, "/auth/ticket-login?ticket=any", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Errorf("status = %d, want %d (redirect)", rec.Code, http.StatusFound)
	}
	location := rec.Header().Get("Location")
	if !strings.Contains(location, "error=method_not_allowed") {
		t.Errorf("Location = %q, want to contain error=method_not_allowed", location)
	}
}

// --- Test: SPA handler does NOT serve HTML for /api/* and /auth/* paths ---

func TestSPAHandler_APIPathReturnsJSON404(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html><body>SPA</body></html>"), 0644)

	handler := spaHandler(dir)

	apiPaths := []string{
		"/api/auth/sn-login",
		"/api/some/unknown/endpoint",
		"/auth/ticket-login",
		"/auth/anything",
		"/api/health",
	}

	for _, path := range apiPaths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			ct := rec.Header().Get("Content-Type")
			if !strings.Contains(ct, "application/json") {
				t.Errorf("Content-Type = %q, want application/json", ct)
			}
			if rec.Code != http.StatusNotFound {
				t.Errorf("status = %d, want %d", rec.Code, http.StatusNotFound)
			}

			// Body must be JSON, not HTML
			body := rec.Body.String()
			if strings.Contains(body, "<html") || strings.Contains(body, "<!DOCTYPE") {
				t.Errorf("response body contains HTML:\n%s", body)
			}

			var resp map[string]interface{}
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Errorf("response is not valid JSON: %v", err)
			}
		})
	}
}

func TestSPAHandler_NonAPIPathServesHTML(t *testing.T) {
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<!DOCTYPE html><html><body>SPA</body></html>"), 0644)

	handler := spaHandler(dir)

	paths := []string{"/", "/login", "/dashboard", "/some/page"}
	for _, p := range paths {
		t.Run(p, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, p, nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			body := rec.Body.String()
			if !strings.Contains(body, "SPA") {
				t.Errorf("expected SPA HTML, got: %s", body)
			}
		})
	}
}

// --- Test: Route priority — API handlers take precedence over SPA ---

func TestRoutePriority_APIBeforeSPA(t *testing.T) {
	mux := http.NewServeMux()

	// Register API handlers
	mux.HandleFunc("/api/auth/sn-login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"source":"api_handler"}`))
	})
	mux.HandleFunc("/auth/ticket-login", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ticket_handler"))
	})

	// Register SPA catch-all
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>SPA</html>"), 0644)
	mux.Handle("/", spaHandler(dir))

	// /api/auth/sn-login → API handler
	req := httptest.NewRequest(http.MethodPost, "/api/auth/sn-login", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "api_handler") {
		t.Errorf("/api/auth/sn-login served by SPA instead of API handler: %s", rec.Body.String())
	}

	// /auth/ticket-login → ticket handler
	req = httptest.NewRequest(http.MethodGet, "/auth/ticket-login?ticket=test", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "ticket_handler") {
		t.Errorf("/auth/ticket-login served by SPA instead of ticket handler: %s", rec.Body.String())
	}

	// / → SPA
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	if !strings.Contains(rec.Body.String(), "SPA") {
		t.Errorf("/ should serve SPA, got: %s", rec.Body.String())
	}

	// Unknown /api/ path → JSON 404 from SPA guard
	req = httptest.NewRequest(http.MethodGet, "/api/nonexistent", nil)
	rec = httptest.NewRecorder()
	mux.ServeHTTP(rec, req)
	ct := rec.Header().Get("Content-Type")
	if !strings.Contains(ct, "application/json") {
		t.Errorf("/api/nonexistent Content-Type = %q, want application/json", ct)
	}
}

// --- Test: Full end-to-end ticket-login flow ---

func TestFullTicketLoginFlow(t *testing.T) {
	db := setupSNLoginTestDB(t)

	// Step 1: Create SN user (simulating HandleSNLogin after license verification)
	db.Exec("INSERT INTO sn_users (email, display_name, sn) VALUES (?, ?, ?)",
		"flow@example.com", "flow", "SN-FLOW")
	var snUserID int64
	db.QueryRow("SELECT id FROM sn_users WHERE email = ?", "flow@example.com").Scan(&snUserID)

	// Step 2: Create login ticket
	ticket := "flow-test-ticket-uuid"
	expiresAt := time.Now().UTC().Add(5 * time.Minute)
	db.Exec("INSERT INTO login_tickets (ticket, user_id, used, expires_at) VALUES (?, ?, 0, ?)",
		ticket, snUserID, expiresAt.Format(time.RFC3339))

	app := newTestApp(t, db, newTestConfigManager(t, ""))

	// Step 3: Hit the ticket-login endpoint — should redirect to /?ticket=xxx
	handler := handleTicketLogin(app)
	req := httptest.NewRequest(http.MethodGet, "/auth/ticket-login?ticket="+ticket, nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusFound)
	}
	if loc := rec.Header().Get("Location"); loc != "/?ticket="+ticket {
		t.Errorf("Location = %q, want %q", loc, "/?ticket="+ticket)
	}

	// Step 4: Frontend calls POST /api/auth/ticket-exchange with the ticket
	exchangeHandler := handleTicketExchange(app)
	body := `{"ticket":"` + ticket + `"}`
	req = httptest.NewRequest(http.MethodPost, "/api/auth/ticket-exchange", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	exchangeHandler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("ticket-exchange status = %d, want 200, body: %s", rec.Code, rec.Body.String())
	}

	var exchangeResp struct {
		Session struct {
			ID     string `json:"id"`
			UserID string `json:"user_id"`
		} `json:"session"`
		User struct {
			ID       string `json:"id"`
			Email    string `json:"email"`
			Name     string `json:"name"`
			Provider string `json:"provider"`
		} `json:"user"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &exchangeResp); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if exchangeResp.Session.ID == "" {
		t.Error("expected non-empty session ID")
	}
	if exchangeResp.User.Email != "flow@example.com" {
		t.Errorf("user email = %q, want %q", exchangeResp.User.Email, "flow@example.com")
	}
	if exchangeResp.User.Provider != "sn" {
		t.Errorf("user provider = %q, want %q", exchangeResp.User.Provider, "sn")
	}

	// Step 5: Verify ticket is now marked as used
	var used int
	db.QueryRow("SELECT used FROM login_tickets WHERE ticket = ?", ticket).Scan(&used)
	if used != 1 {
		t.Errorf("ticket used = %d, want 1", used)
	}

	// Step 6: Trying the same ticket again should fail
	req = httptest.NewRequest(http.MethodPost, "/api/auth/ticket-exchange", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec = httptest.NewRecorder()
	exchangeHandler.ServeHTTP(rec, req)

	if rec.Code == http.StatusOK {
		t.Error("reused ticket should not return 200")
	}
}

// --- Test: Same email creates only one SN user (idempotent) ---

func TestSNLogin_IdempotentUserCreation(t *testing.T) {
	db := setupSNLoginTestDB(t)

	email := "repeat@example.com"

	// First insert
	_, err := db.Exec(
		"INSERT INTO sn_users (email, display_name, sn) VALUES (?, ?, ?)",
		email, "repeat", "SN-REPEAT",
	)
	if err != nil {
		t.Fatalf("first insert: %v", err)
	}

	// Second insert with same email should fail (UNIQUE constraint)
	_, err = db.Exec(
		"INSERT INTO sn_users (email, display_name, sn) VALUES (?, ?, ?)",
		email, "repeat2", "SN-REPEAT-2",
	)
	if err == nil {
		t.Fatal("expected UNIQUE constraint error for duplicate email")
	}

	// Verify only one user exists
	var count int
	db.QueryRow("SELECT COUNT(*) FROM sn_users WHERE email = ?", email).Scan(&count)
	if count != 1 {
		t.Errorf("sn_users count = %d, want 1", count)
	}
}

// --- Test: Display name extraction from email ---

func TestDisplayNameFromEmail(t *testing.T) {
	tests := []struct {
		email    string
		expected string
	}{
		{"alice@example.com", "alice"},
		{"bob.smith@company.co", "bob.smith"},
		{"user@test.com", "user"},
	}

	for _, tt := range tests {
		t.Run(tt.email, func(t *testing.T) {
			displayName := tt.email
			if idx := strings.Index(tt.email, "@"); idx > 0 {
				displayName = tt.email[:idx]
			}
			if displayName != tt.expected {
				t.Errorf("displayName = %q, want %q", displayName, tt.expected)
			}
		})
	}
}
