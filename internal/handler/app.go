// Package handler provides the App struct that serves as the API facade
// for the askflow system, delegating to internal service components.
package handler

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	mrand "math/rand"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"askflow/internal/auth"
	"askflow/internal/config"
	"askflow/internal/document"
	"askflow/internal/email"
	"askflow/internal/embedding"
	"askflow/internal/errlog"
	"askflow/internal/llm"
	"askflow/internal/pending"
	"askflow/internal/product"
	"askflow/internal/query"
	"askflow/internal/vectorstore"
)

// httpClient is an alias for http.Client used for outbound requests.
type httpClient = http.Client

// App is the API facade that binds all backend services for the frontend.
// Each public method delegates to the appropriate service component.
type App struct {
	db             *sql.DB // write DB (also used for reads in App-level queries)
	readDB         *sql.DB // read-only DB pool for concurrent reads
	queryEngine    *query.QueryEngine
	docManager     *document.DocumentManager
	pendingManager *pending.PendingQuestionManager
	oauthClient    *auth.OAuthClient
	sessionManager *auth.SessionManager
	configManager  *config.ConfigManager
	emailService   *email.Service
	productService *product.ProductService
	loginLimiter   *auth.LoginLimiter
}

// NewApp creates a new App with all service dependencies injected.
func NewApp(
	writeDB *sql.DB,
	readDB *sql.DB,
	qe *query.QueryEngine,
	dm *document.DocumentManager,
	pm *pending.PendingQuestionManager,
	oc *auth.OAuthClient,
	sm *auth.SessionManager,
	cm *config.ConfigManager,
	es *email.Service,
	ps *product.ProductService,
) *App {
	return &App{
		db:             writeDB,
		readDB:         readDB,
		queryEngine:    qe,
		docManager:     dm,
		pendingManager: pm,
		oauthClient:    oc,
		sessionManager: sm,
		configManager:  cm,
		emailService:   es,
		productService: ps,
		loginLimiter:   auth.NewLoginLimiterRW(readDB, writeDB),
	}
}
// SessionManager returns the session manager for testing purposes.
func (a *App) SessionManager() *auth.SessionManager {
	return a.sessionManager
}

// --- Query Interface ---

// Query processes a user question through the RAG pipeline.
func (a *App) Query(req query.QueryRequest) (*query.QueryResponse, error) {
	return a.queryEngine.Query(req)
}

// --- Document Management Interface ---

// UploadFile uploads and processes a document file.
func (a *App) UploadFile(req document.UploadFileRequest) (*document.DocumentInfo, error) {
	return a.docManager.UploadFile(req)
}

// UploadURL fetches and processes content from a URL.
func (a *App) UploadURL(req document.UploadURLRequest) (*document.DocumentInfo, error) {
	return a.docManager.UploadURL(req)
}

// PreviewURL fetches and parses URL content for preview.
func (a *App) PreviewURL(url string) (*document.URLPreviewResult, error) {
	return a.docManager.PreviewURL(url)
}

// ListDocuments returns uploaded documents, optionally filtered by product ID.
func (a *App) ListDocuments(productID string) ([]document.DocumentInfo, error) {
	return a.docManager.ListDocuments(productID)
}

// DeleteDocument removes a document and its associated vectors.
func (a *App) DeleteDocument(docID string) error {
	return a.docManager.DeleteDocument(docID)
}

// GetDocumentInfo returns metadata for a single document by ID.
func (a *App) GetDocumentInfo(docID string) (*document.DocumentInfo, error) {
	return a.docManager.GetDocumentInfo(docID)
}

// --- Pending Questions Interface ---

// ListPendingQuestions returns pending questions filtered by status and productID.
// Pass an empty string to list all questions.
func (a *App) ListPendingQuestions(status string, productID string) ([]pending.PendingQuestion, error) {
	return a.pendingManager.ListPending(status, productID)
}

// AnswerQuestion submits an admin answer to a pending question.
func (a *App) AnswerQuestion(req pending.AdminAnswerRequest) error {
	return a.pendingManager.AnswerQuestion(req)
}

// DeletePendingQuestion removes a pending question by ID.
func (a *App) DeletePendingQuestion(id string) error {
	return a.pendingManager.DeletePending(id)
}

// CreatePendingQuestion creates a new pending question from a user who is not satisfied with the answer.
func (a *App) CreatePendingQuestion(question, userID, imageData, productID string) (*pending.PendingQuestion, error) {
	return a.pendingManager.CreatePending(question, userID, imageData, productID)
}

// --- Authentication Interface ---

// GetOAuthURL returns the OAuth authorization URL for the given provider.
func (a *App) GetOAuthURL(provider string) (string, error) {
	// Validate provider name
	if len(provider) > 50 || strings.ContainsAny(provider, "/<>\"'\\") {
		return "", fmt.Errorf("invalid provider name")
	}
	return a.oauthClient.GetAuthURL(provider)
}

// OAuthCallbackResponse contains the result of an OAuth callback.
type OAuthCallbackResponse struct {
	User    *auth.OAuthUser `json:"user"`
	Session *auth.Session   `json:"session"`
}

// HandleOAuthCallback exchanges the auth code for user info and creates a session.
func (a *App) HandleOAuthCallback(provider, code string) (*OAuthCallbackResponse, error) {
	// Validate provider name to prevent injection
	if len(provider) > 50 || strings.ContainsAny(provider, "/<>\"'\\") {
		return nil, fmt.Errorf("invalid provider name")
	}
	user, err := a.oauthClient.HandleCallback(provider, code)
	if err != nil {
		return nil, err
	}

	// Upsert user into the users table
	_, err = a.db.Exec(
		`INSERT INTO users (id, email, name, provider, provider_id, email_verified) VALUES (?, ?, ?, ?, ?, 1)
		 ON CONFLICT(id) DO UPDATE SET name=excluded.name, email=excluded.email, last_login=CURRENT_TIMESTAMP`,
		provider+"_"+user.ID, user.Email, user.Name, provider, user.ID,
	)
	if err != nil {
		return nil, fmt.Errorf("upsert OAuth user: %w", err)
	}

	session, err := a.sessionManager.CreateSession(provider + "_" + user.ID)
	if err != nil {
		return nil, err
	}

	return &OAuthCallbackResponse{
		User:    user,
		Session: session,
	}, nil
}

// GetEnabledOAuthProviders returns the list of OAuth provider names that have
// been configured with at least client_id, client_secret, auth_url, and token_url.
func (a *App) GetEnabledOAuthProviders() []string {
	cfg := a.configManager.Get()
	if cfg == nil || cfg.OAuth.Providers == nil {
		return nil
	}
	var enabled []string
	for name, p := range cfg.OAuth.Providers {
		if p.ClientID != "" && p.ClientSecret != "" && p.AuthURL != "" && p.TokenURL != "" {
			enabled = append(enabled, name)
		}
	}
	return enabled
}

