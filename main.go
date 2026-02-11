package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"helpdesk/internal/auth"
	"helpdesk/internal/backup"
	"helpdesk/internal/captcha"
	"helpdesk/internal/chunker"
	"helpdesk/internal/config"
	"helpdesk/internal/db"
	"helpdesk/internal/document"
	"helpdesk/internal/email"
	"helpdesk/internal/embedding"
	"helpdesk/internal/llm"
	"helpdesk/internal/parser"
	"helpdesk/internal/pending"
	"helpdesk/internal/product"
	"helpdesk/internal/query"
	"helpdesk/internal/vectorstore"
	"helpdesk/internal/video"
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
	log.Printf("[SIMD] Vector acceleration: %s", vectorstore.SIMDCapability())
	tc := &chunker.TextChunker{ChunkSize: cfg.Vector.ChunkSize, Overlap: cfg.Vector.Overlap}
	dp := &parser.DocumentParser{}
	es := embedding.NewAPIEmbeddingService(cfg.Embedding.Endpoint, cfg.Embedding.APIKey, cfg.Embedding.ModelName, cfg.Embedding.UseMultimodal)
	ls := llm.NewAPILLMService(cfg.LLM.Endpoint, cfg.LLM.APIKey, cfg.LLM.ModelName, cfg.LLM.Temperature, cfg.LLM.MaxTokens)
	dm := document.NewDocumentManager(dp, tc, es, vs, database)
	dm.SetVideoConfig(cfg.Video)

	// 视频依赖检测
	if cfg.Video.FFmpegPath != "" || cfg.Video.WhisperPath != "" {
		vp := video.NewParser(cfg.Video)
		ffmpegOK, whisperOK := vp.CheckDependencies()
		statusStr := func(ok bool) string {
			if ok {
				return "可用"
			}
			return "不可用"
		}
		log.Printf("视频检索: ffmpeg=%s, whisper=%s", statusStr(ffmpegOK), statusStr(whisperOK))
	}

	ps := product.NewProductService(database)

	// Check for CLI subcommands
	if len(os.Args) >= 2 {
		switch os.Args[1] {
		case "import":
			runBatchImport(os.Args[2:], dm, ps)
			return
		case "backup":
			runBackup(os.Args[2:], database)
			return
		case "restore":
			runRestore(os.Args[2:])
			return
		case "products":
			runListProducts(ps)
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
	app := NewApp(database, qe, dm, pm, oc, sm, cm, emailSvc, ps)

	// 5. Register HTTP API handlers
	registerAPIHandlers(app)

	// 6. Serve frontend with SPA fallback (non-API routes serve index.html)
	http.Handle("/", spaHandler("frontend/dist"))

	// 7. Start HTTP server with graceful shutdown
	addr := fmt.Sprintf("0.0.0.0:%d", cfg.Server.Port)
	server := &http.Server{
		Addr:         addr,
		ReadTimeout:  30 * time.Second,
		WriteTimeout: 120 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	// Start periodic session cleanup
	go func() {
		ticker := time.NewTicker(1 * time.Hour)
		defer ticker.Stop()
		for range ticker.C {
			if n, err := sm.CleanExpired(); err == nil && n > 0 {
				log.Printf("Cleaned %d expired sessions", n)
			}
		}
	}()

	// Graceful shutdown on SIGINT/SIGTERM
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		sig := <-sigCh
		log.Printf("Received signal %v, shutting down gracefully...", sig)
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(ctx); err != nil {
			log.Printf("Graceful shutdown error: %v", err)
		}
	}()

	if cfg.Server.SSLCert != "" && cfg.Server.SSLKey != "" {
		fmt.Printf("Helpdesk system starting on https://%s\n", addr)
		if err := server.ListenAndServeTLS(cfg.Server.SSLCert, cfg.Server.SSLKey); err != http.ErrServerClosed {
			log.Fatalf("HTTPS server error: %v", err)
		}
	} else {
		fmt.Printf("Helpdesk system starting on http://%s\n", addr)
		if err := server.ListenAndServe(); err != http.ErrServerClosed {
			log.Fatalf("HTTP server error: %v", err)
		}
	}
	log.Println("Server stopped")
}

// printUsage prints CLI usage information.
func printUsage() {
	fmt.Println(`用法:
  helpdesk                                        启动 HTTP 服务（默认端口 8080）
  helpdesk import [--product <product_id>] <目录> [...]  批量导入目录下的文档到知识库
  helpdesk products                                列出所有产品及产品 ID
  helpdesk backup [选项]                           备份整站数据
  helpdesk restore <备份文件>                       从备份恢复数据
  helpdesk help                                   显示此帮助信息

import 命令:
  递归扫描指定目录及子目录，将支持的文件（PDF、Word、Excel、PPT、Markdown、HTML）
  解析后存入向量数据库。可同时指定多个目录。

  选项:
    --product <product_id>  指定目标产品 ID，导入的文档将关联到该产品。
                            如果不指定，文档将导入到公共库。

  支持的文件格式: .pdf .doc .docx .xls .xlsx .ppt .pptx .md .markdown .html .htm

  示例:
    helpdesk import ./docs
    helpdesk import ./docs ./manuals /path/to/files
    helpdesk import --product abc123 ./docs

products 命令:
  列出系统中所有产品的 ID、名称和描述，方便在 import 等命令中使用产品 ID。

  示例:
    helpdesk products

backup 命令:
  将整站数据按类型分层备份为 tar.gz 归档。
  全量模式: 完整数据库快照 + 全部上传文件 + 配置
  增量模式: 仅导出新增数据库行 + 新上传文件 + 配置（可变表全量导出）

  备份文件命名: helpdesk_<模式>_<主机名>_<日期-时间>.tar.gz
  例如: helpdesk_full_myserver_20260212-143000.tar.gz

  选项:
    --output <目录>    备份文件输出目录（默认当前目录）
    --incremental      增量备份模式
    --base <manifest>  增量备份的基准 manifest 文件路径（增量模式必需）

  示例:
    helpdesk backup                                    全量备份到当前目录
    helpdesk backup --output ./backups                 全量备份到指定目录
    helpdesk backup --incremental --base ./backups/helpdesk_full_myserver_20260212-143000.manifest.json

restore 命令:
  从备份归档恢复数据到 data 目录。
  全量恢复: 直接解压即可运行
  增量恢复: 先恢复全量备份，再依次应用增量备份的 db_delta.sql

  选项:
    --target <目录>    恢复目标目录（默认 ./data）

  示例:
    helpdesk restore helpdesk_full_myserver_20260212-143000.tar.gz
    helpdesk restore --target ./data-new backup.tar.gz`)
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
	".html":     "html",
	".htm":      "html",
}

// runBatchImport scans directories and imports supported files.
func runBatchImport(args []string, dm *document.DocumentManager, ps *product.ProductService) {
	// Parse --product flag
	var productID string
	var dirs []string
	for i := 0; i < len(args); i++ {
		if args[i] == "--product" {
			if i+1 >= len(args) {
				fmt.Println("错误: --product 参数需要指定产品 ID")
				fmt.Println("用法: helpdesk import [--product <product_id>] <目录> [...]")
				os.Exit(1)
			}
			productID = args[i+1]
			i++ // skip the value
		} else {
			dirs = append(dirs, args[i])
		}
	}

	if len(dirs) == 0 {
		fmt.Println("错误: 请指定至少一个目录路径")
		fmt.Println("用法: helpdesk import [--product <product_id>] <目录> [...]")
		os.Exit(1)
	}

	// Validate product ID if provided
	if productID != "" {
		p, err := ps.GetByID(productID)
		if err != nil || p == nil {
			fmt.Printf("错误: 指定的产品不存在 (ID: %s)\n", productID)
			os.Exit(1)
		}
		fmt.Printf("目标产品: %s (%s)\n", p.Name, p.ID)
	} else {
		fmt.Println("目标: 公共库")
	}

	// Collect all files to import
	var files []string
	for _, dir := range dirs {
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
			FileName:  fileName,
			FileData:  fileData,
			FileType:  fileType,
			ProductID: productID,
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

// runBackup executes a full or incremental backup of the data directory.
func runBackup(args []string, db *sql.DB) {
	opts := backup.Options{
		DataDir: "./data",
		Mode:    "full",
	}

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--output", "-o":
			if i+1 >= len(args) {
				fmt.Println("错误: --output 需要指定目录")
				os.Exit(1)
			}
			opts.OutputDir = args[i+1]
			i++
		case "--incremental":
			opts.Mode = "incremental"
		case "--base":
			if i+1 >= len(args) {
				fmt.Println("错误: --base 需要指定 manifest 文件路径")
				os.Exit(1)
			}
			opts.ManifestIn = args[i+1]
			i++
		default:
			fmt.Printf("未知参数: %s\n", args[i])
			fmt.Println("用法: helpdesk backup [--output <目录>] [--incremental --base <manifest>]")
			os.Exit(1)
		}
	}

	if opts.OutputDir != "" {
		if err := os.MkdirAll(opts.OutputDir, 0755); err != nil {
			fmt.Printf("创建输出目录失败: %v\n", err)
			os.Exit(1)
		}
	}

	fmt.Printf("开始%s备份...\n", map[string]string{"full": "全量", "incremental": "增量"}[opts.Mode])

	result, err := backup.Run(db, opts)
	if err != nil {
		fmt.Printf("备份失败: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("备份完成:\n")
	fmt.Printf("  归档文件: %s\n", result.ArchivePath)
	fmt.Printf("  Manifest: %s\n", result.ManifestPath)
	fmt.Printf("  文件数: %d, 数据库行数: %d\n", result.FilesWritten, result.DBRows)
	fmt.Printf("  归档大小: %.2f MB\n", float64(result.BytesWritten)/(1024*1024))
}

// runRestore restores data from a backup archive.
func runRestore(args []string) {
	targetDir := "./data"
	var archivePath string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--target", "-t":
			if i+1 >= len(args) {
				fmt.Println("错误: --target 需要指定目录")
				os.Exit(1)
			}
			targetDir = args[i+1]
			i++
		default:
			if archivePath != "" {
				fmt.Printf("未知参数: %s\n", args[i])
				os.Exit(1)
			}
			archivePath = args[i]
		}
	}

	if archivePath == "" {
		fmt.Println("错误: 请指定备份文件路径")
		fmt.Println("用法: helpdesk restore [--target <目录>] <备份文件>")
		os.Exit(1)
	}

	fmt.Printf("从 %s 恢复数据到 %s ...\n", archivePath, targetDir)
	if err := backup.Restore(archivePath, targetDir); err != nil {
		fmt.Printf("恢复失败: %v\n", err)
		os.Exit(1)
	}
}

// runListProducts lists all products with their IDs.
func runListProducts(ps *product.ProductService) {
	products, err := ps.List()
	if err != nil {
		fmt.Printf("查询产品列表失败: %v\n", err)
		os.Exit(1)
	}
	if len(products) == 0 {
		fmt.Println("暂无产品")
		return
	}
	fmt.Printf("%-34s  %-20s  %s\n", "产品 ID", "名称", "描述")
	fmt.Println(strings.Repeat("-", 80))
	for _, p := range products {
		desc := p.Description
		if len(desc) > 30 {
			desc = desc[:30] + "..."
		}
		fmt.Printf("%-34s  %-20s  %s\n", p.ID, p.Name, desc)
	}
	fmt.Printf("\n共 %d 个产品\n", len(products))
}



func registerAPIHandlers(app *App) {
	// Wrap all API handlers with security headers
	secureAPI := func(handler http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("Cache-Control", "no-store")
			handler(w, r)
		}
	}

	// OAuth
	http.HandleFunc("/api/oauth/url", secureAPI(handleOAuthURL(app)))
	http.HandleFunc("/api/oauth/callback", secureAPI(handleOAuthCallback(app)))
	http.HandleFunc("/api/oauth/providers/", secureAPI(handleOAuthProviderDelete(app)))

	// Admin login
	http.HandleFunc("/api/admin/login", secureAPI(handleAdminLogin(app)))
	http.HandleFunc("/api/admin/setup", secureAPI(handleAdminSetup(app)))
	http.HandleFunc("/api/admin/status", secureAPI(handleAdminStatus(app)))

	// User registration & login
	http.HandleFunc("/api/auth/register", secureAPI(handleRegister(app)))
	http.HandleFunc("/api/auth/login", secureAPI(handleUserLogin(app)))
	http.HandleFunc("/api/auth/verify", secureAPI(handleVerifyEmail(app)))
	http.HandleFunc("/api/captcha", secureAPI(handleCaptcha()))
	http.HandleFunc("/api/captcha/image", secureAPI(handleCaptchaImage()))

	// Public info
	http.HandleFunc("/api/product-intro", secureAPI(handleProductIntro(app)))
	http.HandleFunc("/api/app-info", secureAPI(handleAppInfo(app)))
	http.HandleFunc("/api/translate-product-name", secureAPI(handleTranslateProductName(app)))

	// Query
	http.HandleFunc("/api/query", secureAPI(handleQuery(app)))

	// Documents
	http.HandleFunc("/api/documents/upload", secureAPI(handleDocumentUpload(app)))
	http.HandleFunc("/api/documents/url/preview", secureAPI(handleDocumentURLPreview(app)))
	http.HandleFunc("/api/documents/url", secureAPI(handleDocumentURL(app)))
	http.HandleFunc("/api/documents", secureAPI(handleDocuments(app)))
	// DELETE /api/documents/{id} - handled by prefix match
	http.HandleFunc("/api/documents/", secureAPI(handleDocumentByID(app)))

	// Pending questions
	http.HandleFunc("/api/pending/answer", secureAPI(handlePendingAnswer(app)))
	http.HandleFunc("/api/pending/create", secureAPI(handlePendingCreate(app)))
	http.HandleFunc("/api/pending/", secureAPI(handlePendingByID(app)))
	http.HandleFunc("/api/pending", secureAPI(handlePending(app)))

	// Config (with role check)
	http.HandleFunc("/api/config", secureAPI(handleConfigWithRole(app)))

	// System status (public — used by frontend to check if system is ready)
	http.HandleFunc("/api/system/status", secureAPI(handleSystemStatus(app)))

	// Health check endpoint
	http.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// LLM / Embedding test endpoints (admin only)
	http.HandleFunc("/api/test/llm", secureAPI(handleTestLLM(app)))
	http.HandleFunc("/api/test/embedding", secureAPI(handleTestEmbedding(app)))

	// Email test
	http.HandleFunc("/api/email/test", secureAPI(handleEmailTest(app)))

	// Video dependency check
	http.HandleFunc("/api/video/check-deps", secureAPI(handleVideoCheckDeps(app)))

	// Admin sub-accounts
	http.HandleFunc("/api/admin/users", secureAPI(handleAdminUsers(app)))
	http.HandleFunc("/api/admin/users/", secureAPI(handleAdminUserByID(app)))
	http.HandleFunc("/api/admin/role", secureAPI(handleAdminRole(app)))

	// Products — register /my before / to avoid prefix matching issues
	http.HandleFunc("/api/products/my", secureAPI(handleMyProducts(app)))
	http.HandleFunc("/api/products/", secureAPI(handleProductByID(app)))
	http.HandleFunc("/api/products", secureAPI(handleProducts(app)))

	// Knowledge entry
	http.HandleFunc("/api/knowledge", secureAPI(handleKnowledgeEntry(app)))

	// Image upload for knowledge entry
	http.HandleFunc("/api/images/upload", secureAPI(handleImageUpload(app)))

	// Serve uploaded images
	http.Handle("/api/images/", http.StripPrefix("/api/images/", http.FileServer(http.Dir("./data/images"))))

	// Public media streaming endpoint for video/audio playback in chat
	http.HandleFunc("/api/media/", secureAPI(handleMediaStream(app)))
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
	// Limit request body to 1MB to prevent large payload attacks
	limited := io.LimitReader(r.Body, 1<<20)
	return json.NewDecoder(limited).Decode(v)
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

func handleOAuthProviderDelete(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Require admin session
		_, role, err := getAdminSession(app, r)
		if err != nil || role != "super_admin" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// Extract provider name from URL: /api/oauth/providers/{name}
		provider := strings.TrimPrefix(r.URL.Path, "/api/oauth/providers/")
		if provider == "" {
			writeError(w, http.StatusBadRequest, "missing provider name")
			return
		}
		if err := app.DeleteOAuthProvider(provider); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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
			Username      string `json:"username"`
			Password      string `json:"password"`
			CaptchaID     string `json:"captcha_id"`
			CaptchaAnswer string `json:"captcha_answer"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if !captcha.Validate(req.CaptchaID, req.CaptchaAnswer) {
			writeError(w, http.StatusBadRequest, "验证码错误")
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

func handleCaptchaImage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		cap := captcha.Generate()
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
		productID := r.URL.Query().Get("product_id")
		if productID != "" {
			p, err := app.GetProduct(productID)
			if err == nil && p != nil && p.WelcomeMessage != "" {
				writeJSON(w, http.StatusOK, map[string]string{"product_intro": p.WelcomeMessage})
				return
			}
		}
		cfg := app.configManager.Get()
		writeJSON(w, http.StatusOK, map[string]string{"product_intro": cfg.ProductIntro})
	}
}

// handleAppInfo returns public app info (product_name, enabled OAuth providers) for frontend display.
func handleAppInfo(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		cfg := app.configManager.Get()
		providers := app.GetEnabledOAuthProviders()
		if providers == nil {
			providers = []string{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"product_name":    cfg.ProductName,
			"oauth_providers": providers,
		})
	}
}

// handleTranslateProductName translates the product name to the requested language using LLM.
func handleTranslateProductName(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		lang := r.URL.Query().Get("lang")
		cfg := app.configManager.Get()
		name := cfg.ProductName
		if name == "" {
			writeJSON(w, http.StatusOK, map[string]string{"product_name": ""})
			return
		}
		if lang == "" {
			writeJSON(w, http.StatusOK, map[string]string{"product_name": name})
			return
		}
		translated, err := app.queryEngine.TranslateText(name, lang)
		if err != nil || translated == "" {
			writeJSON(w, http.StatusOK, map[string]string{"product_name": name})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"product_name": translated})
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
		if strings.TrimSpace(req.Question) == "" {
			writeError(w, http.StatusBadRequest, "question is required")
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
		// Require admin session for document listing
		_, _, err := getAdminSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		productID := r.URL.Query().Get("product_id")
		docs, err := app.ListDocuments(productID)
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

		// Require admin session
		_, _, err := getAdminSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
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
			FileName:  header.Filename,
			FileData:  fileData,
			FileType:  fileType,
			ProductID: r.FormValue("product_id"),
		}
		doc, err := app.UploadFile(req)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, doc)
	}
}

func handleDocumentURLPreview(app *App) http.HandlerFunc {
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
		var req struct {
			URL string `json:"url"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		result, err := app.PreviewURL(req.URL)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func handleDocumentURL(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Require admin session
		_, _, err := getAdminSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
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
			// Require admin session for downloads
			_, _, err := getAdminSession(app, r)
			if err != nil {
				writeError(w, http.StatusUnauthorized, err.Error())
				return
			}
			filePath, fileName, err := app.docManager.GetFilePath(docID)
			if err != nil {
				writeError(w, http.StatusNotFound, err.Error())
				return
			}
			// Sanitize filename to prevent header injection
			safeName := strings.ReplaceAll(fileName, "\"", "")
			safeName = strings.ReplaceAll(safeName, "\n", "")
			safeName = strings.ReplaceAll(safeName, "\r", "")
			w.Header().Set("Content-Disposition", "inline; filename=\""+safeName+"\"")
			http.ServeFile(w, r, filePath)
			return
		}

		// Handle DELETE /api/documents/{id}
		docID := path
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		// Require admin session for deletion
		_, _, err := getAdminSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
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
		// Require admin session for pending questions listing
		_, _, err := getAdminSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		status := r.URL.Query().Get("status")
		productID := r.URL.Query().Get("product_id")
		questions, err := app.ListPendingQuestions(status, productID)
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
		// Require admin session
		_, _, err := getAdminSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
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

func handlePendingCreate(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			Question  string `json:"question"`
			UserID    string `json:"user_id"`
			ImageData string `json:"image_data,omitempty"`
			ProductID string `json:"product_id"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Question == "" {
			writeError(w, http.StatusBadRequest, "question is required")
			return
		}
		pq, err := app.CreatePendingQuestion(req.Question, req.UserID, req.ImageData, req.ProductID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, pq)
	}
}

func handlePendingByID(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/pending/")
		if id == "" || id == "answer" || id == "create" {
			writeError(w, http.StatusBadRequest, "missing question ID")
			return
		}
		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Require admin session
		_, _, err := getAdminSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if err := app.DeletePendingQuestion(id); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
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
			Email    string `json:"email"`
			Host     string `json:"host"`
			Port     int    `json:"port"`
			Username string `json:"username"`
			Password string `json:"password"`
			FromAddr string `json:"from_addr"`
			FromName string `json:"from_name"`
			UseTLS   *bool  `json:"use_tls"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		// If SMTP params provided in request, use them for testing (allows testing before save)
		if req.Host != "" {
			smtpCfg := config.SMTPConfig{
				Host:     req.Host,
				Port:     req.Port,
				Username: req.Username,
				Password: req.Password,
				FromAddr: req.FromAddr,
				FromName: req.FromName,
			}
			if req.UseTLS != nil {
				smtpCfg.UseTLS = *req.UseTLS
			} else {
				smtpCfg.UseTLS = true
			}
			// Fall back to saved password if not provided
			if smtpCfg.Password == "" {
				cfg := app.configManager.Get()
				if cfg != nil {
					smtpCfg.Password = cfg.SMTP.Password
				}
			}
			svc := email.NewService(func() config.SMTPConfig { return smtpCfg })
			if err := svc.SendTest(req.Email); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		} else {
			if err := app.TestEmail(req.Email); err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "测试邮件已发送"})
	}
}

