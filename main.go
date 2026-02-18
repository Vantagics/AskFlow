package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"askflow/internal/auth"
	"askflow/internal/backup"
	"askflow/internal/captcha"
	"askflow/internal/config"
	"askflow/internal/document"
	"askflow/internal/email"
	"askflow/internal/embedding"
	"askflow/internal/llm"
	"askflow/internal/pending"
	"askflow/internal/product"
	"askflow/internal/query"
	"askflow/internal/service"
	"askflow/internal/video"
)

const (
	serviceName = "AskflowService"
	displayName = "Askflow Support Service"
	description = "Vantage Askflow RAG Question Answering Service"
)

func main() {
	// Check if running as Windows service
	isService := isWindowsService()

	// Parse datadir flag from command line
	dataDir := parseDataDirFlag()

	// Handle command-line commands
	if len(os.Args) >= 2 && !isService {
		switch os.Args[1] {
		// Windows service management commands
		case "install":
			handleInstall(os.Args[2:])
			return
		case "remove":
			handleRemove()
			return
		case "start":
			handleStart()
			return
		case "stop":
			handleStop()
			return

		// Existing CLI commands
		case "import":
			runCLICommand(dataDir, func(appSvc *service.AppService) {
				runBatchImport(os.Args[2:], appSvc.GetDocManager(), appSvc.GetProductService())
			})
			return
		case "backup":
			runCLICommand(dataDir, func(appSvc *service.AppService) {
				runBackup(os.Args[2:], appSvc.GetDatabase())
			})
			return
		case "restore":
			runRestore(os.Args[2:])
			return
		case "products":
			runCLICommand(dataDir, func(appSvc *service.AppService) {
				runListProducts(appSvc.GetProductService())
			})
			return
		case "help", "-h", "--help":
			printUsage()
			return
		}
	}

	// Run application
	if isService {
		runAsService(dataDir)
	} else {
		runAsConsoleApp(dataDir)
	}
}

// parseDataDirFlag extracts the --datadir flag from command line arguments.
func parseDataDirFlag() string {
	for i, arg := range os.Args {
		if strings.HasPrefix(arg, "--datadir=") {
			return strings.TrimPrefix(arg, "--datadir=")
		}
		if arg == "--datadir" && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}
	return "./data"
}

// parsePortFlag extracts the --port or -p flag from command line arguments.
func parsePortFlag() int {
	for i, arg := range os.Args {
		if strings.HasPrefix(arg, "--port=") {
			port, err := strconv.Atoi(strings.TrimPrefix(arg, "--port="))
			if err == nil {
				return port
			}
		}
		if (arg == "--port" || arg == "-p") && i+1 < len(os.Args) {
			port, err := strconv.Atoi(os.Args[i+1])
			if err == nil {
				return port
			}
		}
	}
	return 0
}

// parseBindFlag extracts the --bind flag or IP version shorthands (-4/-6) from command line arguments.
func parseBindFlag() string {
	// Check --bind first
	for i, arg := range os.Args {
		if strings.HasPrefix(arg, "--bind=") {
			return strings.TrimPrefix(arg, "--bind=")
		}
		if arg == "--bind" && i+1 < len(os.Args) {
			return os.Args[i+1]
		}
	}

	// Check for shorthand IP version flags
	for _, arg := range os.Args {
		if arg == "-4" || arg == "--ipv4" {
			return "0.0.0.0"
		}
		if arg == "-6" || arg == "--ipv6" {
			return "::"
		}
	}
	return ""
}

// runAsConsoleApp runs the application in console mode.
func runAsConsoleApp(dataDir string) {
	bind := parseBindFlag()
	port := parsePortFlag()

	// Initialize application service
	appSvc := &service.AppService{}
	if err := appSvc.Initialize(dataDir, bind, port); err != nil {
		log.Fatalf("Failed to initialize application: %v", err)
	}

	// Create App and register handlers
	app := createApp(appSvc)
	registerAPIHandlers(app)
	http.Handle("/", spaHandler("frontend/dist"))

	// Run with graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	fmt.Printf("Starting Askflow in console mode (data directory: %s)...\n", dataDir)
	if err := appSvc.Run(ctx); err != nil && err != http.ErrServerClosed {
		log.Fatalf("Server error: %v", err)
	}
}

// runCLICommand initializes the app service and runs a CLI command.
func runCLICommand(dataDir string, fn func(*service.AppService)) {
	appSvc := &service.AppService{}
	if err := appSvc.Initialize(dataDir, "", 0); err != nil {
		log.Fatalf("Failed to initialize application: %v", err)
	}
	defer appSvc.GetDatabase().Close()
	fn(appSvc)
}

// createApp creates an App instance from AppService.
func createApp(appSvc *service.AppService) *App {
	return NewApp(
		appSvc.GetDatabase(),
		appSvc.GetQueryEngine(),
		appSvc.GetDocManager(),
		appSvc.GetPendingManager(),
		appSvc.GetOAuthClient(),
		appSvc.GetSessionManager(),
		appSvc.GetConfigManager(),
		appSvc.GetEmailService(),
		appSvc.GetProductService(),
	)
}