// RefreshOAuthClient rebuilds the OAuthClient from the current config.
// Called after OAuth provider settings are updated.
func (a *App) RefreshOAuthClient() {
	cfg := a.configManager.Get()
	a.oauthClient = auth.NewOAuthClient(cfg.OAuth.Providers)
}

// DeleteOAuthProvider removes an OAuth provider from the config.
func (a *App) DeleteOAuthProvider(provider string) error {
	cfg := a.configManager.Get()
	if cfg == nil || cfg.OAuth.Providers == nil {
		return fmt.Errorf("no OAuth providers configured")
	}
	if _, ok := cfg.OAuth.Providers[provider]; !ok {
		return fmt.Errorf("provider %s not found", provider)
	}
	if err := a.configManager.DeleteOAuthProvider(provider); err != nil {
		return fmt.Errorf("delete OAuth provider: %w", err)
	}
	a.RefreshOAuthClient()
	return nil
}

// AdminLoginResponse contains the session created after admin login.
type AdminLoginResponse struct {
	Session *auth.Session `json:"session"`
	Role    string        `json:"role,omitempty"`
}

// AdminUserInfo holds info about an admin sub-account.
type AdminUserInfo struct {
	ID           string   `json:"id"`
	Username     string   `json:"username"`
	Role         string   `json:"role"`
	CreatedAt    string   `json:"created_at,omitempty"`
	ProductNames []string `json:"product_names,omitempty"`
	Permissions  []string `json:"permissions,omitempty"`
}

// IsAdminConfigured returns whether the admin account has been set up.
func (a *App) IsAdminConfigured() bool {
	cfg := a.configManager.Get()
	return cfg.Admin.Username != "" && cfg.Admin.PasswordHash != ""
}

// AdminSetup sets the admin username and password for the first time.
// Returns an error if admin is already configured.
func (a *App) AdminSetup(username, password string) (*AdminLoginResponse, error) {
	if a.IsAdminConfigured() {
		return nil, fmt.Errorf("管理员账号已设置")
	}
	username = strings.TrimSpace(username)
	password = strings.TrimSpace(password)
	if username == "" || password == "" {
		return nil, fmt.Errorf("用户名和密码不能为空")
	}
	if len(username) < 3 {
		return nil, fmt.Errorf("用户名至少3位")
	}
	if msg := ValidatePassword(password); msg != "" {
		return nil, errors.New(msg)
	}
	if len(username) > 64 {
		return nil, fmt.Errorf("用户名不能超过64位")
	}
	// Reject usernames with special characters that could cause issues
	for _, c := range username {
		if c < 0x20 || c == '"' || c == '\'' || c == '\\' || c == '<' || c == '>' {
			return nil, fmt.Errorf("用户名包含非法字符")
		}
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}

	err = a.configManager.Update(map[string]interface{}{
		"admin.username":      username,
		"admin.password_hash": hash,
	})
	if err != nil {
		return nil, err
	}

	if err := a.ensureAdminUser(); err != nil {
		return nil, err
	}

	session, err := a.sessionManager.CreateSession("admin")
	if err != nil {
		return nil, err
	}

	return &AdminLoginResponse{Session: session, Role: "super_admin"}, nil
}

// GetAdminRole returns the role for a session user ID.
// "admin" → super_admin, "admin_xxx" → lookup from admin_users table.
func (a *App) GetAdminRole(userID string) string {
	if userID == "admin" {
		return "super_admin"
	}
	if strings.HasPrefix(userID, "admin_") {
		subID := strings.TrimPrefix(userID, "admin_")
		var role string
		err := a.readDB.QueryRow(`SELECT role FROM admin_users WHERE id = ?`, subID).Scan(&role)
		if err == nil {
			return role
		}
	}
	return ""
}

// GetAdminPermissions returns the permissions list for an admin user.
// super_admin has all permissions implicitly.
func (a *App) GetAdminPermissions(userID string) []string {
	if userID == "admin" {
		return []string{"batch_import"}
	}
	if strings.HasPrefix(userID, "admin_") {
		subID := strings.TrimPrefix(userID, "admin_")
		var role, permsStr string
		err := a.readDB.QueryRow(`SELECT role, COALESCE(permissions,'') FROM admin_users WHERE id = ?`, subID).Scan(&role, &permsStr)
		if err != nil {
			return nil
		}
		if role == "super_admin" {
			return []string{"batch_import"}
		}
		if permsStr == "" {
			return nil
		}
		return strings.Split(permsStr, ",")
	}
	return nil
}

// IsAdminSession checks if a user ID belongs to any admin (super or sub).
func (a *App) IsAdminSession(userID string) bool {
	return userID == "admin" || strings.HasPrefix(userID, "admin_")
}

// AdminLogin verifies the admin username and password and creates a session.
// Checks the super admin first, then admin sub-accounts.
// Enforces login rate limiting based on failed attempts per username and IP.
func (a *App) AdminLogin(username, password, ip string) (*AdminLoginResponse, error) {
	// Check login rate limits before attempting authentication
	if err := a.loginLimiter.CheckAllowed(username, ip); err != nil {
		return nil, err
	}

	cfg := a.configManager.Get()

	// Check super admin
	if cfg.Admin.Username != "" && cfg.Admin.PasswordHash != "" && username == cfg.Admin.Username {
		if err := auth.VerifyAdminPassword(password, cfg.Admin.PasswordHash); err != nil {
			a.loginLimiter.RecordAttempt(username, ip, false)
			log.Printf("[Auth] failed admin login attempt: username=%q ip=%s", username, ip)
			return nil, fmt.Errorf("用户名或密码错误")
		}
		a.loginLimiter.RecordAttempt(username, ip, true)
		log.Printf("[Auth] successful admin login: username=%q ip=%s", username, ip)
		if err := a.ensureAdminUser(); err != nil {
			return nil, err
		}
		// Session rotation: invalidate old sessions before creating new one
		_ = a.sessionManager.DeleteSessionsByUserID("admin")
		session, err := a.sessionManager.CreateSession("admin")
		if err != nil {
			return nil, err
		}
		return &AdminLoginResponse{Session: session, Role: "super_admin"}, nil
	}

	// Check admin sub-accounts
	var id, passwordHash, role string
	err := a.readDB.QueryRow(
		`SELECT id, password_hash, role FROM admin_users WHERE username = ?`, username,
	).Scan(&id, &passwordHash, &role)
	if err != nil {
		a.loginLimiter.RecordAttempt(username, ip, false)
		log.Printf("[Auth] failed sub-admin login attempt: username=%q ip=%s (user not found)", username, ip)
		return nil, fmt.Errorf("用户名或密码错误")
	}
	if err := auth.VerifyAdminPassword(password, passwordHash); err != nil {
		a.loginLimiter.RecordAttempt(username, ip, false)
		log.Printf("[Auth] failed sub-admin login attempt: username=%q ip=%s (wrong password)", username, ip)
		return nil, fmt.Errorf("用户名或密码错误")
	}
	a.loginLimiter.RecordAttempt(username, ip, true)
	log.Printf("[Auth] successful sub-admin login: username=%q ip=%s role=%s", username, ip, role)

	// Ensure user record exists for FK
	a.db.Exec(
		`INSERT OR IGNORE INTO users (id, email, name, provider, provider_id) VALUES (?, ?, ?, ?, ?)`,
		"admin_"+id, "admin_"+id+"@internal", username, "admin_sub", id,
	)

	// Session rotation: invalidate old sessions before creating new one
	_ = a.sessionManager.DeleteSessionsByUserID("admin_" + id)
	session, err := a.sessionManager.CreateSession("admin_" + id)
	if err != nil {
		return nil, err
	}
	return &AdminLoginResponse{Session: session, Role: role}, nil
}

