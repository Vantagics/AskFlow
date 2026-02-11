package main

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"helpdesk/internal/auth"
	"helpdesk/internal/chunker"
	"helpdesk/internal/config"
	"helpdesk/internal/db"
	"helpdesk/internal/document"
	"helpdesk/internal/email"
	"helpdesk/internal/embedding"
	"helpdesk/internal/llm"
	"helpdesk/internal/parser"
	"helpdesk/internal/pending"
	"helpdesk/internal/query"
	"helpdesk/internal/vectorstore"
)

func main() {
	// Ensure data directory exists
	if err := os.MkdirAll("./data", 0755); err != nil {
		log.Fatalf("Failed to create data directory: %v", err)
	}

	// 1. Initialize ConfigManager and load config
	configPath := "./data/config.json"
	cm, err := config.NewConfigManager(configPath)
	if err != nil {
		log.Fatalf("Failed to create config manager: %v", err)
	}
	if err := cm.Load(); err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	cfg := cm.Get()

	// 2. Initialize database
	database, err := db.InitDB(cfg.Vector.DBPath)
	if err != nil {
		log.Fatalf("Failed to initialize database: %v", err)
	}
	defer database.Close()

	// 3. Create service instances
	vs := vectorstore.NewSQLiteVectorStore(database)
	tc := &chunker.TextChunker{ChunkSize: cfg.Vector.ChunkSize, Overlap: cfg.Vector.Overlap}
	dp := &parser.DocumentParser{}
	es := embedding.NewAPIEmbeddingService(cfg.Embedding.Endpoint, cfg.Embedding.APIKey, cfg.Embedding.ModelName, cfg.Embedding.UseMultimodal)
	ls := llm.NewAPILLMService(cfg.LLM.Endpoint, cfg.LLM.APIKey, cfg.LLM.ModelName, cfg.LLM.Temperature, cfg.LLM.MaxTokens)
	dm := document.NewDocumentManager(dp, tc, es, vs, database)

	// Check for CLI subcommands
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "import":
			runBatchImport(os.Args[2:], dm)
			return
		case "help", "-h", "--help":
			printUsage()
			return
		}
	}

	qe := query.NewQueryEngine(es, vs, ls, database, cfg)
	pm := pending.NewPendingQuestionManager(database, tc, es, vs, ls)
	oc := auth.NewOAuthClient(cfg.OAuth.Providers)
	sm := auth.NewSessionManager(database, 24*time.Hour)

	// Create email service
	emailSvc := email.NewService(func() config.SMTPConfig {
		return cm.Get().SMTP
	})

	// 4. Create App
	app := NewApp(database, qe, dm, pm, oc, sm, cm, emailSvc)

	// 5. Register HTTP API handlers
	registerAPIHandlers(app)

	// 6. Serve frontend with SPA fallback (non-API routes serve index.html)
	http.Handle("/", spaHandler("frontend/dist"))

	// 7. Start HTTP server
	addr := fmt.Sprintf("0.0.0.0:%d", cfg.Server.Port)
	fmt.Printf("Helpdesk system starting on http://%s\n", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}

// printUsage prints CLI usage information.
func printUsage() {
	fmt.Println(`用法:
  helpdesk                     启动 HTTP 服务（默认端口 8080）
  helpdesk import <目录> [...]  批量导入目录下的文档到知识库
  helpdesk help                显示此帮助信息

import 命令:
  递归扫描指定目录及子目录，将支持的文件（PDF、Word、Excel、PPT、Markdown）
  解析后存入向量数据库。可同时指定多个目录。

  支持的文件格式: .pdf .doc .docx .xls .xlsx .ppt .pptx .md .markdown

  示例:
    helpdesk import ./docs
    helpdesk import ./docs ./manuals /path/to/files`)
}

// supportedExtensions lists file extensions that can be imported.
var supportedExtensions = map[string]string{
	".pdf":      "pdf",
	".doc":      "word",
	".docx":     "word",
	".xls":      "excel",
	".xlsx":     "excel",
	".ppt":      "ppt",
	".pptx":     "ppt",
	".md":       "markdown",
	".markdown": "markdown",
}