// printUsage prints CLI usage information.
func printUsage() {
	fmt.Println(`Usage:
  askflow                                        Start HTTP service (default port 8080)
  askflow --bind=<addr>                          Specify listen address (e.g., 0.0.0.0, ::, 127.0.0.1)
  askflow -4, --ipv4                             Listen on IPv4 only (equivalent to --bind=0.0.0.0)
  askflow -6, --ipv6                             Listen on IPv6 (equivalent to --bind=::)
  askflow --port=<port>                          Specify service port (or -p <port>)
  askflow --datadir=<path>                       Specify data directory

Windows Service Commands:
  askflow install [-4|-6] [--bind=<addr>] [--port=<port>]  Install as Windows service
  askflow remove                                           Uninstall Windows service
  askflow start                                            Start Windows service
  askflow stop                                             Stop Windows service

CLI Commands:
  askflow import [--product <product_id>] <目录> [...]  批量导入目录下的文档到知识库
  askflow products                                         List all products and their IDs
  askflow backup [options]                                 Backup all system data
  askflow restore <backup_file>                            Restore data from backup
  askflow help                                             Show this help information

import command:
  Recursively scan specified directories and subdirectories for supported files
  (PDF, Word, Excel, PPT, Markdown, HTML), parse them, and store in vector database.
  Multiple directories can be specified.

  Options:
    --product <product_id>  Specify target product ID. Imported documents will be associated
                            with this product. If not specified, they will be imported to the public library.

  Supported formats: .pdf .doc .docx .xls .xlsx .ppt .pptx .md .markdown .html .htm

  Examples:
    askflow import ./docs
    askflow import ./docs ./manuals /path/to/files
    askflow import --product abc123 ./docs

products command:
  List all products' IDs, names, and descriptions in the system.

  Example:
    askflow products

backup command:
  Backup all system data into a tiered tar.gz archive.
  Full mode: Complete database snapshot + all uploaded files + configuration.
  Incremental mode: Export only new database rows + new uploaded files + configuration.

  Backup filename: askflow_<mode>_<hostname>_<date-time>.tar.gz
  Example: askflow_full_myserver_20260212-143000.tar.gz

  Options:
    --output <dir>     Output directory for backup file (default: current directory)
    --incremental      Incremental backup mode
    --base <manifest>  Path to base manifest file (required for incremental mode)

  Examples:
    askflow backup                                    Full backup to current directory
    askflow backup --output ./backups                 Full backup to specified directory
    askflow backup --incremental --base ./backups/askflow_full_myserver_20260212-143000.manifest.json

restore command:
  Restore data from a backup archive to the data directory.
  Full restore: Extract and run directly.
  Incremental restore: Restore full backup first, then apply db_delta.sql from incremental backups.

  Options:
    --target <dir>     Target restore directory (default: ./data)

  Examples:
    askflow restore askflow_full_myserver_20260212-143000.tar.gz
    askflow restore --target ./data-new backup.tar.gz`)
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
				fmt.Println("用法: askflow import [--product <product_id>] <目录> [...]")
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
		fmt.Println("用法: askflow import [--product <product_id>] <目录> [...]")
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

	type failedFile struct {
		Path   string
		Reason string
	}
	var success, failed int
	var failedFiles []failedFile
	for i, filePath := range files {
		fileName := filepath.Base(filePath)
		ext := strings.ToLower(filepath.Ext(fileName))
		fileType := supportedExtensions[ext]

		fmt.Printf("[%d/%d] %s ... ", i+1, len(files), filePath)

		fileData, err := os.ReadFile(filePath)
		if err != nil {
			reason := fmt.Sprintf("读取失败: %v", err)
			fmt.Println(reason)
			failed++
			failedFiles = append(failedFiles, failedFile{Path: filePath, Reason: reason})
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
			reason := fmt.Sprintf("导入失败: %v", err)
			fmt.Println(reason)
			failed++
			failedFiles = append(failedFiles, failedFile{Path: filePath, Reason: reason})
			continue
		}
		if doc.Status == "failed" {
			reason := fmt.Sprintf("处理失败: %s", doc.Error)
			fmt.Println(reason)
			failed++
			failedFiles = append(failedFiles, failedFile{Path: filePath, Reason: reason})
			continue
		}

		fmt.Printf("成功 (ID: %s)\n", doc.ID)
		success++
	}

	fmt.Println("\n========== 导入报告 ==========")
	fmt.Printf("总文件数: %d\n", len(files))
	fmt.Printf("成功文件数: %d\n", success)
	fmt.Printf("失败文件数: %d\n", failed)
	if len(failedFiles) > 0 {
		fmt.Println("\n失败文件列表:")
		for _, f := range failedFiles {
			absPath, err := filepath.Abs(f.Path)
			if err != nil {
				absPath = f.Path
			}
			fmt.Printf("  %s\n    原因: %s\n", absPath, f.Reason)
		}
	}
	fmt.Println("==============================")
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
			fmt.Println("用法: askflow backup [--output <目录>] [--incremental --base <manifest>]")
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
		fmt.Println("用法: askflow restore [--target <目录>] <备份文件>")
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



// --- Rate Limiter ---

// rateLimiter provides per-IP rate limiting using a sliding window counter.
type rateLimiter struct {
	mu       sync.Mutex
	requests map[string][]time.Time
	limit    int           // max requests per window
	window   time.Duration // time window
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	rl := &rateLimiter{
		requests: make(map[string][]time.Time),
		limit:    limit,
		window:   window,
	}
	// Background cleanup of stale entries every 5 minutes
	go func() {
		defer func() {
			if r := recover(); r != nil {
				log.Printf("[RateLimiter] panic in cleanup goroutine: %v", r)
			}
		}()
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			rl.cleanup()
		}
	}()
	return rl
}

func (rl *rateLimiter) allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Prevent memory exhaustion: if too many unique IPs, force cleanup
	if len(rl.requests) > 100000 {
		for k := range rl.requests {
			delete(rl.requests, k)
			if len(rl.requests) <= 50000 {
				break
			}
		}
	}

	// Filter out expired entries
	times := rl.requests[ip]
	valid := times[:0]
	for _, t := range times {
		if t.After(cutoff) {
			valid = append(valid, t)
		}
	}

	if len(valid) >= rl.limit {
		rl.requests[ip] = valid
		return false
	}

	rl.requests[ip] = append(valid, now)
	return true
}

func (rl *rateLimiter) cleanup() {
	rl.mu.Lock()
	defer rl.mu.Unlock()
	cutoff := time.Now().Add(-rl.window)
	for ip, times := range rl.requests {
		valid := times[:0]
		for _, t := range times {
			if t.After(cutoff) {
				valid = append(valid, t)
			}
		}
		if len(valid) == 0 {
			delete(rl.requests, ip)
		} else {
			rl.requests[ip] = valid
		}
	}
}

// getClientIP extracts the client IP from the request, respecting X-Forwarded-For
// but only using the first (leftmost) IP to avoid spoofing.
func getClientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		// Use only the first IP (client IP set by the first proxy)
		if idx := strings.IndexByte(xff, ','); idx != -1 {
			return strings.TrimSpace(xff[:idx])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-Ip"); xri != "" {
		return strings.TrimSpace(xri)
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// Auth rate limiter: 10 attempts per minute per IP
var authRateLimiter = newRateLimiter(10, 1*time.Minute)

// API rate limiter: 60 requests per minute per IP (for non-auth endpoints like translate)
var apiRateLimiter = newRateLimiter(60, 1*time.Minute)

// rateLimit wraps a handler with per-IP rate limiting for auth endpoints.
func rateLimit(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		if !authRateLimiter.allow(ip) {
			writeError(w, http.StatusTooManyRequests, "请求过于频繁，请稍后再试")
			return
		}
		handler(w, r)
	}
}

// apiRateLimit wraps a handler with a more relaxed per-IP rate limit for non-auth endpoints.
func apiRateLimit(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ip := getClientIP(r)
		if !apiRateLimiter.allow(ip) {
			writeError(w, http.StatusTooManyRequests, "请求过于频繁，请稍后再试")
			return
		}
		handler(w, r)
	}
}