// ensureAdminUser inserts the admin user record into the users table if it doesn't exist.
func (a *App) ensureAdminUser() error {
	_, err := a.db.Exec(
		`INSERT OR IGNORE INTO users (id, email, name, provider, provider_id) VALUES (?, ?, ?, ?, ?)`,
		"admin", "admin@internal", "管理员", "local", "admin",
	)
	if err != nil {
		return fmt.Errorf("ensure admin user: %w", err)
	}
	// Fix legacy records with empty email to avoid UNIQUE conflicts
	if _, err := a.db.Exec(`UPDATE users SET email = 'admin@internal' WHERE id = 'admin' AND email = ''`); err != nil {
		log.Printf("[ensureAdminUser] failed to fix legacy admin email: %v", err)
	}
	return nil
}

// --- User Registration Interface ---

// RegisterRequest holds user registration data.
type RegisterRequest struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

// Register creates a new user account and sends a verification email.
func (a *App) Register(req RegisterRequest, baseURL string) error {
	email := strings.TrimSpace(req.Email)
	name := strings.TrimSpace(req.Name)
	password := req.Password

	if email == "" || password == "" {
		return fmt.Errorf("邮箱和密码不能为空")
	}
	// Basic email format validation
	if !strings.Contains(email, "@") || !strings.Contains(email, ".") || len(email) > 254 {
		return fmt.Errorf("邮箱格式不正确")
	}
	if msg := ValidatePassword(password); msg != "" {
		return errors.New(msg)
	}
	if len(name) > 200 {
		return fmt.Errorf("名称过长")
	}
	if name == "" {
		name = email
	}

	// Check if email already exists (use writeDB to avoid TOCTOU race with concurrent registrations)
	var existingID string
	err := a.db.QueryRow("SELECT id FROM users WHERE email = ?", email).Scan(&existingID)
	if err == nil {
		return fmt.Errorf("该邮箱已注册")
	}
	if err != sql.ErrNoRows {
		return fmt.Errorf("查询用户失败: %w", err)
	}

	// Hash password
	hash, err := auth.HashPassword(password)
	if err != nil {
		return err
	}

	// Generate user ID
	userID, err := generateToken()
	if err != nil {
		return err
	}

	// Insert user (unverified)
	_, err = a.db.Exec(
		`INSERT INTO users (id, email, name, provider, provider_id, password_hash, email_verified) VALUES (?, ?, ?, ?, ?, ?, 0)`,
		userID, email, name, "local", email, hash,
	)
	if err != nil {
		return fmt.Errorf("创建用户失败: %w", err)
	}

	// Generate verification token
	token, err := generateToken()
	if err != nil {
		return err
	}
	expiresAt := time.Now().UTC().Add(24 * time.Hour)
	_, err = a.db.Exec(
		`INSERT INTO email_tokens (id, user_id, token, type, expires_at) VALUES (?, ?, ?, 'verify', ?)`,
		token, userID, token, expiresAt.Format(time.RFC3339),
	)
	if err != nil {
		return fmt.Errorf("创建验证令牌失败: %w", err)
	}

	// Send verification email asynchronously so registration returns immediately
	verifyURL := strings.TrimRight(baseURL, "/") + "/verify?token=" + token
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[Register] panic sending verification email to %s: %v", email, r)
			}
		}()
		if err := a.emailService.SendVerification(email, name, verifyURL); err != nil {
			log.Printf("[Register] failed to send verification email to %s: %v", email, err)
			errlog.Logf("[Email] failed to send verification email to %s: %v", email, err)
		}
	}()

	return nil
}

// VerifyEmail verifies a user's email using the token.
func (a *App) VerifyEmail(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("无效的验证链接")
	}

	var userID, expiresAtStr string
	// Use writeDB for the token lookup to prevent TOCTOU race:
	// two concurrent requests with the same token could both read it from readDB
	// before either deletes it, causing double-verification.
	err := a.db.QueryRow(
		`SELECT user_id, expires_at FROM email_tokens WHERE token = ? AND type = 'verify'`, token,
	).Scan(&userID, &expiresAtStr)
	if err == sql.ErrNoRows {
		return fmt.Errorf("验证链接无效或已过期")
	}
	if err != nil {
		return fmt.Errorf("查询验证令牌失败: %w", err)
	}

	expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
	if err != nil {
		expiresAt, _ = time.Parse("2006-01-02T15:04:05Z", expiresAtStr)
	}
	if time.Now().UTC().After(expiresAt) {
		return fmt.Errorf("验证链接已过期，请重新注册")
	}

	// Mark email as verified
	_, err = a.db.Exec(`UPDATE users SET email_verified = 1 WHERE id = ?`, userID)
	if err != nil {
		return fmt.Errorf("验证失败: %w", err)
	}

	// Delete used token
	a.db.Exec(`DELETE FROM email_tokens WHERE token = ?`, token)

	return nil
}

// UserLoginResponse contains the session and user info after login.
type UserLoginResponse struct {
	Session *auth.Session `json:"session"`
	User    *UserInfo     `json:"user"`
}

// UserInfo holds basic user info for the frontend.
type UserInfo struct {
	ID               string `json:"id"`
	Email            string `json:"email"`
	Name             string `json:"name"`
	Provider         string `json:"provider"`
	DefaultProductID string `json:"default_product_id,omitempty"`
}

