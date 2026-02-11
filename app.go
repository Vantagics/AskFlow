// Package main provides the App struct that serves as the API facade
// for the helpdesk system, delegating to internal service components.
package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	mrand "math/rand"
	"strings"
	"sync"
	"time"

	"helpdesk/internal/auth"
	"helpdesk/internal/config"
	"helpdesk/internal/document"
	"helpdesk/internal/email"
	"helpdesk/internal/embedding"
	"helpdesk/internal/llm"
	"helpdesk/internal/pending"
	"helpdesk/internal/query"
	"helpdesk/internal/vectorstore"
)

// App is the API facade that binds all backend services for the frontend.
// Each public method delegates to the appropriate service component.
type App struct {
	db             *sql.DB
	queryEngine    *query.QueryEngine
	docManager     *document.DocumentManager
	pendingManager *pending.PendingQuestionManager
	oauthClient    *auth.OAuthClient
	sessionManager *auth.SessionManager
	configManager  *config.ConfigManager
	emailService   *email.Service
}

// NewApp creates a new App with all service dependencies injected.
func NewApp(
	db *sql.DB,
	qe *query.QueryEngine,
	dm *document.DocumentManager,
	pm *pending.PendingQuestionManager,
	oc *auth.OAuthClient,
	sm *auth.SessionManager,
	cm *config.ConfigManager,
	es *email.Service,
) *App {
	return &App{
		db:             db,
		queryEngine:    qe,
		docManager:     dm,
		pendingManager: pm,
		oauthClient:    oc,
		sessionManager: sm,
		configManager:  cm,
		emailService:   es,
	}
}

// --- Query Interface ---