func registerAPIHandlers(app *App) {
	// Wrap all API handlers with security headers
	secureAPI := func(handler http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			// Handle CORS preflight
			// Only allow same-origin requests — reflect the Host as allowed origin
			origin := r.Header.Get("Origin")
			if origin != "" {
				// Validate that the origin matches the request host
				// This prevents cross-origin requests from arbitrary domains
				requestHost := r.Host
				if requestHost != "" && (origin == "http://"+requestHost || origin == "https://"+requestHost) {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
					w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
					w.Header().Set("Access-Control-Max-Age", "3600")
					w.Header().Set("Vary", "Origin")
				}
			}
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			w.Header().Set("X-Content-Type-Options", "nosniff")
			w.Header().Set("X-Frame-Options", "DENY")
			w.Header().Set("X-XSS-Protection", "0") // Disabled per OWASP recommendation; CSP is the modern replacement
			w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")
			w.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; img-src 'self' data: blob: https:; media-src 'self' blob:; connect-src 'self'")
			w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=()")
			w.Header().Set("Cache-Control", "no-store")
			w.Header().Set("Strict-Transport-Security", "max-age=63072000; includeSubDomains")
			w.Header().Set("Cross-Origin-Opener-Policy", "same-origin")
			// Add request ID for tracing
			reqID := make([]byte, 8)
			rand.Read(reqID)
			w.Header().Set("X-Request-Id", fmt.Sprintf("%x", reqID))
			handler(w, r)
		}
	}

	// OAuth
	http.HandleFunc("/api/oauth/url", secureAPI(handleOAuthURL(app)))
	http.HandleFunc("/api/oauth/callback", secureAPI(rateLimit(handleOAuthCallback(app))))
	http.HandleFunc("/api/oauth/providers/", secureAPI(handleOAuthProviderDelete(app)))

	// Admin login
	http.HandleFunc("/api/admin/login", secureAPI(rateLimit(handleAdminLogin(app))))
	http.HandleFunc("/api/admin/setup", secureAPI(rateLimit(handleAdminSetup(app))))
	http.HandleFunc("/api/admin/status", secureAPI(handleAdminStatus(app)))

	// User registration & login
	http.HandleFunc("/api/auth/register", secureAPI(rateLimit(handleRegister(app))))
	http.HandleFunc("/api/auth/login", secureAPI(rateLimit(handleUserLogin(app))))
	http.HandleFunc("/api/auth/verify", secureAPI(handleVerifyEmail(app)))
	http.HandleFunc("/api/auth/sn-login", secureAPI(rateLimit(handleSNLogin(app))))
	http.HandleFunc("/api/auth/ticket-exchange", secureAPI(rateLimit(handleTicketExchange(app))))
	http.HandleFunc("/auth/ticket-login", handleTicketLogin(app))
	http.HandleFunc("/api/captcha", secureAPI(handleCaptcha()))
	http.HandleFunc("/api/captcha/image", secureAPI(rateLimit(handleCaptchaImage())))

	// Public info
	http.HandleFunc("/api/product-intro", secureAPI(handleProductIntro(app)))
	http.HandleFunc("/api/app-info", secureAPI(handleAppInfo(app)))
	http.HandleFunc("/api/translate-product-name", secureAPI(apiRateLimit(handleTranslateProductName(app))))

	// Query (rate limited to prevent abuse)
	http.HandleFunc("/api/query", secureAPI(rateLimit(handleQuery(app))))

	// User preferences (default product)
	http.HandleFunc("/api/user/preferences", secureAPI(handleUserPreferences(app)))

	// Documents
	http.HandleFunc("/api/documents/public-download/", secureAPI(handlePublicDocumentDownload(app)))
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
	http.HandleFunc("/api/email/test", secureAPI(rateLimit(handleEmailTest(app))))

	// Video dependency check
	http.HandleFunc("/api/video/check-deps", secureAPI(handleVideoCheckDeps(app)))

	// Admin sub-accounts
	http.HandleFunc("/api/admin/users", secureAPI(handleAdminUsers(app)))
	http.HandleFunc("/api/admin/users/", secureAPI(handleAdminUserByID(app)))
	http.HandleFunc("/api/admin/role", secureAPI(handleAdminRole(app)))

	// Customer management
	http.HandleFunc("/api/admin/customers", secureAPI(handleAdminCustomers(app)))
	http.HandleFunc("/api/admin/customers/verify", secureAPI(handleAdminCustomerVerify(app)))
	http.HandleFunc("/api/admin/customers/ban", secureAPI(handleAdminCustomerBan(app)))
	http.HandleFunc("/api/admin/customers/unban", secureAPI(handleAdminCustomerUnban(app)))
	http.HandleFunc("/api/admin/customers/delete", secureAPI(handleAdminCustomerDelete(app)))

	// Login ban management
	http.HandleFunc("/api/admin/bans", secureAPI(handleAdminBans(app)))
	http.HandleFunc("/api/admin/bans/unban", secureAPI(handleAdminUnban(app)))
	http.HandleFunc("/api/admin/bans/add", secureAPI(handleAdminAddBan(app)))

	// Products — register /my before / to avoid prefix matching issues
	http.HandleFunc("/api/products/my", secureAPI(handleMyProducts(app)))
	http.HandleFunc("/api/products/", secureAPI(handleProductByID(app)))
	http.HandleFunc("/api/products", secureAPI(handleProducts(app)))

	// Knowledge entry
	http.HandleFunc("/api/knowledge", secureAPI(handleKnowledgeEntry(app)))

	// Image upload for knowledge entry
	http.HandleFunc("/api/images/upload", secureAPI(handleImageUpload(app)))

	// Video upload for knowledge entry
	http.HandleFunc("/api/videos/upload", secureAPI(handleKnowledgeVideoUpload(app)))

	// Serve uploaded images (no directory listing, with path validation)
	http.HandleFunc("/api/images/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/images/")
		if name == "" || name == "upload" || strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
			http.NotFound(w, r)
			return
		}
		filePath := filepath.Join(".", "data", "images", name)
		// Verify the resolved path stays within the images directory
		absDir, _ := filepath.Abs(filepath.Join(".", "data", "images"))
		absFile, _ := filepath.Abs(filePath)
		if !strings.HasPrefix(absFile, absDir) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, filePath)
	})

	// Serve uploaded videos for knowledge entries (no directory listing, with path validation)
	http.HandleFunc("/api/videos/knowledge/", func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/videos/knowledge/")
		if name == "" || strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
			http.NotFound(w, r)
			return
		}
		filePath := filepath.Join(".", "data", "videos", "knowledge", name)
		// Verify the resolved path stays within the videos directory
		absDir, _ := filepath.Abs(filepath.Join(".", "data", "videos", "knowledge"))
		absFile, _ := filepath.Abs(filePath)
		if !strings.HasPrefix(absFile, absDir) {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, filePath)
	})

	// Batch import (SSE streaming)
	http.HandleFunc("/api/batch-import", secureAPI(handleBatchImport(app)))

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
	// Validate content type
	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "application/json") {
		return fmt.Errorf("expected Content-Type application/json")
	}
	defer r.Body.Close()
	// Limit request body to 1MB to prevent large payload attacks
	limited := io.LimitReader(r.Body, 1<<20)
	decoder := json.NewDecoder(limited)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	// Ensure no trailing data (prevents request smuggling)
	if decoder.More() {
		return fmt.Errorf("unexpected trailing data in request body")
	}
	return nil
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
		// Extract state from the generated URL for the frontend to store
		// The state is the value of the 'state' query parameter in the auth URL
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
			State    string `json:"state"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		// Validate OAuth state to prevent CSRF (state is required)
		if req.State == "" || !app.oauthClient.ValidateState(req.State) {
			writeError(w, http.StatusBadRequest, "invalid or expired OAuth state")
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
		resp, err := app.AdminLogin(req.Username, req.Password, getClientIP(r))
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
		if fwd := r.Header.Get("X-Forwarded-Proto"); fwd == "https" || fwd == "http" {
			baseURL = fwd + "://" + r.Host
		}
		if err := app.Register(req.RegisterRequest, baseURL); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "注册成功，请查收验证邮件"})
	}
}

// handleUserPreferences handles GET/PUT for user default product preference.
func handleUserPreferences(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, err := getUserSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		switch r.Method {
		case http.MethodGet:
			defaultProductID, err := app.GetUserDefaultProduct(userID)
			if err != nil {
				writeError(w, http.StatusInternalServerError, "获取用户偏好失败")
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"default_product_id": defaultProductID})
		case http.MethodPut:
			var req struct {
				DefaultProductID string `json:"default_product_id"`
			}
			if err := readJSONBody(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid request body")
				return
			}
			if err := app.SetUserDefaultProduct(userID, req.DefaultProductID); err != nil {
				writeError(w, http.StatusInternalServerError, "保存用户偏好失败")
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
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
		// Validate token format (32 hex chars)
		if len(token) != 32 || !isValidHexID(token) {
			writeError(w, http.StatusBadRequest, "无效的验证链接")
			return
		}
		if err := app.VerifyEmail(token); err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "邮箱验证成功，请登录"})
	}
}

// handleSNLogin handles POST /api/auth/sn-login — verifies a license server token
// and returns a one-time login ticket.
func handleSNLogin(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req SNLoginRequest
		if err := readJSONBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, SNLoginResponse{Success: false, Message: "token is required"})
			return
		}
		resp, status, err := app.HandleSNLogin(req.Token)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, SNLoginResponse{Success: false, Message: "internal error"})
			return
		}
		writeJSON(w, status, resp)
	}
}

// handleTicketLogin handles GET /auth/ticket-login?ticket=xxx — redirects to the
// SPA with the ticket as a query parameter so the frontend can exchange it via JS.
func handleTicketLogin(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Redirect(w, r, "/login?error=method_not_allowed", http.StatusFound)
			return
		}
		ticket := r.URL.Query().Get("ticket")
		if ticket == "" {
			http.Redirect(w, r, "/login?error=invalid_ticket", http.StatusFound)
			return
		}
		// Pass ticket to frontend — the SPA will call /api/auth/ticket-exchange to
		// validate it and store the session in localStorage (same pattern as OAuth).
		http.Redirect(w, r, "/?ticket="+ticket, http.StatusFound)
	}
}

// handleTicketExchange handles POST /api/auth/ticket-exchange — validates a one-time
// login ticket and returns {session, user} JSON for the frontend to store in localStorage.
func handleTicketExchange(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			Ticket string `json:"ticket"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"success": false, "message": "ticket is required",
			})
			return
		}
		if req.Ticket == "" {
			writeJSON(w, http.StatusBadRequest, map[string]interface{}{
				"success": false, "message": "ticket is required",
			})
			return
		}

		sessionID, err := app.ValidateLoginTicket(req.Ticket)
		if err != nil {
			status := http.StatusUnauthorized
			writeJSON(w, status, map[string]interface{}{
				"success": false, "message": err.Error(),
			})
			return
		}

		// Fetch session details
		session, err := app.sessionManager.ValidateSession(sessionID)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]interface{}{
				"success": false, "message": "internal error",
			})
			return
		}

		// Fetch user info
		var email, name, provider string
		_ = app.db.QueryRow(
			"SELECT COALESCE(email,''), COALESCE(name,''), COALESCE(provider,'') FROM users WHERE id = ?",
			session.UserID,
		).Scan(&email, &name, &provider)

		writeJSON(w, http.StatusOK, map[string]interface{}{
			"session": session,
			"user": map[string]string{
				"id":       session.UserID,
				"email":    email,
				"name":     name,
				"provider": provider,
			},
		})
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
			if !isValidOptionalID(productID) {
				writeError(w, http.StatusBadRequest, "invalid product_id")
				return
			}
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
			"product_name":        cfg.ProductName,
			"oauth_providers":     providers,
			"max_upload_size_mb":  cfg.Video.MaxUploadSizeMB,
		})
	}
}