// UserLogin authenticates a user with email and password.
func (a *App) UserLogin(email, password, ip string) (*UserLoginResponse, error) {
	email = strings.TrimSpace(email)
	if email == "" || password == "" {
		return nil, fmt.Errorf("邮箱和密码不能为空")
	}

	// Check login rate limits and manual bans
	if err := a.loginLimiter.CheckAllowed(email, ip); err != nil {
		return nil, err
	}

	var userID, name, passwordHash string
	var emailVerified int
	err := a.readDB.QueryRow(
		`SELECT id, name, password_hash, email_verified FROM users WHERE email = ? AND provider = 'local'`,
		email,
	).Scan(&userID, &name, &passwordHash, &emailVerified)
	if err == sql.ErrNoRows {
		a.loginLimiter.RecordAttempt(email, ip, false)
		return nil, fmt.Errorf("邮箱或密码错误")
	}
	if err != nil {
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}

	if emailVerified == 0 {
		return nil, fmt.Errorf("邮箱未验证，请先查收验证邮件")
	}

	if err := auth.VerifyAdminPassword(password, passwordHash); err != nil {
		a.loginLimiter.RecordAttempt(email, ip, false)
		return nil, fmt.Errorf("邮箱或密码错误")
	}

	a.loginLimiter.RecordAttempt(email, ip, true)

	// Update last login
	a.db.Exec(`UPDATE users SET last_login = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339), userID)

	// Session rotation: invalidate old sessions before creating new one
	_ = a.sessionManager.DeleteSessionsByUserID(userID)
	session, err := a.sessionManager.CreateSession(userID)
	if err != nil {
		return nil, err
	}

	// Fetch default product (best-effort, column may not exist yet)
	var defaultProductID string
	_ = a.readDB.QueryRow(`SELECT COALESCE(default_product_id, '') FROM users WHERE id = ?`, userID).Scan(&defaultProductID)

	return &UserLoginResponse{
		Session: session,
		User: &UserInfo{
			ID:               userID,
			Email:            email,
			Name:             name,
			Provider:         "local",
			DefaultProductID: defaultProductID,
		},
	}, nil
}

// --- Captcha System ---

type captchaEntry struct {
	answer    int
	expiresAt time.Time
}

var (
	captchaStore = make(map[string]captchaEntry)
	captchaMu    sync.Mutex
)

// CaptchaResponse holds the captcha ID and question text.
type CaptchaResponse struct {
	ID       string `json:"id"`
	Question string `json:"question"`
}

// GenerateCaptcha creates a math captcha (two-digit op single-digit).
func GenerateCaptcha() *CaptchaResponse {
	captchaMu.Lock()
	defer captchaMu.Unlock()

	now := time.Now()

	// Only clean expired entries when store exceeds threshold to avoid O(n) on every call
	if len(captchaStore) > 1000 {
		for k, v := range captchaStore {
			if now.After(v.expiresAt) {
				delete(captchaStore, k)
			}
		}
		// Force eviction if still too large after expiry cleanup
		if len(captchaStore) > 10000 {
			for k := range captchaStore {
				delete(captchaStore, k)
				if len(captchaStore) <= 5000 {
					break
				}
			}
		}
	}

	a := mrand.Intn(90) + 10 // 10-99
	b := mrand.Intn(9) + 1   // 1-9
	ops := []string{"+", "-", "×"}
	op := ops[mrand.Intn(3)]

	var answer int
	switch op {
	case "+":
		answer = a + b
	case "-":
		answer = a - b
	case "×":
		answer = a * b
	}

	id, _ := generateToken()
	if id == "" {
		id = fmt.Sprintf("cap_%d", now.UnixNano())
	}
	captchaStore[id] = captchaEntry{
		answer:    answer,
		expiresAt: now.Add(5 * time.Minute),
	}

	return &CaptchaResponse{
		ID:       id,
		Question: fmt.Sprintf("%d %s %d = ?", a, op, b),
	}
}

// ValidateCaptcha checks if the answer is correct for the given captcha ID.
func ValidateCaptcha(id string, answer int) bool {
	captchaMu.Lock()
	defer captchaMu.Unlock()

	entry, ok := captchaStore[id]
	if !ok {
		return false
	}
	delete(captchaStore, id) // one-time use
	if time.Now().After(entry.expiresAt) {
		return false
	}
	return entry.answer == answer
}

func generateToken() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// TestEmail sends a test email to verify SMTP configuration.
func (a *App) TestEmail(toEmail string) error {
	toEmail = strings.TrimSpace(toEmail)
	if toEmail == "" {
		return fmt.Errorf("请输入收件人邮箱")
	}
	return a.emailService.SendTest(toEmail)
}

// --- Configuration Interface ---

// MaskedConfig is a copy of Config with API keys replaced by "***".
type MaskedConfig struct {
	Server       config.ServerConfig    `json:"server"`
	LLM          config.LLMConfig       `json:"llm"`
	Embedding    config.EmbeddingConfig `json:"embedding"`
	Vector       config.VectorConfig    `json:"vector"`
	OAuth        MaskedOAuthConfig      `json:"oauth"`
	Admin        config.AdminConfig     `json:"admin"`
	SMTP         config.SMTPConfig      `json:"smtp"`
	ProductIntro string                 `json:"product_intro"`
	ProductName  string                 `json:"product_name"`
	Video        config.VideoConfig     `json:"video"`
	AuthServer   string                 `json:"auth_server"`
}

// MaskedOAuthConfig holds OAuth config with secrets masked.
type MaskedOAuthConfig struct {
	Providers map[string]MaskedOAuthProvider `json:"providers"`
}

// MaskedOAuthProvider holds a single provider config with the secret masked.
type MaskedOAuthProvider struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	AuthURL      string   `json:"auth_url"`
	TokenURL     string   `json:"token_url"`
	RedirectURL  string   `json:"redirect_url"`
	Scopes       []string `json:"scopes"`
}

// GetConfig returns the current configuration with API keys masked.
func (a *App) GetConfig() *MaskedConfig {
	cfg := a.configManager.Get()
	if cfg == nil {
		return nil
	}

	masked := &MaskedConfig{
		Server:       cfg.Server,
		LLM:          cfg.LLM,
		Embedding:    cfg.Embedding,
		Vector:       cfg.Vector,
		Admin:        cfg.Admin,
		SMTP:         cfg.SMTP,
		ProductIntro: cfg.ProductIntro,
		ProductName:  cfg.ProductName,
		Video:        cfg.Video,
		AuthServer:   cfg.AuthServer,
	}

	// Mask API keys
	masked.LLM.APIKey = maskSecret(cfg.LLM.APIKey)
	masked.Embedding.APIKey = maskSecret(cfg.Embedding.APIKey)

	// Mask OAuth secrets
	masked.OAuth.Providers = make(map[string]MaskedOAuthProvider, len(cfg.OAuth.Providers))
	for name, p := range cfg.OAuth.Providers {
		masked.OAuth.Providers[name] = MaskedOAuthProvider{
			ClientID:     p.ClientID,
			ClientSecret: maskSecret(p.ClientSecret),
			AuthURL:      p.AuthURL,
			TokenURL:     p.TokenURL,
			RedirectURL:  p.RedirectURL,
			Scopes:       p.Scopes,
		}
	}

	// Mask admin password hash
	masked.Admin.PasswordHash = maskSecret(cfg.Admin.PasswordHash)

	// Mask SMTP password
	masked.SMTP.Password = maskSecret(cfg.SMTP.Password)

	return masked
}

// UpdateConfig applies partial configuration updates.
func (a *App) UpdateConfig(updates map[string]interface{}) error {
	if err := a.configManager.Update(updates); err != nil {
		return err
	}
	// Refresh services with new config
	cfg := a.configManager.Get()
	es := embedding.NewAPIEmbeddingService(cfg.Embedding.Endpoint, cfg.Embedding.APIKey, cfg.Embedding.ModelName, cfg.Embedding.UseMultimodal)
	ls := llm.NewAPILLMService(cfg.LLM.Endpoint, cfg.LLM.APIKey, cfg.LLM.ModelName, cfg.LLM.Temperature, cfg.LLM.MaxTokens)
	a.queryEngine.UpdateServices(es, ls, cfg)
	a.docManager.UpdateEmbeddingService(es)
	a.pendingManager.UpdateServices(es, ls)

	// Propagate video config to DocumentManager if any video settings changed
	for key := range updates {
		if strings.HasPrefix(key, "video.") {
			a.docManager.SetVideoConfig(cfg.Video)
			break
		}
	}

	// Refresh OAuth client if any OAuth settings changed
	for key := range updates {
		if strings.HasPrefix(key, "oauth.") {
			a.RefreshOAuthClient()
			break
		}
	}
	return nil
}

// maskSecret replaces a non-empty secret with "***".
func maskSecret(s string) string {
	if strings.TrimSpace(s) == "" {
		return ""
	}
	return "***"
}

// --- Admin Sub-Account Management ---

// CreateAdminUser creates a new admin sub-account.
func (a *App) CreateAdminUser(username, password, role string, permissions []string) (*AdminUserInfo, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, fmt.Errorf("用户名和密码不能为空")
	}
	if len(username) < 3 {
		return nil, fmt.Errorf("用户名至少3位")
	}
	if len(username) > 64 {
		return nil, fmt.Errorf("用户名不能超过64位")
	}
	if msg := ValidatePassword(password); msg != "" {
		return nil, errors.New(msg)
	}
	if role != "editor" && role != "super_admin" {
		role = "editor"
	}
	// Reject usernames with special characters
	for _, c := range username {
		if c < 0x20 || c == '"' || c == '\'' || c == '\\' || c == '<' || c == '>' {
			return nil, fmt.Errorf("用户名包含非法字符")
		}
	}

	// Check conflict with super admin
	cfg := a.configManager.Get()
	if username == cfg.Admin.Username {
		return nil, fmt.Errorf("用户名已存在")
	}

	hash, err := auth.HashPassword(password)
	if err != nil {
		return nil, err
	}

	id, err := generateToken()
	if err != nil {
		return nil, err
	}

	// Filter valid permissions
	validPerms := map[string]bool{"batch_import": true}
	var filteredPerms []string
	for _, p := range permissions {
		if validPerms[p] {
			filteredPerms = append(filteredPerms, p)
		}
	}
	permsStr := strings.Join(filteredPerms, ",")

	_, err = a.db.Exec(
		`INSERT INTO admin_users (id, username, password_hash, role, permissions) VALUES (?, ?, ?, ?, ?)`,
		id, username, hash, role, permsStr,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("用户名已存在")
		}
		return nil, fmt.Errorf("创建用户失败: %w", err)
	}

	return &AdminUserInfo{ID: id, Username: username, Role: role, Permissions: filteredPerms}, nil
}

// ListAdminUsers returns all admin sub-accounts.
func (a *App) ListAdminUsers() ([]AdminUserInfo, error) {
	rows, err := a.readDB.Query(`SELECT id, username, role, created_at, COALESCE(permissions,'') FROM admin_users ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []AdminUserInfo
	for rows.Next() {
		var u AdminUserInfo
		var createdAt sql.NullTime
		var permsStr string
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &createdAt, &permsStr); err != nil {
			return nil, err
		}
		if createdAt.Valid {
			u.CreatedAt = createdAt.Time.Format("2006-01-02 15:04:05")
		}
		if permsStr != "" {
			u.Permissions = strings.Split(permsStr, ",")
		}
		users = append(users, u)
	}

	// Fetch product names for all admin users in a single query (avoid N+1)
	if len(users) > 0 {
		pRows, err := a.readDB.Query(
			`SELECT aup.admin_user_id, p.name FROM products p
			 INNER JOIN admin_user_products aup ON p.id = aup.product_id
			 ORDER BY p.name`)
		if err == nil {
			productMap := make(map[string][]string)
			for pRows.Next() {
				var adminID, name string
				if err := pRows.Scan(&adminID, &name); err == nil {
					productMap[adminID] = append(productMap[adminID], name)
				}
			}
			pRows.Close()
			for i := range users {
				users[i].ProductNames = productMap[users[i].ID]
			}
		}
	}

	return users, nil
}