// runBatchImport scans directories and imports supported files.
func runBatchImport(args []string, dm *document.DocumentManager) {
	if len(args) == 0 {
		fmt.Println("错误: 请指定至少一个目录路径")
		fmt.Println("用法: helpdesk import <目录> [...]")
		os.Exit(1)
	}

	// Collect all files to import
	var files []string
	for _, dir := range args {
		info, err := os.Stat(dir)
		if err != nil {
			fmt.Printf("警告: 无法访问 %s: %v\n", dir, err)
			continue
		}
		if !info.IsDir() {
			// Single file
			if _, ok := supportedExtensions[strings.ToLower(filepath.Ext(dir))]; ok {
				files = append(files, dir)
			} else {
				fmt.Printf("跳过: 不支持的文件格式 %s\n", dir)
			}
			continue
		}
		// Walk directory
		filepath.Walk(dir, func(path string, fi os.FileInfo, err error) error {
			if err != nil {
				fmt.Printf("警告: 无法访问 %s: %v\n", path, err)
				return nil
			}
			if fi.IsDir() {
				return nil
			}
			ext := strings.ToLower(filepath.Ext(fi.Name()))
			if _, ok := supportedExtensions[ext]; ok {
				files = append(files, path)
			}
			return nil
		})
	}

	if len(files) == 0 {
		fmt.Println("未找到支持的文件")
		return
	}

	fmt.Printf("找到 %d 个文件，开始导入...\n\n", len(files))

	var success, failed int
	for i, filePath := range files {
		fileName := filepath.Base(filePath)
		ext := strings.ToLower(filepath.Ext(fileName))
		fileType := supportedExtensions[ext]

		fmt.Printf("[%d/%d] %s ... ", i+1, len(files), filePath)

		fileData, err := os.ReadFile(filePath)
		if err != nil {
			fmt.Printf("读取失败: %v\n", err)
			failed++
			continue
		}

		req := document.UploadFileRequest{
			FileName: fileName,
			FileData: fileData,
			FileType: fileType,
		}
		doc, err := dm.UploadFile(req)
		if err != nil {
			fmt.Printf("导入失败: %v\n", err)
			failed++
			continue
		}
		if doc.Status == "failed" {
			fmt.Printf("处理失败: %s\n", doc.Error)
			failed++
			continue
		}

		fmt.Printf("成功 (ID: %s)\n", doc.ID)
		success++
	}

	fmt.Printf("\n导入完成: 成功 %d, 失败 %d, 共 %d\n", success, failed, len(files))
}

func registerAPIHandlers(app *App) {
	// OAuth
	http.HandleFunc("/api/oauth/url", handleOAuthURL(app))
	http.HandleFunc("/api/oauth/callback", handleOAuthCallback(app))

	// Admin login
	http.HandleFunc("/api/admin/login", handleAdminLogin(app))
	http.HandleFunc("/api/admin/setup", handleAdminSetup(app))
	http.HandleFunc("/api/admin/status", handleAdminStatus(app))

	// User registration & login
	http.HandleFunc("/api/auth/register", handleRegister(app))
	http.HandleFunc("/api/auth/login", handleUserLogin(app))
	http.HandleFunc("/api/auth/verify", handleVerifyEmail(app))
	http.HandleFunc("/api/captcha", handleCaptcha())

	// Public info
	http.HandleFunc("/api/product-intro", handleProductIntro(app))

	// Query
	http.HandleFunc("/api/query", handleQuery(app))

	// Documents
	http.HandleFunc("/api/documents/upload", handleDocumentUpload(app))
	http.HandleFunc("/api/documents/url", handleDocumentURL(app))
	http.HandleFunc("/api/documents", handleDocuments(app))
	// DELETE /api/documents/{id} - handled by prefix match
	http.HandleFunc("/api/documents/", handleDocumentByID(app))

	// Pending questions
	http.HandleFunc("/api/pending/answer", handlePendingAnswer(app))
	http.HandleFunc("/api/pending", handlePending(app))

	// Config (with role check)
	http.HandleFunc("/api/config", handleConfigWithRole(app))

	// Email test
	http.HandleFunc("/api/email/test", handleEmailTest(app))

	// Admin sub-accounts
	http.HandleFunc("/api/admin/users", handleAdminUsers(app))
	http.HandleFunc("/api/admin/users/", handleAdminUserByID(app))
	http.HandleFunc("/api/admin/role", handleAdminRole(app))

	// Knowledge entry
	http.HandleFunc("/api/knowledge", handleKnowledgeEntry(app))

	// Image upload for knowledge entry
	http.HandleFunc("/api/images/upload", handleImageUpload(app))

	// Serve uploaded images
	http.Handle("/api/images/", http.StripPrefix("/api/images/", http.FileServer(http.Dir("./data/images"))))
}