// handleTranslateProductName translates the product name to the requested language using LLM.
func handleTranslateProductName(app *App) http.HandlerFunc {
	// Simple in-memory cache for translated product names (avoids LLM call on every page load)
	type cacheEntry struct {
		text    string
		expires time.Time
	}
	var cacheMu sync.Mutex
	cache := make(map[string]cacheEntry)

	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Rate limiting (via apiRateLimit wrapper) prevents LLM abuse
		lang := r.URL.Query().Get("lang")
		// Validate lang parameter to prevent injection
		if len(lang) > 20 {
			writeError(w, http.StatusBadRequest, "invalid language parameter")
			return
		}
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

		// Check cache first
		cacheKey := name + "\x00" + lang
		cacheMu.Lock()
		if entry, ok := cache[cacheKey]; ok && time.Now().Before(entry.expires) {
			cacheMu.Unlock()
			writeJSON(w, http.StatusOK, map[string]string{"product_name": entry.text})
			return
		}
		cacheMu.Unlock()

		// Use a timeout to prevent slow LLM calls from blocking the page load
		type result struct {
			text string
			err  error
		}
		ch := make(chan result, 1)
		go func() {
			translated, err := app.queryEngine.TranslateText(name, lang)
			ch <- result{translated, err}
		}()
		select {
		case res := <-ch:
			if res.err != nil || res.text == "" {
				writeJSON(w, http.StatusOK, map[string]string{"product_name": name})
				return
			}
			// Cache the result for 30 minutes
			cacheMu.Lock()
			cache[cacheKey] = cacheEntry{text: res.text, expires: time.Now().Add(30 * time.Minute)}
			cacheMu.Unlock()
			writeJSON(w, http.StatusOK, map[string]string{"product_name": res.text})
		case <-time.After(10 * time.Second):
			// LLM too slow, return original name
			writeJSON(w, http.StatusOK, map[string]string{"product_name": name})
		}
	}
}

// --- Query handler ---