// Query processes a user question through the RAG pipeline.
func (a *App) Query(question string) (*query.QueryResponse, error) {
	return a.queryEngine.Query(query.QueryRequest{
		Question: question,
		UserID:   "", // UserID can be set by the caller via session context
	})
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

// ListDocuments returns all uploaded documents.
func (a *App) ListDocuments() ([]document.DocumentInfo, error) {
	return a.docManager.ListDocuments()
}

// DeleteDocument removes a document and its associated vectors.
func (a *App) DeleteDocument(docID string) error {
	return a.docManager.DeleteDocument(docID)
}

// --- Pending Questions Interface ---

// ListPendingQuestions returns pending questions filtered by status.
// Pass an empty string to list all questions.
func (a *App) ListPendingQuestions(status string) ([]pending.PendingQuestion, error) {
	return a.pendingManager.ListPending(status)
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
func (a *App) CreatePendingQuestion(question, userID, imageData string) (*pending.PendingQuestion, error) {
	return a.pendingManager.CreatePending(question, userID, imageData)
}

// --- Authentication Interface ---

// GetOAuthURL returns the OAuth authorization URL for the given provider.
func (a *App) GetOAuthURL(provider string) (string, error) {
	return a.oauthClient.GetAuthURL(provider)
}

// OAuthCallbackResponse contains the result of an OAuth callback.
type OAuthCallbackResponse struct {
	User    *auth.OAuthUser `json:"user"`
	Session *auth.Session   `json:"session"`
}

// HandleOAuthCallback exchanges the auth code for user info and creates a session.
func (a *App) HandleOAuthCallback(provider, code string) (*OAuthCallbackResponse, error) {
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
	a.configManager.DeleteOAuthProvider(provider)
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
	ID        string `json:"id"`
	Username  string `json:"username"`
	Role      string `json:"role"`
	CreatedAt string `json:"created_at,omitempty"`
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
	if strings.TrimSpace(username) == "" || strings.TrimSpace(password) == "" {
		return nil, fmt.Errorf("用户名和密码不能为空")
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
		err := a.db.QueryRow(`SELECT role FROM admin_users WHERE id = ?`, subID).Scan(&role)
		if err == nil {
			return role
		}
	}
	return ""
}

// IsAdminSession checks if a user ID belongs to any admin (super or sub).
func (a *App) IsAdminSession(userID string) bool {
	return userID == "admin" || strings.HasPrefix(userID, "admin_")
}

// AdminLogin verifies the admin username and password and creates a session.
// Checks the super admin first, then admin sub-accounts.
func (a *App) AdminLogin(username, password string) (*AdminLoginResponse, error) {
	cfg := a.configManager.Get()

	// Check super admin
	if cfg.Admin.Username != "" && cfg.Admin.PasswordHash != "" && username == cfg.Admin.Username {
		if err := auth.VerifyAdminPassword(password, cfg.Admin.PasswordHash); err != nil {
			return nil, fmt.Errorf("用户名或密码错误")
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

	// Check admin sub-accounts
	var id, passwordHash, role string
	err := a.db.QueryRow(
		`SELECT id, password_hash, role FROM admin_users WHERE username = ?`, username,
	).Scan(&id, &passwordHash, &role)
	if err != nil {
		return nil, fmt.Errorf("用户名或密码错误")
	}
	if err := auth.VerifyAdminPassword(password, passwordHash); err != nil {
		return nil, fmt.Errorf("用户名或密码错误")
	}

	// Ensure user record exists for FK
	a.db.Exec(
		`INSERT OR IGNORE INTO users (id, email, name, provider, provider_id) VALUES (?, ?, ?, ?, ?)`,
		"admin_"+id, "", username, "admin_sub", id,
	)

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
		"admin", "", "管理员", "local", "admin",
	)
	if err != nil {
		return fmt.Errorf("ensure admin user: %w", err)
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
	if len(password) < 6 {
		return fmt.Errorf("密码至少6位")
	}
	if name == "" {
		name = email
	}

	// Check if email already exists
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

	// Send verification email
	verifyURL := strings.TrimRight(baseURL, "/") + "/verify?token=" + token
	if err := a.emailService.SendVerification(email, name, verifyURL); err != nil {
		return fmt.Errorf("发送验证邮件失败: %w", err)
	}

	return nil
}

// VerifyEmail verifies a user's email using the token.
func (a *App) VerifyEmail(token string) error {
	token = strings.TrimSpace(token)
	if token == "" {
		return fmt.Errorf("无效的验证链接")
	}

	var userID, expiresAtStr string
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
	ID       string `json:"id"`
	Email    string `json:"email"`
	Name     string `json:"name"`
	Provider string `json:"provider"`
}

// UserLogin authenticates a user with email and password.
func (a *App) UserLogin(email, password string) (*UserLoginResponse, error) {
	email = strings.TrimSpace(email)
	if email == "" || password == "" {
		return nil, fmt.Errorf("邮箱和密码不能为空")
	}

	var userID, name, passwordHash string
	var emailVerified int
	err := a.db.QueryRow(
		`SELECT id, name, password_hash, email_verified FROM users WHERE email = ? AND provider = 'local'`,
		email,
	).Scan(&userID, &name, &passwordHash, &emailVerified)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("邮箱或密码错误")
	}
	if err != nil {
		return nil, fmt.Errorf("查询用户失败: %w", err)
	}

	if emailVerified == 0 {
		return nil, fmt.Errorf("邮箱未验证，请先查收验证邮件")
	}

	if err := auth.VerifyAdminPassword(password, passwordHash); err != nil {
		return nil, fmt.Errorf("邮箱或密码错误")
	}

	// Update last login
	a.db.Exec(`UPDATE users SET last_login = ? WHERE id = ?`, time.Now().UTC().Format(time.RFC3339), userID)

	session, err := a.sessionManager.CreateSession(userID)
	if err != nil {
		return nil, err
	}

	return &UserLoginResponse{
		Session: session,
		User: &UserInfo{
			ID:       userID,
			Email:    email,
			Name:     name,
			Provider: "local",
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

	// Clean expired entries
	now := time.Now()
	for k, v := range captchaStore {
		if now.After(v.expiresAt) {
			delete(captchaStore, k)
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
func (a *App) CreateAdminUser(username, password, role string) (*AdminUserInfo, error) {
	username = strings.TrimSpace(username)
	if username == "" || password == "" {
		return nil, fmt.Errorf("用户名和密码不能为空")
	}
	if len(password) < 6 {
		return nil, fmt.Errorf("密码至少6位")
	}
	if role != "editor" && role != "super_admin" {
		role = "editor"
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

	_, err = a.db.Exec(
		`INSERT INTO admin_users (id, username, password_hash, role) VALUES (?, ?, ?, ?)`,
		id, username, hash, role,
	)
	if err != nil {
		if strings.Contains(err.Error(), "UNIQUE") {
			return nil, fmt.Errorf("用户名已存在")
		}
		return nil, fmt.Errorf("创建用户失败: %w", err)
	}

	return &AdminUserInfo{ID: id, Username: username, Role: role}, nil
}

// ListAdminUsers returns all admin sub-accounts.
func (a *App) ListAdminUsers() ([]AdminUserInfo, error) {
	rows, err := a.db.Query(`SELECT id, username, role, created_at FROM admin_users ORDER BY created_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var users []AdminUserInfo
	for rows.Next() {
		var u AdminUserInfo
		var createdAt sql.NullTime
		if err := rows.Scan(&u.ID, &u.Username, &u.Role, &createdAt); err != nil {
			return nil, err
		}
		if createdAt.Valid {
			u.CreatedAt = createdAt.Time.Format("2006-01-02 15:04:05")
		}
		users = append(users, u)
	}
	return users, nil
}

// DeleteAdminUser removes an admin sub-account.
func (a *App) DeleteAdminUser(id string) error {
	_, err := a.db.Exec(`DELETE FROM admin_users WHERE id = ?`, id)
	return err
}

// --- Knowledge Entry (直接录入图文) ---

// KnowledgeEntryRequest represents a direct knowledge entry from admin.
type KnowledgeEntryRequest struct {
	Title    string   `json:"title"`
	Content  string   `json:"content"`
	ImageURLs []string `json:"image_urls,omitempty"`
}

// AddKnowledgeEntry stores a text+image knowledge entry into the vector store.
func (a *App) AddKnowledgeEntry(req KnowledgeEntryRequest) error {
	title := strings.TrimSpace(req.Title)
	content := strings.TrimSpace(req.Content)
	if title == "" || content == "" {
		return fmt.Errorf("标题和内容不能为空")
	}

	docID, err := generateToken()
	if err != nil {
		return err
	}
	docName := "知识录入: " + title

	// Insert document record
	_, err = a.db.Exec(
		`INSERT INTO documents (id, name, type, status, created_at) VALUES (?, ?, ?, ?, ?)`,
		docID, docName, "knowledge", "success", time.Now().UTC(),
	)
	if err != nil {
		return fmt.Errorf("创建文档记录失败: %w", err)
	}

	// Embed and store text content
	if err := a.docManager.ChunkEmbedStore(docID, docName, content); err != nil {
		return fmt.Errorf("存储文本失败: %w", err)
	}

	// Store image references — always create text-searchable chunks with image URLs
	if len(req.ImageURLs) > 0 {
		es := a.docManager.GetEmbeddingService()
		for i, imgURL := range req.ImageURLs {
			imgURL = strings.TrimSpace(imgURL)
			if imgURL == "" {
				continue
			}
			// Create a text-embedded chunk that carries the image URL
			// so text-based search can find and return the image
			imgText := fmt.Sprintf("[图片: %s] %s", title, content)
			vec, err := es.Embed(imgText)
			if err != nil {
				fmt.Printf("Warning: failed to embed image text %d: %v\n", i, err)
				continue
			}
			imgChunk := []vectorstore.VectorChunk{{
				ChunkText:    fmt.Sprintf("[图片: %s]", title),
				ChunkIndex:   1000 + i,
				DocumentID:   docID,
				DocumentName: docName,
				Vector:       vec,
				ImageURL:     imgURL,
			}}
			if err := a.docManager.StoreChunks(docID, imgChunk); err != nil {
				fmt.Printf("Warning: failed to store image chunk %d: %v\n", i, err)
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
					fmt.Printf("Warning: failed to embed image %d multimodal: %v\n", i, err)
					continue
				}
				imgChunk := []vectorstore.VectorChunk{{
					ChunkText:    fmt.Sprintf("[图片: %s]", title),
					ChunkIndex:   2000 + i,
					DocumentID:   docID,
					DocumentName: docName,
					Vector:       vec,
					ImageURL:     imgURL,
				}}
				if err := a.docManager.StoreChunks(docID, imgChunk); err != nil {
					fmt.Printf("Warning: failed to store multimodal image vector %d: %v\n", i, err)
				}
			}
		}
	}

	return nil
}