// DeleteAdminUser removes an admin sub-account and cleans up associated sessions.
func (a *App) DeleteAdminUser(id string) error {
	// Clean up sessions for this admin user
	_, _ = a.db.Exec(`DELETE FROM sessions WHERE user_id = ?`, "admin_"+id)
	// Clean up product assignments
	_, _ = a.db.Exec(`DELETE FROM admin_user_products WHERE admin_user_id = ?`, id)
	// Delete the admin user record
	_, err := a.db.Exec(`DELETE FROM admin_users WHERE id = ?`, id)
	return err
}

// --- Knowledge Entry (直接录入图文) ---

// KnowledgeEntryRequest represents a direct knowledge entry from admin.
type KnowledgeEntryRequest struct {
	Title     string   `json:"title"`
	Content   string   `json:"content"`
	ImageURLs []string `json:"image_urls,omitempty"`
	VideoURLs []string `json:"video_urls,omitempty"`
	ProductID string   `json:"product_id"`
}

// AddKnowledgeEntry stores a text+image knowledge entry into the vector store.
func (a *App) AddKnowledgeEntry(req KnowledgeEntryRequest) error {
	title := strings.TrimSpace(req.Title)
	content := strings.TrimSpace(req.Content)
	if title == "" || content == "" {
		return fmt.Errorf("标题和内容不能为空")
	}
	if len(title) > 500 {
		return fmt.Errorf("标题过长（最多500字符）")
	}
	if len(content) > 100000 {
		return fmt.Errorf("内容过长（最多100000字符）")
	}
	if len(req.ImageURLs) > 50 {
		return fmt.Errorf("图片数量过多（最多50张）")
	}
	if len(req.VideoURLs) > 10 {
		return fmt.Errorf("视频数量过多（最多10个）")
	}

	// Validate image URLs (must be local paths or HTTPS)
	for _, imgURL := range req.ImageURLs {
		imgURL = strings.TrimSpace(imgURL)
		if imgURL == "" {
			continue
		}
		if !strings.HasPrefix(imgURL, "/api/") && !strings.HasPrefix(imgURL, "data:image/") {
			return fmt.Errorf("图片URL格式不正确")
		}
	}
	// Validate video URLs (must be local paths)
	for _, vidURL := range req.VideoURLs {
		vidURL = strings.TrimSpace(vidURL)
		if vidURL == "" {
			continue
		}
		if !strings.HasPrefix(vidURL, "/api/") {
			return fmt.Errorf("视频URL格式不正确")
		}
	}

	docID, err := generateToken()
	if err != nil {
		return err
	}
	docName := "知识录入: " + title

	// Insert document record
	_, err = a.db.Exec(
		`INSERT INTO documents (id, name, type, status, product_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		docID, docName, "knowledge", "success", req.ProductID, time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("创建文档记录失败: %w", err)
	}

	// Embed and store text content
	if err := a.docManager.ChunkEmbedStore(docID, docName, content, req.ProductID); err != nil {
		return fmt.Errorf("存储文本失败: %w", err)
	}

	// Store image references — always create text-searchable chunks with image URLs
	if len(req.ImageURLs) > 0 {
		es := a.docManager.GetEmbeddingService()
		// Embed the text once and reuse for all images (same text → same embedding)
		imgText := fmt.Sprintf("[图片: %s] %s", title, content)
		imgVec, imgEmbErr := es.Embed(imgText)
		if imgEmbErr != nil {
			log.Printf("Warning: failed to embed image text: %v", imgEmbErr)
		} else {
			for i, imgURL := range req.ImageURLs {
				imgURL = strings.TrimSpace(imgURL)
				if imgURL == "" {
					continue
				}
				// Copy the vector to avoid shared slice mutation
				vecCopy := make([]float64, len(imgVec))
				copy(vecCopy, imgVec)
				imgChunk := []vectorstore.VectorChunk{{
					ChunkText:    fmt.Sprintf("[图片: %s]", title),
					ChunkIndex:   1000 + i,
					DocumentID:   docID,
					DocumentName: docName,
					Vector:       vecCopy,
					ImageURL:     imgURL,
					ProductID:    req.ProductID,
				}}
				if err := a.docManager.StoreChunks(docID, imgChunk); err != nil {
					log.Printf("Warning: failed to store image chunk %d: %v", i, err)
				}
			}
		}

		// Additionally, if multimodal embedding is available, also store image-embedded vectors
		cfg := a.configManager.Get()
		if cfg.Embedding.UseMultimodal {
			for i, imgURL := range req.ImageURLs {
				imgURL = strings.TrimSpace(imgURL)
				if imgURL == "" {
					continue
				}
				vec, err := es.EmbedImageURL(imgURL)
				if err != nil {
					log.Printf("Warning: failed to embed image %d multimodal: %v", i, err)
					continue
				}
				imgChunk := []vectorstore.VectorChunk{{
					ChunkText:    fmt.Sprintf("[图片: %s]", title),
					ChunkIndex:   2000 + i,
					DocumentID:   docID,
					DocumentName: docName,
					Vector:       vec,
					ImageURL:     imgURL,
					ProductID:    req.ProductID,
				}}
				if err := a.docManager.StoreChunks(docID, imgChunk); err != nil {
					log.Printf("Warning: failed to store multimodal image vector %d: %v", i, err)
				}
			}
		}
	}

	// Process video files - extract keyframes and transcripts using existing pipeline
	if len(req.VideoURLs) > 0 {
		for _, videoURL := range req.VideoURLs {
			videoURL = strings.TrimSpace(videoURL)
			if videoURL == "" {
				continue
			}

			// Extract file path from URL (e.g., "/api/videos/knowledge/uuid.mp4" -> "./data/videos/knowledge/uuid.mp4")
			videoPath := strings.TrimPrefix(videoURL, "/api/videos/knowledge/")
			if videoPath == videoURL {
				log.Printf("Warning: invalid video URL format: %s", videoURL)
				continue
			}
			fullPath := filepath.Join(".", "data", "videos", "knowledge", videoPath)

			// Read video file data
			videoData, err := os.ReadFile(fullPath)
			if err != nil {
				log.Printf("Warning: failed to read video file %s: %v", fullPath, err)
				continue
			}

			// Call processVideo to extract keyframes + transcripts
			// This will create chunks associated with this knowledge entry docID
			if err := a.docManager.ProcessVideoForKnowledge(docID, docName, videoData, videoURL, req.ProductID); err != nil {
				log.Printf("Warning: failed to process video %s: %v", videoPath, err)
				// Continue with other videos even if one fails
			}
		}
	}

	return nil
}

// --- Product Management ---

// CreateProduct creates a new product with the given name, type, description, and welcome message.
func (a *App) CreateProduct(name, productType, description, welcomeMessage string, allowDownload bool) (*product.Product, error) {
	return a.productService.Create(name, productType, description, welcomeMessage, allowDownload)
}

// UpdateProduct updates an existing product's name, type, description, and welcome message.
func (a *App) UpdateProduct(id, name, productType, description, welcomeMessage string, allowDownload bool) (*product.Product, error) {
	return a.productService.Update(id, name, productType, description, welcomeMessage, allowDownload)
}

// DeleteProduct removes a product by ID.
func (a *App) DeleteProduct(id string) error {
	return a.productService.Delete(id)
}

// GetProduct retrieves a product by ID.
func (a *App) GetProduct(id string) (*product.Product, error) {
	return a.productService.GetByID(id)
}

// ListProducts returns all products.
func (a *App) ListProducts() ([]product.Product, error) {
	return a.productService.List()
}

// GetFirstProductID returns the ID of the first product, or empty string if none exist.
// More efficient than ListProducts() when only the default product ID is needed.
func (a *App) GetFirstProductID() (string, error) {
	return a.productService.GetFirstID()
}

// HasProductDocumentsOrKnowledge checks whether a product has associated documents or knowledge entries.
func (a *App) HasProductDocumentsOrKnowledge(productID string) (bool, error) {
	return a.productService.HasDocumentsOrKnowledge(productID)
}

// GetProductsByAdminUserID returns the products assigned to the given admin user.
// If the admin user has zero assigned products, all products are returned.
// The session stores userID as "admin_<id>" for sub-admins and "admin" for super admin.
// We strip the "admin_" prefix to get the actual admin_users.id for the DB lookup.
func (a *App) GetProductsByAdminUserID(adminUserID string) ([]product.Product, error) {
	// Super admin ("admin") has access to all products
	if adminUserID == "admin" {
		return a.productService.List()
	}
	// Sub-admin session stores "admin_<actual_id>", strip prefix for DB lookup
	actualID := strings.TrimPrefix(adminUserID, "admin_")
	return a.productService.GetByAdminUserID(actualID)
}

// AssignProductsToAdminUser assigns the given product IDs to an admin user,
// replacing any previous assignments.
func (a *App) AssignProductsToAdminUser(adminUserID string, productIDs []string) error {
	return a.productService.AssignAdminUser(adminUserID, productIDs)
}

// --- User Preferences ---

// GetUserDefaultProduct returns the default product ID for a user.
func (a *App) GetUserDefaultProduct(userID string) (string, error) {
	var defaultProductID string
	err := a.readDB.QueryRow(`SELECT COALESCE(default_product_id, '') FROM users WHERE id = ?`, userID).Scan(&defaultProductID)
	if err != nil {
		// Column may not exist yet; return empty gracefully
		return "", nil
	}
	return defaultProductID, nil
}

// SetUserDefaultProduct sets the default product ID for a user.
func (a *App) SetUserDefaultProduct(userID, productID string) error {
	_, err := a.db.Exec(`UPDATE users SET default_product_id = ? WHERE id = ?`, productID, userID)
	return err
}

// --- Customer Management ---

// CustomerUserInfo holds detailed info about a regular user for admin management.
type CustomerUserInfo struct {
	ID            string `json:"id"`
	Email         string `json:"email"`
	Name          string `json:"name"`
	Provider      string `json:"provider"`
	EmailVerified bool   `json:"email_verified"`
	CreatedAt     string `json:"created_at"`
	LastLogin     string `json:"last_login,omitempty"`
	IsBanned      bool   `json:"is_banned"`
	BanReason     string `json:"ban_reason,omitempty"`
	BanUnlocksAt  string `json:"ban_unlocks_at,omitempty"`
}

// CustomerListResult holds paginated customer list with stats.
type CustomerListResult struct {
	Customers   []CustomerUserInfo `json:"customers"`
	Total       int                `json:"total"`
	BannedCount int                `json:"banned_count"`
	Page        int                `json:"page"`
	PageSize    int                `json:"page_size"`
}

// ListCustomers returns all regular users with their status.
func (a *App) ListCustomers() ([]CustomerUserInfo, error) {
	result, err := a.ListCustomersPaged(1, 999999, "")
	if err != nil {
		return nil, err
	}
	return result.Customers, nil
}

// ListCustomersPaged returns paginated customers with stats and optional email search.
func (a *App) ListCustomersPaged(page, pageSize int, search string) (*CustomerListResult, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 200 {
		pageSize = 20
	}

	// Build WHERE clause (use table-qualified column names for JOIN compatibility)
	baseWhere := `provider != 'admin_sub' AND id != 'admin'`
	// For JOIN queries, we need table-qualified names to avoid ambiguity with login_bans.id
	joinWhere := `u.provider != 'admin_sub' AND u.id != 'admin'`
	var args []interface{}
	if search != "" {
		baseWhere += ` AND COALESCE(email, '') LIKE ?`
		joinWhere += ` AND COALESCE(u.email, '') LIKE ?`
		args = append(args, "%"+search+"%")
	}

	// Get total count
	var total int
	err := a.readDB.QueryRow(`SELECT COUNT(*) FROM users WHERE `+baseWhere, args...).Scan(&total)
	if err != nil {
		return nil, fmt.Errorf("count customers: %w", err)
	}

	// Get paginated rows with ban info via LEFT JOIN (eliminates N+1 query)
	now := time.Now().UTC().Format(time.RFC3339)
	offset := (page - 1) * pageSize
	queryArgs := append(args, now, pageSize, offset)
	rows, err := a.readDB.Query(`
		SELECT u.id, COALESCE(u.email, ''), COALESCE(u.name, ''), u.provider, u.email_verified, u.created_at, u.last_login,
			COALESCE(b.reason, ''), COALESCE(b.unlocks_at, '')
		FROM users u
		LEFT JOIN login_bans b ON (b.username = COALESCE(u.email, '') OR b.username = u.id) AND b.unlocks_at > ?
		WHERE `+joinWhere+`
		ORDER BY u.created_at DESC
		LIMIT ? OFFSET ?
	`, queryArgs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var customers []CustomerUserInfo
	bannedCount := 0
	for rows.Next() {
		var c CustomerUserInfo
		var emailVerified int
		var createdAt, lastLogin sql.NullString
		var banReason, banUnlocksAt string
		if err := rows.Scan(&c.ID, &c.Email, &c.Name, &c.Provider, &emailVerified, &createdAt, &lastLogin, &banReason, &banUnlocksAt); err != nil {
			return nil, err
		}
		c.EmailVerified = emailVerified == 1
		if createdAt.Valid && createdAt.String != "" {
			c.CreatedAt = createdAt.String
		}
		if lastLogin.Valid && lastLogin.String != "" {
			c.LastLogin = lastLogin.String
		}
		if banReason != "" || banUnlocksAt != "" {
			c.IsBanned = true
			c.BanReason = banReason
			c.BanUnlocksAt = banUnlocksAt
		}

		customers = append(customers, c)
	}

	// Get global banned count (across all customers, not just current page)
	var globalBanned int
	err = a.readDB.QueryRow(`
		SELECT COUNT(DISTINCT u.id) FROM users u
		INNER JOIN login_bans b ON (b.username = COALESCE(u.email, '') OR b.username = u.id)
		WHERE u.provider != 'admin_sub' AND u.id != 'admin' AND b.unlocks_at > ?
	`, now).Scan(&globalBanned)
	if err == nil {
		bannedCount = globalBanned
	}

	return &CustomerListResult{
		Customers:   customers,
		Total:       total,
		BannedCount: bannedCount,
		Page:        page,
		PageSize:    pageSize,
	}, nil
}

// VerifyCustomerEmail manually marks a user's email as verified.
func (a *App) VerifyCustomerEmail(userID string) error {
	_, err := a.db.Exec(`UPDATE users SET email_verified = 1 WHERE id = ?`, userID)
	return err
}

// DeleteCustomer removes a user account and their sessions.
func (a *App) DeleteCustomer(userID string) error {
	tx, err := a.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Delete tokens and sessions first
	_, _ = tx.Exec(`DELETE FROM email_tokens WHERE user_id = ?`, userID)
	_, _ = tx.Exec(`DELETE FROM sessions WHERE user_id = ?`, userID)
	// Delete user record
	_, err = tx.Exec(`DELETE FROM users WHERE id = ?`, userID)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// BanCustomer manually bans a user's email or ID.
func (a *App) BanCustomer(email string, reason string, days int) error {
	if email == "" {
		return fmt.Errorf("email is required")
	}
	if days <= 0 {
		days = 3650 // Default to ~10 years
	}
	a.loginLimiter.AddManualBan(email, "", reason, time.Duration(days)*24*time.Hour)
	return nil
}

// UnbanCustomer removes any manual bans for a user's email.
func (a *App) UnbanCustomer(email string) error {
	if email == "" {
		return fmt.Errorf("email is required")
	}
	a.loginLimiter.Unban(email, "")
	return nil
}

// SNLoginRequest is the request body for POST /api/auth/sn-login.
type SNLoginRequest struct {
	Token string `json:"token"`
}

// SNLoginResponse is the response for POST /api/auth/sn-login.
type SNLoginResponse struct {
	Success     bool   `json:"success"`
	LoginTicket string `json:"login_ticket,omitempty"`
	Message     string `json:"message,omitempty"`
}

// HandleSNLogin verifies a token with the license server, finds or creates the SN user,
// and returns a one-time login ticket.
func (a *App) HandleSNLogin(token string) (*SNLoginResponse, int, error) {
	if token == "" {
		return &SNLoginResponse{Success: false, Message: "token is required"}, 400, nil
	}

	cfg := a.configManager.Get()
	authServer := cfg.AuthServer
	if authServer == "" {
		return &SNLoginResponse{Success: false, Message: "auth server not configured"}, 500, nil
	}

	// Verify token with license server
	verifyURL := fmt.Sprintf("https://%s/api/marketplace-verify", authServer)
	// Use json.Marshal to safely encode the token, preventing JSON injection
	tokenJSON, err := json.Marshal(map[string]string{"token": token})
	if err != nil {
		return &SNLoginResponse{Success: false, Message: "internal error"}, 500, nil
	}

	client := &httpClient{Timeout: 10 * time.Second}
	resp, err := client.Post(verifyURL, "application/json", strings.NewReader(string(tokenJSON)))
	if err != nil {
		log.Printf("[SNLogin] failed to contact license server: %v", err)
		return &SNLoginResponse{Success: false, Message: "failed to contact license server"}, 502, nil
	}
	defer resp.Body.Close()

	var verifyResp struct {
		Success bool   `json:"success"`
		SN      string `json:"sn"`
		Email   string `json:"email"`
		Message string `json:"message"`
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return &SNLoginResponse{Success: false, Message: "internal error"}, 500, nil
	}
	if err := json.Unmarshal(body, &verifyResp); err != nil {
		return &SNLoginResponse{Success: false, Message: "internal error"}, 500, nil
	}

	if !verifyResp.Success {
		msg := "license authentication failed"
		if verifyResp.Message != "" {
			msg += ": " + verifyResp.Message
		}
		return &SNLoginResponse{Success: false, Message: msg}, 401, nil
	}

	email := verifyResp.Email
	sn := verifyResp.SN
	if email == "" {
		return &SNLoginResponse{Success: false, Message: "internal error"}, 500, nil
	}

	// Find or create SN user (use writeDB for the read to avoid TOCTOU race
	// where two concurrent logins for the same email both see ErrNoRows)
	var userID int64
	err = a.db.QueryRow("SELECT id FROM sn_users WHERE email = ?", email).Scan(&userID)
	if err == sql.ErrNoRows {
		// Create new user
		displayName := email
		if idx := strings.Index(email, "@"); idx > 0 {
			displayName = email[:idx]
		}
		result, err := a.db.Exec(
			"INSERT INTO sn_users (email, display_name, sn, last_login_at, created_at) VALUES (?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)",
			email, displayName, sn,
		)
		if err != nil {
			log.Printf("[SNLogin] create user error: %v", err)
			return &SNLoginResponse{Success: false, Message: "internal error"}, 500, nil
		}
		userID, _ = result.LastInsertId()
	} else if err != nil {
		log.Printf("[SNLogin] query user error: %v", err)
		return &SNLoginResponse{Success: false, Message: "internal error"}, 500, nil
	} else {
		// Update last login and SN
		if _, err := a.db.Exec("UPDATE sn_users SET last_login_at = CURRENT_TIMESTAMP, sn = ? WHERE id = ?", sn, userID); err != nil {
			log.Printf("[SNLogin] failed to update last login: %v", err)
		}
	}

	// Generate one-time login ticket (UUID-like)
	ticketBytes := make([]byte, 16)
	if _, err := rand.Read(ticketBytes); err != nil {
		return &SNLoginResponse{Success: false, Message: "internal error"}, 500, nil
	}
	ticket := fmt.Sprintf("%x-%x-%x-%x-%x",
		ticketBytes[0:4], ticketBytes[4:6], ticketBytes[6:8], ticketBytes[8:10], ticketBytes[10:16])

	expiresAt := time.Now().UTC().Add(5 * time.Minute)
	_, err = a.db.Exec(
		"INSERT INTO login_tickets (ticket, user_id, used, created_at, expires_at) VALUES (?, ?, 0, CURRENT_TIMESTAMP, ?)",
		ticket, userID, expiresAt.Format(time.RFC3339),
	)
	if err != nil {
		log.Printf("[SNLogin] create ticket error: %v", err)
		return &SNLoginResponse{Success: false, Message: "internal error"}, 500, nil
	}

	return &SNLoginResponse{Success: true, LoginTicket: ticket}, 200, nil
}

// ValidateLoginTicket validates a one-time login ticket and returns the associated user info.
// On success, it marks the ticket as used and creates a session.
func (a *App) ValidateLoginTicket(ticket string) (sessionID string, err error) {
	if ticket == "" {
		return "", fmt.Errorf("invalid_ticket")
	}

	var userID int64
	var used int
	var expiresAtStr string

	// Read ticket from writeDB (not readDB) to prevent TOCTOU race:
	// two concurrent requests could both read used=0 from the read pool
	// before either marks it used. Using writeDB serializes access.
	err = a.db.QueryRow(
		"SELECT user_id, used, expires_at FROM login_tickets WHERE ticket = ?", ticket,
	).Scan(&userID, &used, &expiresAtStr)
	if err == sql.ErrNoRows {
		return "", fmt.Errorf("invalid_ticket")
	}
	if err != nil {
		return "", fmt.Errorf("internal_error")
	}

	if used != 0 {
		return "", fmt.Errorf("ticket_already_used")
	}

	expiresAt, err := time.Parse(time.RFC3339, expiresAtStr)
	if err != nil {
		expiresAt, _ = time.Parse("2006-01-02T15:04:05Z", expiresAtStr)
	}
	if time.Now().UTC().After(expiresAt) {
		return "", fmt.Errorf("ticket_expired")
	}

	// Atomically mark ticket as used — WHERE used = 0 ensures only one request succeeds
	result, err := a.db.Exec("UPDATE login_tickets SET used = 1 WHERE ticket = ? AND used = 0", ticket)
	if err != nil {
		log.Printf("[ValidateLoginTicket] failed to mark ticket as used: %v", err)
		return "", fmt.Errorf("internal_error")
	}
	rowsAffected, _ := result.RowsAffected()
	if rowsAffected == 0 {
		// Another concurrent request already consumed this ticket
		return "", fmt.Errorf("ticket_already_used")
	}

	// Find the SN user
	var email, displayName string
	err = a.readDB.QueryRow("SELECT email, display_name FROM sn_users WHERE id = ?", userID).Scan(&email, &displayName)
	if err != nil {
		return "", fmt.Errorf("internal_error")
	}

	// Find or create a regular user entry for session management
	// Use writeDB for the read to avoid TOCTOU race with concurrent ticket validations
	var regularUserID string
	err = a.db.QueryRow("SELECT id FROM users WHERE email = ? AND provider = 'sn'", email).Scan(&regularUserID)
	if err == sql.ErrNoRows {
		regularUserID = hex.EncodeToString(func() []byte { b := make([]byte, 16); rand.Read(b); return b }())
		_, err = a.db.Exec(
			"INSERT INTO users (id, email, name, provider, provider_id, email_verified, created_at, last_login) VALUES (?, ?, ?, 'sn', ?, 1, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)",
			regularUserID, email, displayName, email,
		)
		if err != nil {
			log.Printf("[TicketLogin] create user error: %v", err)
			return "", fmt.Errorf("internal_error")
		}
	} else if err != nil {
		return "", fmt.Errorf("internal_error")
	} else {
		a.db.Exec("UPDATE users SET last_login = CURRENT_TIMESTAMP WHERE id = ?", regularUserID)
	}

	// Create session
	session, err := a.sessionManager.CreateSession(regularUserID)
	if err != nil {
		return "", fmt.Errorf("internal_error")
	}

	return session.ID, nil
}