func handleQuery(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Validate user session
		_, err := getUserSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		var req query.QueryRequest
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		question := strings.TrimSpace(req.Question)
		if question == "" {
			writeError(w, http.StatusBadRequest, "question is required")
			return
		}
		// Limit question length to prevent abuse
		if len(question) > 10000 {
			writeError(w, http.StatusBadRequest, "question too long (max 10000 characters)")
			return
		}
		req.Question = question
		// Default to first product if no product_id specified
		if req.ProductID == "" {
			products, pErr := app.ListProducts()
			if pErr == nil && len(products) > 0 {
				req.ProductID = products[0].ID
			}
		}
		resp, err := app.queryEngine.Query(req)
		if err != nil {
			log.Printf("[Query] error: %v", err)
			writeError(w, http.StatusInternalServerError, "查询处理失败，请稍后重试")
			return
		}
		// Check if product allows document download
		if req.ProductID != "" {
			p, pErr := app.GetProduct(req.ProductID)
			if pErr == nil && p != nil {
				resp.AllowDownload = p.AllowDownload
			}
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
		if !isValidOptionalID(productID) {
			writeError(w, http.StatusBadRequest, "invalid product_id")
			return
		}
		docs, err := app.ListDocuments(productID)
		if err != nil {
			log.Printf("[Documents] list error: %v", err)
			writeError(w, http.StatusInternalServerError, "获取文档列表失败")
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

		// Limit request body size to prevent memory exhaustion
		maxUploadSize := int64(app.configManager.Get().Video.MaxUploadSizeMB)<<20 + 10<<20 // file limit + 10MB overhead
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

		// Parse multipart form (32MB in memory, rest goes to temp files)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeError(w, http.StatusBadRequest, "failed to parse multipart form")
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			writeError(w, http.StatusBadRequest, "missing file in upload")
			return
		}
		defer file.Close()

		// Check file size against configured max
		maxSize := int64(app.configManager.Get().Video.MaxUploadSizeMB) << 20
		fileData, err := io.ReadAll(io.LimitReader(file, maxSize+1))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read file")
			return
		}
		if int64(len(fileData)) > maxSize {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("文件大小超过限制 (%dMB)", app.configManager.Get().Video.MaxUploadSizeMB))
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

// handlePublicDocumentDownload allows regular users to download source documents
// if the product has allow_download enabled and the document type is downloadable.
func handlePublicDocumentDownload(app *App) http.HandlerFunc {
	downloadableTypes := map[string]bool{
		"pdf": true, "doc": true, "docx": true, "word": true,
		"xls": true, "xlsx": true, "excel": true,
		"ppt": true, "pptx": true,
		"mp4": true, "avi": true, "mkv": true, "mov": true, "webm": true,
		"video": true,
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Require user session (support token in query param for direct download links)
		token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if token == "" {
			token = r.URL.Query().Get("token")
		}
		if token == "" {
			writeError(w, http.StatusUnauthorized, "未登录")
			return
		}
		session, sErr := app.sessionManager.ValidateSession(token)
		if sErr != nil {
			writeError(w, http.StatusUnauthorized, "会话已过期")
			return
		}
		_ = session
		docID := strings.TrimPrefix(r.URL.Path, "/api/documents/public-download/")
		if docID == "" || !isValidHexID(docID) {
			writeError(w, http.StatusBadRequest, "invalid document ID")
			return
		}
		productID := r.URL.Query().Get("product_id")
		if productID == "" {
			writeError(w, http.StatusBadRequest, "product_id is required")
			return
		}
		// Check product allows download
		p, pErr := app.GetProduct(productID)
		if pErr != nil || p == nil || !p.AllowDownload {
			writeError(w, http.StatusForbidden, "该产品不允许下载参考文档")
			return
		}
		// Check document type is downloadable
		docInfo, dErr := app.GetDocumentInfo(docID)
		if dErr != nil {
			writeError(w, http.StatusNotFound, "文档未找到")
			return
		}
		docType := strings.ToLower(docInfo.Type)
		if !downloadableTypes[docType] {
			writeError(w, http.StatusForbidden, "该文档类型不支持下载")
			return
		}
		// Verify document belongs to the product
		if docInfo.ProductID != productID && docInfo.ProductID != "" {
			writeError(w, http.StatusForbidden, "文档不属于该产品")
			return
		}
		filePath, fileName, fErr := app.docManager.GetFilePath(docID)
		if fErr != nil {
			writeError(w, http.StatusNotFound, "文件未找到")
			return
		}
		safeName := strings.Map(func(r rune) rune {
			if r == '"' || r == '\n' || r == '\r' || r == '\\' {
				return '_'
			}
			return r
		}, fileName)
		w.Header().Set("Content-Disposition", "attachment; filename=\""+safeName+"\"")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, filePath)
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
			if !isValidHexID(docID) {
				writeError(w, http.StatusBadRequest, "invalid document ID")
				return
			}
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
				writeError(w, http.StatusNotFound, "文件未找到")
				return
			}
			// Sanitize filename to prevent header injection
			safeName := strings.Map(func(r rune) rune {
				if r == '"' || r == '\n' || r == '\r' || r == '\\' {
					return '_'
				}
				return r
			}, fileName)
			w.Header().Set("Content-Disposition", "attachment; filename=\""+safeName+"\"")
			w.Header().Set("X-Content-Type-Options", "nosniff")
			http.ServeFile(w, r, filePath)
			return
		}

		// Handle DELETE /api/documents/{id}
		docID := path
		if !isValidHexID(docID) {
			writeError(w, http.StatusBadRequest, "invalid document ID")
			return
		}
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
			log.Printf("[Documents] delete error for %s: %v", docID, err)
			writeError(w, http.StatusInternalServerError, "删除文档失败")
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	}
}

// --- Batch import handler (SSE) ---

