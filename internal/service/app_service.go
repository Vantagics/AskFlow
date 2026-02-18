// Package service provides the application service layer that encapsulates
// initialization and lifecycle management for the Askflow application.
package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"askflow/internal/auth"
	"askflow/internal/chunker"
	"askflow/internal/config"
	"askflow/internal/db"
	"askflow/internal/document"
	"askflow/internal/email"
	"askflow/internal/embedding"
	"askflow/internal/errlog"
	"askflow/internal/llm"
	"askflow/internal/parser"
	"askflow/internal/pending"
	"askflow/internal/product"
	"askflow/internal/query"
	"askflow/internal/vectorstore"
	"askflow/internal/video"
)

// AppService encapsulates the entire application initialization and lifecycle.
type AppService struct {
	server          *http.Server
	configManager   *config.ConfigManager
	database        *sql.DB
	sessionManager  *auth.SessionManager
	queryEngine     *query.QueryEngine
	docManager      *document.DocumentManager
	pendingManager  *pending.PendingQuestionManager
	oauthClient     *auth.OAuthClient
	emailService    *email.Service
	productService  *product.ProductService
	cfg             *config.Config
	dataDir         string
	sessionCleanup  chan struct{}
	cleanupWg       sync.WaitGroup
}

// Initialize sets up all services and prepares the application for running.
// The dataDir parameter specifies the root data directory.
// overrideBind and overridePort can be used to bypass settings in the config file.
func (as *AppService) Initialize(dataDir string, overrideBind string, overridePort int) error {
	as.dataDir = dataDir

	// 0. Initialize error logger (/var/log/askflow/error.log)
	if err := errlog.Init(); err != nil {
		log.Printf("Warning: error logger init failed: %v (errors will not be persisted to file)", err)
	}

	// 1. Ensure data directory exists
	if err := os.MkdirAll(dataDir, 0755); err != nil {
		return fmt.Errorf("failed to create data directory: %w", err)
	}

	// 2. Initialize ConfigManager and load config
	configPath := filepath.Join(dataDir, "config.json")
	cm, err := config.NewConfigManager(configPath)
	if err != nil {
		return fmt.Errorf("failed to create config manager: %w", err)
	}
	if err := cm.Load(); err != nil {
		return fmt.Errorf("failed to load config: %w", err)
	}
	as.configManager = cm
	as.cfg = cm.Get()

	// 3. Initialize database
	dbPath := as.cfg.Vector.DBPath
	// Make dbPath relative to dataDir if not absolute
	if !filepath.IsAbs(dbPath) {
		dbPath = filepath.Join(dataDir, dbPath)
	}
	database, err := db.InitDB(dbPath)
	if err != nil {
		return fmt.Errorf("failed to initialize database: %w", err)
	}
	as.database = database

	// 4. Create service instances
	vs := vectorstore.NewSQLiteVectorStore(database)
	log.Printf("[SIMD] Vector acceleration: %s", vectorstore.SIMDCapability())
	tc := &chunker.TextChunker{ChunkSize: as.cfg.Vector.ChunkSize, Overlap: as.cfg.Vector.Overlap}
	dp := &parser.DocumentParser{}
	es := embedding.NewAPIEmbeddingService(
		as.cfg.Embedding.Endpoint,
		as.cfg.Embedding.APIKey,
		as.cfg.Embedding.ModelName,
		as.cfg.Embedding.UseMultimodal,
	)
	ls := llm.NewAPILLMService(
		as.cfg.LLM.Endpoint,
		as.cfg.LLM.APIKey,
		as.cfg.LLM.ModelName,
		as.cfg.LLM.Temperature,
		as.cfg.LLM.MaxTokens,
	)
	as.docManager = document.NewDocumentManager(dp, tc, es, vs, database)
	as.docManager.SetVideoConfig(as.cfg.Video)

	// Video dependency check
	if as.cfg.Video.FFmpegPath != "" || as.cfg.Video.RapidSpeechPath != "" {
		vp := video.NewParser(as.cfg.Video)
		depsResult := vp.CheckDependencies()
		statusStr := func(ok bool, errMsg string) string {
			if ok {
				return "可用"
			}
			if errMsg != "" {
				return "不可用 (" + errMsg + ")"
			}
			return "不可用"
		}
		log.Printf("视频检索: ffmpeg=%s, rapidspeech=%s",
			statusStr(depsResult.FFmpegOK, depsResult.FFmpegError),
			statusStr(depsResult.RapidSpeechOK, depsResult.RapidSpeechError))
	}

	as.productService = product.NewProductService(database)
	as.queryEngine = query.NewQueryEngine(es, vs, ls, database, as.cfg)
	as.pendingManager = pending.NewPendingQuestionManager(database, tc, es, vs, ls)
	as.oauthClient = auth.NewOAuthClient(as.cfg.OAuth.Providers)
	as.sessionManager = auth.NewSessionManager(database, 24*time.Hour)

	// Create email service
	as.emailService = email.NewService(func() config.SMTPConfig {
		return as.configManager.Get().SMTP
	})

	// 5. Create HTTP server
	bind := as.cfg.Server.Bind
	if overrideBind != "" {
		bind = overrideBind
	}
	port := as.cfg.Server.Port
	if overridePort > 0 {
		port = overridePort
	}

	// Format address correctly for IPv6
	addr := fmt.Sprintf("%s:%d", bind, port)
	if strings.Contains(bind, ":") && !strings.HasPrefix(bind, "[") {
		addr = fmt.Sprintf("[%s]:%d", bind, port)
	}

	as.server = &http.Server{
		Addr:              addr,
		ReadTimeout:       30 * time.Second,
		ReadHeaderTimeout: 10 * time.Second,
		WriteTimeout:      600 * time.Second,
		IdleTimeout:       120 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1MB max header size
	}

	return nil
}