// --- Video dependency check handler ---

func handleVideoCheckDeps(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		cfg := app.configManager.Get()
		if cfg == nil {
			writeJSON(w, http.StatusOK, map[string]bool{"ffmpeg_ok": false, "whisper_ok": false})
			return
		}
		vp := video.NewParser(cfg.Video)
		ffmpegOK, whisperOK := vp.CheckDependencies()
		writeJSON(w, http.StatusOK, map[string]bool{"ffmpeg_ok": ffmpegOK, "whisper_ok": whisperOK})
	}
}

// handleMediaStream serves video/audio files for playback in the chat frontend.
// URL format: /api/media/{document_id}
// Supports HTTP Range requests for seeking in video/audio players.
func handleMediaStream(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		docID := strings.TrimPrefix(r.URL.Path, "/api/media/")
		if docID == "" || docID == r.URL.Path {
			writeError(w, http.StatusBadRequest, "missing document ID")
			return
		}
		filePath, fileName, err := app.docManager.GetFilePath(docID)
		if err != nil {
			writeError(w, http.StatusNotFound, "media not found")
			return
		}
		// Set appropriate content type based on extension
		ext := strings.ToLower(filepath.Ext(fileName))
		contentTypes := map[string]string{
			".mp4":  "video/mp4",
			".webm": "video/webm",
			".avi":  "video/x-msvideo",
			".mkv":  "video/x-matroska",
			".mov":  "video/quicktime",
			".mp3":  "audio/mpeg",
			".wav":  "audio/wav",
			".ogg":  "audio/ogg",
			".flac": "audio/flac",
		}
		if ct, ok := contentTypes[ext]; ok {
			w.Header().Set("Content-Type", ct)
		}
		// ServeFile handles Range requests automatically for seeking
		http.ServeFile(w, r, filePath)
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
				Username   string   `json:"username"`
				Password   string   `json:"password"`
				Role       string   `json:"role"`
				ProductIDs []string `json:"product_ids"`
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
			if len(req.ProductIDs) > 0 {
				if err := app.AssignProductsToAdminUser(user.ID, req.ProductIDs); err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
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

// --- Product handlers ---

func handleProducts(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			products, err := app.ListProducts()
			if err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			if products == nil {
				products = []product.Product{}
			}
			writeJSON(w, http.StatusOK, map[string]interface{}{"products": products})

		case http.MethodPost:
			_, role, err := getAdminSession(app, r)
			if err != nil {
				writeError(w, http.StatusUnauthorized, err.Error())
				return
			}
			if role != "super_admin" {
				writeError(w, http.StatusForbidden, "仅超级管理员可管理产品")
				return
			}
			var req struct {
				Name           string `json:"name"`
				Description    string `json:"description"`
				WelcomeMessage string `json:"welcome_message"`
			}
			if err := readJSONBody(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid request body")
				return
			}
			p, err := app.CreateProduct(req.Name, req.Description, req.WelcomeMessage)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, p)

		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func handleProductByID(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/api/products/")
		if id == "" || id == r.URL.Path {
			writeError(w, http.StatusBadRequest, "missing product ID")
			return
		}

		switch r.Method {
		case http.MethodPut:
			_, role, err := getAdminSession(app, r)
			if err != nil {
				writeError(w, http.StatusUnauthorized, err.Error())
				return
			}
			if role != "super_admin" {
				writeError(w, http.StatusForbidden, "仅超级管理员可管理产品")
				return
			}
			var req struct {
				Name           string `json:"name"`
				Description    string `json:"description"`
				WelcomeMessage string `json:"welcome_message"`
			}
			if err := readJSONBody(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid request body")
				return
			}
			p, err := app.UpdateProduct(id, req.Name, req.Description, req.WelcomeMessage)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, p)

		case http.MethodDelete:
			_, role, err := getAdminSession(app, r)
			if err != nil {
				writeError(w, http.StatusUnauthorized, err.Error())
				return
			}
			if role != "super_admin" {
				writeError(w, http.StatusForbidden, "仅超级管理员可管理产品")
				return
			}
			confirm := r.URL.Query().Get("confirm")
			if confirm != "true" {
				hasData, err := app.HasProductDocumentsOrKnowledge(id)
				if err != nil {
					writeError(w, http.StatusInternalServerError, err.Error())
					return
				}
				if hasData {
					writeJSON(w, http.StatusConflict, map[string]interface{}{
						"warning":  "该产品下存在关联的文档或知识条目，确认删除？",
						"has_data": true,
					})
					return
				}
			}
			if err := app.DeleteProduct(id); err != nil {
				writeError(w, http.StatusInternalServerError, err.Error())
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})

		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func handleMyProducts(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		userID, _, err := getAdminSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		products, err := app.GetProductsByAdminUserID(userID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		if products == nil {
			products = []product.Product{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"products": products})
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

// --- System status handler (public) ---

func handleSystemStatus(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		ready := app.configManager.IsReady()
		if ready {
			hasProducts, err := app.productService.HasProducts()
			if err != nil {
				log.Printf("Failed to check products: %v", err)
				ready = false
			} else if !hasProducts {
				ready = false
			}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{
			"ready": ready,
		})
	}
}

// --- LLM test handler (admin only) ---

func handleTestLLM(app *App) http.HandlerFunc {
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
		var req struct {
			Endpoint    string  `json:"endpoint"`
			APIKey      string  `json:"api_key"`
			ModelName   string  `json:"model_name"`
			Temperature float64 `json:"temperature"`
			MaxTokens   int     `json:"max_tokens"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		// If API key is empty, fall back to saved config (user didn't re-enter it)
		if req.APIKey == "" {
			cfg := app.configManager.Get()
			if cfg != nil {
				req.APIKey = cfg.LLM.APIKey
			}
		}
		if req.Endpoint == "" || req.APIKey == "" || req.ModelName == "" {
			writeError(w, http.StatusBadRequest, "endpoint, api_key, model_name are required")
			return
		}
		if req.Temperature == 0 {
			req.Temperature = 0.3
		}
		if req.MaxTokens == 0 {
			req.MaxTokens = 64
		}
		svc := llm.NewAPILLMService(req.Endpoint, req.APIKey, req.ModelName, req.Temperature, req.MaxTokens)
		answer, err := svc.Generate("", nil, "请回复：OK")
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "reply": answer})
	}
}


// --- Embedding test handler (admin only) ---

func handleTestEmbedding(app *App) http.HandlerFunc {
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
		var req struct {
			Endpoint      string `json:"endpoint"`
			APIKey        string `json:"api_key"`
			ModelName     string `json:"model_name"`
			UseMultimodal bool   `json:"use_multimodal"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		// If API key is empty, fall back to saved config (user didn't re-enter it)
		if req.APIKey == "" {
			cfg := app.configManager.Get()
			if cfg != nil {
				req.APIKey = cfg.Embedding.APIKey
			}
		}
		if req.Endpoint == "" || req.APIKey == "" || req.ModelName == "" {
			writeError(w, http.StatusBadRequest, "endpoint, api_key, model_name are required")
			return
		}
		svc := embedding.NewAPIEmbeddingService(req.Endpoint, req.APIKey, req.ModelName, req.UseMultimodal)
		vec, err := svc.Embed("hello")
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "dimensions": len(vec)})
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
	case strings.HasSuffix(lower, ".html"), strings.HasSuffix(lower, ".htm"):
		return "html"
	default:
		return "unknown"
	}
}

// spaHandler serves static files from dir, falling back to index.html for SPA routes.
func spaHandler(dir string) http.Handler {
	fileServer := http.FileServer(http.Dir(dir))
	indexPath := filepath.Join(dir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Clean the path and prevent directory traversal
		cleanPath := filepath.Clean(r.URL.Path)
		if strings.Contains(cleanPath, "..") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		p := filepath.Join(dir, cleanPath)
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