func handleBatchImport(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		// Require admin session with batch_import permission
		userID, role, err := getAdminSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if role != "super_admin" {
			perms := app.GetAdminPermissions(userID)
			hasPerm := false
			for _, p := range perms {
				if p == "batch_import" {
					hasPerm = true
					break
				}
			}
			if !hasPerm {
				writeError(w, http.StatusForbidden, "无批量导入权限")
				return
			}
		}

		var req struct {
			Path      string `json:"path"`
			ProductID string `json:"product_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Path == "" {
			writeError(w, http.StatusBadRequest, "path is required")
			return
		}

		// Validate product ID if provided
		if req.ProductID != "" {
			p, err := app.productService.GetByID(req.ProductID)
			if err != nil || p == nil {
				writeError(w, http.StatusBadRequest, fmt.Sprintf("产品不存在 (ID: %s)", req.ProductID))
				return
			}
		}

		// Validate path exists
		info, err := os.Stat(req.Path)
		if err != nil {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("无法访问路径: %v", err))
			return
		}

		// Collect files
		var files []string
		if !info.IsDir() {
			ext := strings.ToLower(filepath.Ext(req.Path))
			if _, ok := supportedExtensions[ext]; ok {
				files = append(files, req.Path)
			} else {
				writeError(w, http.StatusBadRequest, "不支持的文件格式")
				return
			}
		} else {
			filepath.Walk(req.Path, func(path string, fi os.FileInfo, err error) error {
				if err != nil || fi.IsDir() {
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
			writeError(w, http.StatusBadRequest, "未找到支持的文件")
			return
		}

		// Set SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		flusher, ok := w.(http.Flusher)
		if !ok {
			writeError(w, http.StatusInternalServerError, "streaming not supported")
			return
		}

		sendSSE := func(event string, data interface{}) {
			jsonData, _ := json.Marshal(data)
			fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, jsonData)
			flusher.Flush()
		}

		// Send total count
		sendSSE("start", map[string]int{"total": len(files)})

		type failedItem struct {
			Path   string `json:"path"`
			Reason string `json:"reason"`
		}

		var success, failed int
		var failedFiles []failedItem

		for i, filePath := range files {
			fileName := filepath.Base(filePath)
			ext := strings.ToLower(filepath.Ext(fileName))
			fileType := supportedExtensions[ext]

			absPath, _ := filepath.Abs(filePath)

			fileData, err := os.ReadFile(filePath)
			if err != nil {
				reason := fmt.Sprintf("读取失败: %v", err)
				failed++
				failedFiles = append(failedFiles, failedItem{Path: absPath, Reason: reason})
				sendSSE("progress", map[string]interface{}{
					"index": i + 1, "total": len(files), "file": absPath,
					"status": "failed", "reason": reason,
				})
				continue
			}

			uploadReq := document.UploadFileRequest{
				FileName:  fileName,
				FileData:  fileData,
				FileType:  fileType,
				ProductID: req.ProductID,
			}
			doc, err := app.docManager.UploadFile(uploadReq)
			if err != nil {
				reason := fmt.Sprintf("导入失败: %v", err)
				failed++
				failedFiles = append(failedFiles, failedItem{Path: absPath, Reason: reason})
				sendSSE("progress", map[string]interface{}{
					"index": i + 1, "total": len(files), "file": absPath,
					"status": "failed", "reason": reason,
				})
				continue
			}
			if doc.Status == "failed" {
				reason := fmt.Sprintf("处理失败: %s", doc.Error)
				failed++
				failedFiles = append(failedFiles, failedItem{Path: absPath, Reason: reason})
				sendSSE("progress", map[string]interface{}{
					"index": i + 1, "total": len(files), "file": absPath,
					"status": "failed", "reason": reason,
				})
				continue
			}

			success++
			sendSSE("progress", map[string]interface{}{
				"index": i + 1, "total": len(files), "file": absPath,
				"status": "success", "doc_id": doc.ID,
			})
		}

		// Send final report
		sendSSE("done", map[string]interface{}{
			"total":        len(files),
			"success":      success,
			"failed":       failed,
			"failed_files": failedFiles,
		})
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
		// Validate status parameter
		if status != "" && status != "pending" && status != "answered" && status != "rejected" {
			writeError(w, http.StatusBadRequest, "invalid status parameter")
			return
		}
		productID := r.URL.Query().Get("product_id")
		if !isValidOptionalID(productID) {
			writeError(w, http.StatusBadRequest, "invalid product_id")
			return
		}
		questions, err := app.ListPendingQuestions(status, productID)
		if err != nil {
			log.Printf("[Pending] list error: %v", err)
			writeError(w, http.StatusInternalServerError, "获取问题列表失败")
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
			log.Printf("[Pending] answer error: %v", err)
			writeError(w, http.StatusInternalServerError, "回答问题失败")
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
		// Validate user session
		_, err := getUserSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
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
		// Limit question length to prevent abuse
		if len(req.Question) > 10000 {
			writeError(w, http.StatusBadRequest, "question too long")
			return
		}
		// Limit image data size (base64 encoded, ~4MB decoded)
		if len(req.ImageData) > 5*1024*1024 {
			writeError(w, http.StatusBadRequest, "image data too large")
			return
		}
		pq, err := app.CreatePendingQuestion(req.Question, req.UserID, req.ImageData, req.ProductID)
		if err != nil {
			log.Printf("[Pending] create error: %v", err)
			writeError(w, http.StatusInternalServerError, "创建问题失败")
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
		// Validate ID format (hex string only)
		if !isValidHexID(id) {
			writeError(w, http.StatusBadRequest, "invalid question ID")
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
			log.Printf("[Pending] delete error for %s: %v", id, err)
			writeError(w, http.StatusInternalServerError, "删除问题失败")
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
		// Require admin session for email testing
		_, _, err := getAdminSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		var req struct {
			Email      string `json:"email"`
			Host       string `json:"host"`
			Port       int    `json:"port"`
			Username   string `json:"username"`
			Password   string `json:"password"`
			FromAddr   string `json:"from_addr"`
			FromName   string `json:"from_name"`
			UseTLS     *bool  `json:"use_tls"`
			AuthMethod string `json:"auth_method"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		// If SMTP params provided in request, use them for testing (allows testing before save)
		if req.Host != "" {
			smtpCfg := config.SMTPConfig{
				Host:       req.Host,
				Port:       req.Port,
				Username:   req.Username,
				Password:   req.Password,
				FromAddr:   req.FromAddr,
				FromName:   req.FromName,
				AuthMethod: req.AuthMethod,
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
				log.Printf("[EmailTest] error: %v", err)
				writeError(w, http.StatusBadRequest, "发送测试邮件失败，请检查SMTP配置")
				return
			}
		} else {
			if err := app.TestEmail(req.Email); err != nil {
				log.Printf("[EmailTest] error: %v", err)
				writeError(w, http.StatusBadRequest, "发送测试邮件失败，请检查SMTP配置")
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
		// Require admin session
		_, _, err := getAdminSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		cfg := app.configManager.Get()
		if cfg == nil {
			writeJSON(w, http.StatusOK, map[string]bool{"ffmpeg_ok": false, "rapidspeech_ok": false})
			return
		}
		vp := video.NewParser(cfg.Video)
		ffmpegOK, rapidSpeechOK := vp.CheckDependencies()
		writeJSON(w, http.StatusOK, map[string]bool{"ffmpeg_ok": ffmpegOK, "rapidspeech_ok": rapidSpeechOK})
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
		// Validate document ID format to prevent path traversal
		for _, c := range docID {
			if !((c >= 'a' && c <= 'f') || (c >= '0' && c <= '9')) {
				writeError(w, http.StatusBadRequest, "invalid document ID")
				return
			}
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
		// Set Content-Disposition to inline with sanitized filename
		safeName := strings.Map(func(r rune) rune {
			if r == '"' || r == '\n' || r == '\r' || r == '\\' {
				return '_'
			}
			return r
		}, fileName)
		w.Header().Set("Content-Disposition", "inline; filename=\""+safeName+"\"")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		// ServeFile handles Range requests automatically for seeking
		http.ServeFile(w, r, filePath)
	}
}

// --- Admin role check middleware helper ---

// getUserSession validates the session for regular users.
// Returns (userID, error).
func getUserSession(app *App, r *http.Request) (string, error) {
	token := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if token == "" {
		return "", fmt.Errorf("未登录")
	}
	session, err := app.sessionManager.ValidateSession(token)
	if err != nil {
		return "", fmt.Errorf("会话已过期")
	}
	return session.UserID, nil
}

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
				log.Printf("[Admin] list users error: %v", err)
				writeError(w, http.StatusInternalServerError, "获取用户列表失败")
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
				Username    string   `json:"username"`
				Password    string   `json:"password"`
				Role        string   `json:"role"`
				ProductIDs  []string `json:"product_ids"`
				Permissions []string `json:"permissions"`
			}
			if err := readJSONBody(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid request body")
				return
			}
			user, err := app.CreateAdminUser(req.Username, req.Password, req.Role, req.Permissions)
			if err != nil {
				writeError(w, http.StatusBadRequest, err.Error())
				return
			}
			if len(req.ProductIDs) > 0 {
				if err := app.AssignProductsToAdminUser(user.ID, req.ProductIDs); err != nil {
					log.Printf("[Admin] assign products error: %v", err)
					writeError(w, http.StatusInternalServerError, "分配产品失败")
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
		// Validate ID format
		if !isValidHexID(id) {
			writeError(w, http.StatusBadRequest, "invalid user ID")
			return
		}

		if r.Method != http.MethodDelete {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		if err := app.DeleteAdminUser(id); err != nil {
			log.Printf("[Admin] delete user error for %s: %v", id, err)
			writeError(w, http.StatusInternalServerError, "删除用户失败")
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
		userID, role, err := getAdminSession(app, r)
		if err != nil {
			writeJSON(w, http.StatusOK, map[string]interface{}{"role": "", "permissions": []string{}})
			return
		}
		perms := app.GetAdminPermissions(userID)
		if perms == nil {
			perms = []string{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"role": role, "permissions": perms})
	}
}

// --- Login ban management handlers ---

func handleAdminBans(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, role, err := getAdminSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if role != "super_admin" {
			writeError(w, http.StatusForbidden, "仅超级管理员可管理登录限制")
			return
		}
		bans := app.loginLimiter.ListBans()
		if bans == nil {
			bans = []auth.BanEntry{}
		}
		writeJSON(w, http.StatusOK, map[string]interface{}{"bans": bans})
	}
}

func handleAdminUnban(app *App) http.HandlerFunc {
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
			writeError(w, http.StatusForbidden, "仅超级管理员可管理登录限制")
			return
		}
		var req struct {
			Username string `json:"username"`
			IP       string `json:"ip"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		app.loginLimiter.Unban(req.Username, req.IP)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func handleAdminAddBan(app *App) http.HandlerFunc {
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
			writeError(w, http.StatusForbidden, "仅超级管理员可管理登录限制")
			return
		}
		var req struct {
			Username string `json:"username"`
			IP       string `json:"ip"`
			Reason   string `json:"reason"`
			Days     int    `json:"days"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Username == "" && req.IP == "" {
			writeError(w, http.StatusBadRequest, "请输入用户名或IP")
			return
		}
		if req.Days <= 0 {
			req.Days = 1
		}
		if req.Reason == "" {
			req.Reason = "管理员手动封禁"
		}
		app.loginLimiter.AddManualBan(req.Username, req.IP, req.Reason, time.Duration(req.Days)*24*time.Hour)
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
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

		// Limit file size to 10MB
		if header.Size > 10<<20 {
			writeError(w, http.StatusBadRequest, "图片文件过大（最大10MB）")
			return
		}

		// Validate image type
		ext := strings.ToLower(filepath.Ext(header.Filename))
		allowedExts := map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".bmp": true}
		if !allowedExts[ext] {
			writeError(w, http.StatusBadRequest, "不支持的图片格式，支持 jpg/png/gif/webp/bmp")
			return
		}

		data, err := io.ReadAll(io.LimitReader(file, 10<<20+1))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read image")
			return
		}
		if len(data) > 10<<20 {
			writeError(w, http.StatusBadRequest, "图片文件过大（最大10MB）")
			return
		}

		// Validate image content by checking magic bytes
		contentType := http.DetectContentType(data)
		if !strings.HasPrefix(contentType, "image/") {
			writeError(w, http.StatusBadRequest, "文件内容不是有效的图片")
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

// handleKnowledgeVideoUpload handles video uploads for knowledge entries.
func handleKnowledgeVideoUpload(app *App) http.HandlerFunc {
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

		// Parse multipart form (32MB in memory, rest goes to temp files)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			writeError(w, http.StatusBadRequest, "failed to parse form")
			return
		}

		file, header, err := r.FormFile("video")
		if err != nil {
			writeError(w, http.StatusBadRequest, "missing video in upload")
			return
		}
		defer file.Close()

		// Validate video type
		ext := strings.ToLower(filepath.Ext(header.Filename))
		allowedExts := map[string]bool{".mp4": true, ".avi": true, ".mkv": true, ".mov": true, ".webm": true}
		if !allowedExts[ext] {
			writeError(w, http.StatusBadRequest, "不支持的视频格式，支�?MP4/AVI/MKV/MOV/WebM")
			return
		}

		// Read with size limit
		maxSize := int64(app.configManager.Get().Video.MaxUploadSizeMB) << 20
		data, err := io.ReadAll(io.LimitReader(file, maxSize+1))
		if err != nil {
			writeError(w, http.StatusInternalServerError, "failed to read video")
			return
		}
		if int64(len(data)) > maxSize {
			writeError(w, http.StatusBadRequest, fmt.Sprintf("视频文件大小超过限制 (%dMB)", app.configManager.Get().Video.MaxUploadSizeMB))
			return
		}

		// Validate video content by checking magic bytes
		if !isValidVideoMagicBytes(data) {
			writeError(w, http.StatusBadRequest, "文件内容不是有效的视频格式")
			return
		}

		// Generate unique filename
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to generate ID")
			return
		}
		filename := fmt.Sprintf("%x%s", b, ext)

		// Save to data/videos/knowledge/
		videoDir := filepath.Join(".", "data", "videos", "knowledge")
		if err := os.MkdirAll(videoDir, 0755); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to create video dir")
			return
		}
		if err := os.WriteFile(filepath.Join(videoDir, filename), data, 0644); err != nil {
			writeError(w, http.StatusInternalServerError, "failed to save video")
			return
		}

		url := "/api/videos/knowledge/" + filename
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
				log.Printf("[Products] list error: %v", err)
				writeError(w, http.StatusInternalServerError, "获取产品列表失败")
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
				Type           string `json:"type"`
				Description    string `json:"description"`
				WelcomeMessage string `json:"welcome_message"`
				AllowDownload  bool   `json:"allow_download"`
			}
			if err := readJSONBody(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid request body")
				return
			}
			p, err := app.CreateProduct(req.Name, req.Type, req.Description, req.WelcomeMessage, req.AllowDownload)
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
		if !isValidHexID(id) {
			writeError(w, http.StatusBadRequest, "invalid product ID")
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
				Type           string `json:"type"`
				Description    string `json:"description"`
				WelcomeMessage string `json:"welcome_message"`
				AllowDownload  bool   `json:"allow_download"`
			}
			if err := readJSONBody(r, &req); err != nil {
				writeError(w, http.StatusBadRequest, "invalid request body")
				return
			}
			p, err := app.UpdateProduct(id, req.Name, req.Type, req.Description, req.WelcomeMessage, req.AllowDownload)
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
					log.Printf("[Products] check data error for %s: %v", id, err)
					writeError(w, http.StatusInternalServerError, "检查产品数据失败")
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
				log.Printf("[Products] delete error for %s: %v", id, err)
				writeError(w, http.StatusInternalServerError, "删除产品失败")
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
			log.Printf("[Products] get my products error: %v", err)
			writeError(w, http.StatusInternalServerError, "获取产品列表失败")
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
			log.Printf("[TestLLM] error: %v", err)
			writeError(w, http.StatusBadRequest, "LLM 连接测试失败，请检查配置")
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
			log.Printf("[TestEmbedding] error: %v", err)
			writeError(w, http.StatusBadRequest, "Embedding 连接测试失败，请检查配置")
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
				log.Printf("[Config] update error: %v", err)
				writeError(w, http.StatusInternalServerError, "更新配置失败")
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		default:
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// --- Helpers ---