// Run starts the HTTP server and blocks until the context is cancelled.
// Implements graceful shutdown when ctx is done.
func (as *AppService) Run(ctx context.Context) error {
	if as.server == nil {
		return fmt.Errorf("server not initialized - call Initialize first")
	}

	// Start periodic session cleanup
	as.sessionCleanup = make(chan struct{})
	as.cleanupWg.Add(1)
	go as.runSessionCleanup(ctx)

	// Start server in a goroutine
	errCh := make(chan error, 1)
	go func() {
		if as.cfg.Server.SSLCert != "" && as.cfg.Server.SSLKey != "" {
			log.Printf("Askflow system starting on https://%s", as.server.Addr)
			errCh <- as.server.ListenAndServeTLS(as.cfg.Server.SSLCert, as.cfg.Server.SSLKey)
		} else {
			log.Printf("Askflow system starting on http://%s", as.server.Addr)
			errCh <- as.server.ListenAndServe()
		}
	}()

	// Wait for context cancellation or server error
	select {
	case <-ctx.Done():
		log.Println("Received shutdown signal, shutting down gracefully...")
		return as.Shutdown(10 * time.Second)
	case err := <-errCh:
		if err != http.ErrServerClosed {
			return fmt.Errorf("server error: %w", err)
		}
		return nil
	}
}

// runSessionCleanup runs periodic session cleanup in the background.
func (as *AppService) runSessionCleanup(ctx context.Context) {
	defer as.cleanupWg.Done()
	defer func() {
		if r := recover(); r != nil {
			log.Printf("[SessionCleanup] panic in cleanup goroutine: %v", r)
		}
	}()
	// Create a single LoginLimiter instance for reuse across cleanup cycles
	ll := auth.NewLoginLimiter(as.database)
	ticker := time.NewTicker(1 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-as.sessionCleanup:
			return
		case <-ticker.C:
			if n, err := as.sessionManager.CleanExpired(); err == nil && n > 0 {
				log.Printf("Cleaned %d expired sessions", n)
			}
			// Clean old login attempt records (older than 30 days)
			ll.CleanOld()
		}
	}
}

// Shutdown gracefully shuts down the HTTP server and cleans up resources.
func (as *AppService) Shutdown(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Stop session cleanup (only once)
	if as.sessionCleanup != nil {
		select {
		case <-as.sessionCleanup:
			// Already closed
		default:
			close(as.sessionCleanup)
		}
	}

	// Wait for cleanup goroutine to finish before closing database
	as.cleanupWg.Wait()

	// Shutdown HTTP server
	if as.server != nil {
		if err := as.server.Shutdown(ctx); err != nil {
			log.Printf("Server shutdown error: %v", err)
		}
	}

	// Close database (only once)
	if as.database != nil {
		if err := as.database.Close(); err != nil {
			log.Printf("Database close error: %v", err)
		}
		as.database = nil
	}

	log.Println("Server stopped")
	errlog.Close()
	return nil
}

// GetServer returns the HTTP server instance for handler registration.
func (as *AppService) GetServer() *http.Server {
	return as.server
}

// GetDatabase returns the database connection.
func (as *AppService) GetDatabase() *sql.DB {
	return as.database
}

// GetConfigManager returns the configuration manager.
func (as *AppService) GetConfigManager() *config.ConfigManager {
	return as.configManager
}

// GetConfig returns the current configuration.
func (as *AppService) GetConfig() *config.Config {
	return as.cfg
}

// GetDataDir returns the data directory path.
func (as *AppService) GetDataDir() string {
	return as.dataDir
}

// GetQueryEngine returns the query engine.
func (as *AppService) GetQueryEngine() *query.QueryEngine {
	return as.queryEngine
}

// GetDocManager returns the document manager.
func (as *AppService) GetDocManager() *document.DocumentManager {
	return as.docManager
}

// GetPendingManager returns the pending question manager.
func (as *AppService) GetPendingManager() *pending.PendingQuestionManager {
	return as.pendingManager
}

// GetOAuthClient returns the OAuth client.
func (as *AppService) GetOAuthClient() *auth.OAuthClient {
	return as.oauthClient
}

// GetSessionManager returns the session manager.
func (as *AppService) GetSessionManager() *auth.SessionManager {
	return as.sessionManager
}

// GetEmailService returns the email service.
func (as *AppService) GetEmailService() *email.Service {
	return as.emailService
}

// GetProductService returns the product service.
func (as *AppService) GetProductService() *product.ProductService {
	return as.productService
}