// --- JSON helpers ---

func writeJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}

func readJSONBody(r *http.Request, v interface{}) error {
	defer r.Body.Close()
	return json.NewDecoder(r.Body).Decode(v)
}

// --- OAuth handlers ---

func handleOAuthURL(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		provider := r.URL.Query().Get("provider")
		if provider == "" {
			writeError(w, http.StatusBadRequest, "missing provider parameter")
			return
		}
		url, err := app.GetOAuthURL(provider)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"url": url})
	}
}

func handleOAuthCallback(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			Provider string `json:"provider"`
			Code     string `json:"code"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		resp, err := app.HandleOAuthCallback(req.Provider, req.Code)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// --- Admin login handler ---

func handleAdminLogin(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		resp, err := app.AdminLogin(req.Username, req.Password)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleAdminSetup(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		resp, err := app.AdminSetup(req.Username, req.Password)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleAdminStatus(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		cfg := app.configManager.Get()
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"configured":  app.IsAdminConfigured(),
			"login_route": cfg.Admin.LoginRoute,
		})
	}
}

// --- User registration & login handlers ---

func handleCaptcha() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		cap := GenerateCaptcha()
		writeJSON(w, http.StatusOK, cap)
	}
}

func handleRegister(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			RegisterRequest
			CaptchaID     string `json:"captcha_id"`
			CaptchaAnswer int    `json:"captcha_answer"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if !ValidateCaptcha(req.CaptchaID, req.CaptchaAnswer) {
			writeError(w, http.StatusBadRequest, "验证码错误")
			return
		}
		baseURL := "http://" + r.Host
		if r.TLS != nil {
			baseURL = "https://" + r.Host
		}
		if fwd := r.Header.Get("X-Forwarded-Proto"); fwd != "" {
			baseURL = fwd + "://" + r.Host
		}
		if err := app.Register(req.RegisterRequest, baseURL); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "注册成功，请查收验证邮件"})
	}
}