// isValidHexID checks if a string is a valid hex-encoded ID (32 hex chars).
func isValidHexID(id string) bool {
	if len(id) != 32 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

// isValidVideoMagicBytes checks if the file data starts with known video format magic bytes.
func isValidVideoMagicBytes(data []byte) bool {
	if len(data) < 12 {
		return false
	}
	// MP4/MOV: starts with ftyp box (offset 4)
	if string(data[4:8]) == "ftyp" {
		return true
	}
	// AVI: starts with RIFF....AVI
	if string(data[0:4]) == "RIFF" && len(data) >= 12 && string(data[8:12]) == "AVI " {
		return true
	}
	// MKV/WebM: starts with EBML header (0x1A 0x45 0xDF 0xA3)
	if data[0] == 0x1A && data[1] == 0x45 && data[2] == 0xDF && data[3] == 0xA3 {
		return true
	}
	return false
}

// isValidOptionalID validates an optional ID parameter (empty is allowed, non-empty must be hex).
func isValidOptionalID(id string) bool {
	if id == "" {
		return true
	}
	if len(id) > 64 {
		return false
	}
	for _, c := range id {
		if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
			return false
		}
	}
	return true
}

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
	case strings.HasSuffix(lower, ".mp4"):
		return "mp4"
	case strings.HasSuffix(lower, ".avi"):
		return "avi"
	case strings.HasSuffix(lower, ".mkv"):
		return "mkv"
	case strings.HasSuffix(lower, ".mov"):
		return "mov"
	case strings.HasSuffix(lower, ".webm"):
		return "webm"
	default:
		return "unknown"
	}
}

