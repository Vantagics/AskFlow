// Package service provides the application service layer that encapsulates
// initialization and lifecycle management for the Helpdesk application.
package service

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
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
	"helpdesk/internal/product"
	"helpdesk/internal/query"
	"helpdesk/internal/vectorstore"
	"helpdesk/internal/video"
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
}

// Initialize sets up all services and prepares the application for running.
// The dataDir parameter specifies the root data directory.
func (as *AppService) Initialize(dataDir string) error {
	as.dataDir = dataDir

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
		ffmpegOK, rapidSpeechOK := vp.CheckDependencies()
		statusStr := func(ok bool) string {
			if ok {
				return "可用"
			}
			return "不可用"
		}
		log.Printf("视频检索: ffmpeg=%s, rapidspeech=%s", statusStr(ffmpegOK), statusStr(rapidSpeechOK))
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
	as.server = &http.Server{
		Addr:              fmt.Sprintf("0.0.0.0:%d", as.cfg.Server.Port),
		ReadHeaderTimeout: 30 * time.Second,
		WriteTimeout:      600 * time.Second,
		IdleTimeout:       120 * time.Second,
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
	go as.runSessionCleanup(ctx)

	// Start server in a goroutine
	errCh := make(chan error, 1)
	go func() {
		if as.cfg.Server.SSLCert != "" && as.cfg.Server.SSLKey != "" {
			log.Printf("Helpdesk system starting on https://%s", as.server.Addr)
			errCh <- as.server.ListenAndServeTLS(as.cfg.Server.SSLCert, as.cfg.Server.SSLKey)
		} else {
			log.Printf("Helpdesk system starting on http://%s", as.server.Addr)
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
		}
	}
}

// Shutdown gracefully shuts down the HTTP server and cleans up resources.
func (as *AppService) Shutdown(timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// Stop session cleanup
	if as.sessionCleanup != nil {
		close(as.sessionCleanup)
	}

	// Shutdown HTTP server
	if as.server != nil {
		if err := as.server.Shutdown(ctx); err != nil {
			log.Printf("Server shutdown error: %v", err)
		}
	}

	// Close database
	if as.database != nil {
		if err := as.database.Close(); err != nil {
			log.Printf("Database close error: %v", err)
		}
	}

	log.Println("Server stopped")
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