func handleUserLogin(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			Email         string `json:"email"`
			Password      string `json:"password"`
			CaptchaID     string `json:"captcha_id"`
			CaptchaAnswer int    `json:"captcha_answer"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if !ValidateCaptcha(req.CaptchaID, req.CaptchaAnswer) {
			writeError(w, http.StatusBadRequest, "验证码错误")
			return
		}
		resp, err := app.UserLogin(req.Email, req.Password)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

func handleVerifyEmail(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		token := r.URL.Query().Get("token")
		if err := app.VerifyEmail(token); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "邮箱验证成功，请登录"})
	}
}

// --- Product intro handler ---

func handleProductIntro(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		cfg := app.configManager.Get()
		writeJSON(w, http.StatusOK, map[string]string{"product_intro": cfg.ProductIntro})
	}
}

// --- Query handler ---

func handleQuery(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req query.QueryRequest
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		resp, err := app.queryEngine.Query(req)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, resp)
	}
}

// --- Document handlers ---

func handleDocuments(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		docs, err := app.ListDocuments()
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if docs == nil {
			docs = []document.DocumentInfo{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"documents": docs})
	}
}

func handleDocumentUpload(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		// Parse multipart form (max 50MB)
		if err := r.ParseMultipartForm(50 << 20); err != nil {
			writeError(w, http.StatusBadRequest, "failed to parse multipart form")
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, "missing file in upload")
			return
		}
		defer file.Close()

		fileData, err := io.ReadAll(file)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read file")
			return
		}

		// Determine file type from extension
		fileType := detectFileType(header.Filename)

		req := document.UploadFileRequest{
			FileName: header.Filename,
			FileData: fileData,
			FileType: fileType,
		}
		doc, err := app.UploadFile(req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, doc)
	}
}

func handleDocumentURL(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req document.UploadURLRequest
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		doc, err := app.UploadURL(req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, doc)
	}
}

func handleDocumentByID(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Extract path after /api/documents/
		path := strings.TrimPrefix(r.URL.Path, "/api/documents/")
		if path == "" || path == r.URL.Path {
			writeError(w, http.StatusBadRequest, "missing document ID")
			return
		}

		// Handle /api/documents/{id}/download
		if strings.HasSuffix(path, "/download") {
			docID := strings.TrimSuffix(path, "/download")
			if r.Method != http.MethodGet {
				writeError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			filePath, fileName, err := app.docManager.GetFilePath(docID)
			if err != nil {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			w.Header().Set("Content-Disposition", "inline; filename=\""+fileName+"\"")
			http.ServeFile(w, r, filePath)
			return
		}

		// Handle DELETE /api/documents/{id}
		docID := path
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		if err := app.DeleteDocument(docID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	}
}

// --- Pending question handlers ---

func handlePending(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		status := r.URL.Query().Get("status")
		questions, err := app.ListPendingQuestions(status)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if questions == nil {
			questions = []pending.PendingQuestion{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"questions": questions})
	}
}

func handlePendingAnswer(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req pending.AdminAnswerRequest
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := app.AnswerQuestion(req); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// --- Email test handler ---

func handleEmailTest(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			Email string `json:"email"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := app.TestEmail(req.Email); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "测试邮件已发送"})
	}
}

// --- Admin role check middleware helper ---

// getAdminSession validates the session and checks if it's an admin session.
// Returns (userID, role, error). role is "super_admin" or "editor".
func getAdminSession(app *App, r *http.Request) (string, string, error) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		return "", "", fmt.Errorf("未登录")
	}
	session, err := app.sessionManager.ValidateSession(token)
	if err != nil {
		return "", "", fmt.Errorf("会话无效")
	}
	if !app.IsAdminSession(session.UserID) {
		return "", "", fmt.Errorf("无权限")
	}
	role := app.GetAdminRole(session.UserID)
	if role == "" {
		return "", "", fmt.Errorf("无权限")
	}
	return session.UserID, role, nil
}

// --- Admin sub-account handlers ---

func handleAdminUsers(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, role, err := getAdminSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}

		switch r.Method {
		case http.MethodGet:
			if role != "super_admin" {
				writeError(w, http.StatusForbidden, "仅超级管理员可管理用户")
				return
			}
			users, err := app.ListAdminUsers()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if users == nil {
				users = []AdminUserInfo{}
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"users": users})

		case http.MethodPost:
			if role != "super_admin" {
				writeError(w, http.StatusForbidden, "仅超级管理员可管理用户")
				return
			}
			var req struct {
				Username string `json:"username"`
				Password string `json:"password"`
				Role     string `json:"role"`
			}
			if err := readJSONBody(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid request body")
				return
			}
			user, err := app.CreateAdminUser(req.Username, req.Password, req.Role)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, user)

		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func handleAdminUserByID(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, role, err := getAdminSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if role != "super_admin" {
			writeError(w, http.StatusForbidden, "仅超级管理员可管理用户")
			return
		}

		id := strings.TrimPrefix(r.URL.Path, "/api/admin/users/")
		if id == "" {
			writeError(w, http.StatusBadRequest, "missing user ID")
			return
		}

		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		if err := app.DeleteAdminUser(id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func handleAdminRole(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, role, err := getAdminSession(app, r)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]string{"role": ""})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"role": role})
	}
}