// noDirListing wraps a file server to return 404 for directory requests instead of listing.
func noDirListing(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") || r.URL.Path == "" {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// spaHandler serves static files from dir, falling back to index.html for SPA routes.
// IMPORTANT: /api/* and /auth/* paths are never served by the SPA — if they reach here
// it means no backend handler matched, so we return a proper JSON 404 or HTTP 404.
func spaHandler(dir string) http.Handler {
	fileServer := http.FileServer(http.Dir(dir))
	indexPath := filepath.Join(dir, "index.html")
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Backend paths must NEVER fall through to the SPA.
		// If an /api/* or /auth/* request reaches here, it means no specific
		// handler was registered for it — return a proper error, not HTML.
		if strings.HasPrefix(r.URL.Path, "/api/") || strings.HasPrefix(r.URL.Path, "/auth/") {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"success":false,"message":"not found"}`))
			return
		}

		// Clean the path and prevent directory traversal
		cleanPath := filepath.Clean(r.URL.Path)
		if strings.Contains(cleanPath, "..") {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		p := filepath.Join(dir, cleanPath)

		// Double-check resolved path stays within the serving directory
		absDir, _ := filepath.Abs(dir)
		absP, _ := filepath.Abs(p)
		if !strings.HasPrefix(absP, absDir) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		// Smart caching strategy:
		// - Files with version query parameters (e.g., ?v=xxx) can be cached long-term
		// - HTML files should not be cached (entry point needs to be fresh)
		// - Other static files without version params use moderate caching
		hasVersionParam := r.URL.Query().Get("v") != ""
		isHTML := strings.HasSuffix(strings.ToLower(cleanPath), ".html") || strings.HasSuffix(strings.ToLower(cleanPath), ".htm")

		if isHTML {
			// HTML files: no caching (need fresh entry point)
			w.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
			w.Header().Set("Pragma", "no-cache")
			w.Header().Set("Expires", "0")
		} else if hasVersionParam {
			// Versioned static files: long-term caching (1 year)
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			// Other static files: short-term caching (5 minutes)
			w.Header().Set("Cache-Control", "public, max-age=300")
		}

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

// --- Customer management handlers ---

func handleAdminCustomers(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, role, err := getAdminSession(app, r)
		if err != nil {
			writeError(w, http.StatusUnauthorized, err.Error())
			return
		}
		if role != "super_admin" {
			writeError(w, http.StatusForbidden, "insufficient permissions")
			return
		}

		// Parse pagination and search params
		page := 1
		pageSize := 20
		search := r.URL.Query().Get("search")
		if p := r.URL.Query().Get("page"); p != "" {
			if v, e := strconv.Atoi(p); e == nil && v > 0 {
				page = v
			}
		}
		if ps := r.URL.Query().Get("page_size"); ps != "" {
			if v, e := strconv.Atoi(ps); e == nil && v > 0 {
				pageSize = v
			}
		}

		result, err := app.ListCustomersPaged(page, pageSize, search)
		if err != nil {
			log.Printf("[Admin] list customers error: %v", err)
			writeError(w, http.StatusInternalServerError, "failed to list customers")
			return
		}
		writeJSON(w, http.StatusOK, result)
	}
}

func handleAdminCustomerVerify(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, role, err := getAdminSession(app, r)
		if err != nil || role != "super_admin" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req struct {
			UserID string `json:"user_id"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := app.VerifyCustomerEmail(req.UserID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func handleAdminCustomerBan(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, role, err := getAdminSession(app, r)
		if err != nil || role != "super_admin" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req struct {
			Email  string `json:"email"`
			Reason string `json:"reason"`
			Days   int    `json:"days"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := app.BanCustomer(req.Email, req.Reason, req.Days); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func handleAdminCustomerUnban(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, role, err := getAdminSession(app, r)
		if err != nil || role != "super_admin" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req struct {
			Email string `json:"email"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := app.UnbanCustomer(req.Email); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func handleAdminCustomerDelete(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			writeError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, role, err := getAdminSession(app, r)
		if err != nil || role != "super_admin" {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		var req struct {
			UserID string `json:"user_id"`
		}
		if err := readJSONBody(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := app.DeleteCustomer(req.UserID); err != nil {
			writeError(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