// --- Knowledge entry handler ---

func handleImageUpload(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, _, err := getAdminSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}

		// Parse multipart form (max 10MB)
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			writeError(w, http.StatusBadRequest, "failed to parse form")
			return
		}

		file, header, err := r.FormFile("image")
		if err != nil {
			writeError(w, http.StatusBadRequest, "missing image in upload")
			return
		}
		defer file.Close()

		// Validate image type
		ext := strings.ToLower(filepath.Ext(header.Filename))
		allowedExts := map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".bmp": true}
		if !allowedExts[ext] {
			writeError(w, http.StatusBadRequest, "不支持的图片格式，支持 jpg/png/gif/webp/bmp")
			return
		}

		data, err := io.ReadAll(file)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read image")
			return
		}

		// Generate unique filename
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to generate ID")
			return
		}
		filename := fmt.Sprintf("%x%s", b, ext)

		// Save to data/images/
		imgDir := filepath.Join(".", "data", "images")
		if err := os.MkdirAll(imgDir, 0755); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create image dir")
			return
		}
		if err := os.WriteFile(filepath.Join(imgDir, filename), data, 0644); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save image")
			return
		}

		url := "/api/images/" + filename
		writeJSON(w, http.StatusOK, map[string]string{"url": url})
	}
}

func handleKnowledgeEntry(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, _, err := getAdminSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		var req KnowledgeEntryRequest
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := app.AddKnowledgeEntry(req); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// --- Config handler with role check ---

func handleConfigWithRole(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, role, err := getAdminSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}

		switch r.Method {
		case http.MethodGet:
			cfg := app.GetConfig()
			if cfg == nil {
				writeError(w, http.StatusInternalServerError, "config not loaded")
				return
			}
			writeJSON(w, http.StatusOK, cfg)
		case http.MethodPut:
			if role != "super_admin" {
				writeError(w, http.StatusForbidden, "仅超级管理员可修改系统设置")
				return
			}
			var updates map[string]interface{}
			if err := readJSONBody(r, &updates); err != nil {
				writeError(w, http.StatusBadRequest, "invalid request body")
				return
			}
			if err := app.UpdateConfig(updates); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// handleServerRestart restarts the server process (super_admin only).
func handleServerRestart(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, role, err := getAdminSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if role != "super_admin" {
			writeError(w, http.StatusForbidden, "仅超级管理员可重启服务")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "restarting"})

		// Gracefully exit so the process supervisor (systemd, etc.) restarts us
		go func() {
			time.Sleep(500 * time.Millisecond)
			os.Exit(0)
		}()
	}
}

// --- Helpers ---

// detectFileType maps file extensions to the internal file type names.
func detectFileType(filename string) string {
	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".pdf"):
		return "pdf"
	case strings.HasSuffix(lower, ".docx"), strings.HasSuffix(lower, ".doc"):
		return "word"
	case strings.HasSuffix(lower, ".xlsx"), strings.HasSuffix(lower, ".xls"):
		return "excel"
	case strings.HasSuffix(lower, ".pptx"), strings.HasSuffix(lower, ".ppt"):
		return "ppt"
	case strings.HasSuffix(lower, ".md"), strings.HasSuffix(lower, ".markdown"):
		return "markdown"
	default:
		return "unknown"
	}
}

// spaHandler serves static files from dir, falling back to index.html for SPA routes.
func spaHandler(dir string) http.Handler {
	fileServer := http.FileServer(http.Dir(dir))
	indexPath := filepath.Join(dir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Clean the path and build the file path
		p := filepath.Join(dir, filepath.Clean(r.URL.Path))
		info, err := os.Stat(p)
		if err == nil && !info.IsDir() {
			// Static file exists, serve it
			fileServer.ServeHTTP(w, r)
			return
		}
		// Fallback: serve index.html for SPA routing
		http.ServeFile(w, r, indexPath)
	})
}
