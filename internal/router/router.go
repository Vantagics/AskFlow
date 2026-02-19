// Package router provides centralized API route registration.
// All HTTP routes are registered here, grouped by business domain,
// with appropriate middleware applied to each group.
package router

import (
	"net/http"
	"time"

	"askflow/internal/handler"
	"askflow/internal/middleware"
)

// Register registers all API routes to http.DefaultServeMux.
// It creates middleware instances internally and groups routes by business domain.
// Returns a cleanup function that should be called on shutdown to stop background goroutines.
func Register(app *handler.App) func() {
	// Build the secure API middleware chain: SecurityHeaders + CORS + RequestID
	secureAPI := middleware.Chain(
		middleware.SecurityHeaders(),
		middleware.CORS(),
		middleware.RequestID(),
	)

	// Auth rate limiter: 10 attempts per minute per IP
	authRL := middleware.NewRateLimiter(10, 1*time.Minute)
	rateLimit := authRL.Limit()

	// API rate limiter: 60 requests per minute per IP (for non-auth endpoints like translate)
	apiRL := middleware.NewRateLimiter(60, 1*time.Minute)
	apiRateLimit := apiRL.Limit()

	// Helper to apply secureAPI chain
	secure := func(h http.HandlerFunc) http.HandlerFunc {
		return secureAPI(h)
	}

	// Helper to apply secureAPI + auth rate limit
	secureRL := func(h http.HandlerFunc) http.HandlerFunc {
		return secureAPI(rateLimit(h))
	}

	// Helper to apply secureAPI + API rate limit
	secureAPIRL := func(h http.HandlerFunc) http.HandlerFunc {
		return secureAPI(apiRateLimit(h))
	}

	// ── OAuth ──
	http.HandleFunc("/api/oauth/url", secure(handler.HandleOAuthURL(app)))
	http.HandleFunc("/api/oauth/callback", secureRL(handler.HandleOAuthCallback(app)))
	http.HandleFunc("/api/oauth/providers/", secure(handler.HandleOAuthProviderDelete(app)))

	// ── Admin login ──
	http.HandleFunc("/api/admin/login", secureRL(handler.HandleAdminLogin(app)))
	http.HandleFunc("/api/admin/setup", secureRL(handler.HandleAdminSetup(app)))
	http.HandleFunc("/api/admin/status", secure(handler.HandleAdminStatus(app)))

	// ── User registration & login ──
	http.HandleFunc("/api/auth/register", secureRL(handler.HandleRegister(app)))
	http.HandleFunc("/api/auth/login", secureRL(handler.HandleUserLogin(app)))
	http.HandleFunc("/api/auth/verify", secure(handler.HandleVerifyEmail(app)))
	http.HandleFunc("/api/auth/forgot-password", secureRL(handler.HandleForgotPassword(app)))
	http.HandleFunc("/api/auth/reset-password", secureRL(handler.HandleResetPassword(app)))
	http.HandleFunc("/api/auth/sn-login", secureRL(handler.HandleSNLogin(app)))
	http.HandleFunc("/api/auth/ticket-exchange", secureRL(handler.HandleTicketExchange(app)))
	http.HandleFunc("/auth/ticket-login", handler.HandleTicketLogin(app))
	http.HandleFunc("/api/captcha", secure(handler.HandleCaptcha()))
	http.HandleFunc("/api/captcha/image", secureRL(handler.HandleCaptchaImage()))

	// ── Public info (product) ──
	http.HandleFunc("/api/product-intro", secure(handler.HandleProductIntro(app)))
	http.HandleFunc("/api/app-info", secure(handler.HandleAppInfo(app)))
	http.HandleFunc("/api/translate-product-name", secureAPIRL(handler.HandleTranslateProductName(app)))

	// ── Query ──
	http.HandleFunc("/api/query", secureRL(handler.HandleQuery(app)))

	// ── User preferences ──
	http.HandleFunc("/api/user/preferences", secure(handler.HandleUserPreferences(app)))

	// ── Documents ──
	http.HandleFunc("/api/documents/public-download/", secure(handler.HandlePublicDocumentDownload(app)))
	http.HandleFunc("/api/documents/upload", secure(handler.HandleDocumentUpload(app)))
	http.HandleFunc("/api/documents/url/preview", secure(handler.HandleDocumentURLPreview(app)))
	http.HandleFunc("/api/documents/url", secure(handler.HandleDocumentURL(app)))
	http.HandleFunc("/api/documents", secure(handler.HandleDocuments(app)))
	http.HandleFunc("/api/documents/", secure(handler.HandleDocumentByID(app)))

	// ── Pending questions ──
	http.HandleFunc("/api/pending/answer", secure(handler.HandlePendingAnswer(app)))
	http.HandleFunc("/api/pending/create", secure(handler.HandlePendingCreate(app)))
	http.HandleFunc("/api/pending/", secure(handler.HandlePendingByID(app)))
	http.HandleFunc("/api/pending", secure(handler.HandlePending(app)))

	// ── Config ──
	http.HandleFunc("/api/config", secure(handler.HandleConfigWithRole(app)))

	// ── System ──
	http.HandleFunc("/api/system/status", secure(handler.HandleSystemStatus(app)))

	// ── Health check ──
	http.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			handler.WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		handler.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// ── LLM / Embedding test (admin only) ──
	http.HandleFunc("/api/test/llm", secure(handler.HandleTestLLM(app)))
	http.HandleFunc("/api/test/embedding", secure(handler.HandleTestEmbedding(app)))

	// ── Email test ──
	http.HandleFunc("/api/email/test", secureRL(handler.HandleEmailTest(app)))

	// ── Video ──
	http.HandleFunc("/api/video/check-deps", secure(handler.HandleVideoCheckDeps(app)))
	http.HandleFunc("/api/video/validate-rapidspeech", secure(handler.HandleValidateRapidSpeech(app)))
	http.HandleFunc("/api/video/auto-setup/check", secure(handler.HandleVideoAutoSetupCheck(app)))
	http.HandleFunc("/api/video/auto-setup", secure(handler.HandleVideoAutoSetup(app)))

	// ── Admin sub-accounts ──
	http.HandleFunc("/api/admin/users", secure(handler.HandleAdminUsers(app)))
	http.HandleFunc("/api/admin/users/", secure(handler.HandleAdminUserByID(app)))
	http.HandleFunc("/api/admin/role", secure(handler.HandleAdminRole(app)))

	// ── Customer management ──
	http.HandleFunc("/api/admin/customers", secure(handler.HandleAdminCustomers(app)))
	http.HandleFunc("/api/admin/customers/verify", secure(handler.HandleAdminCustomerVerify(app)))
	http.HandleFunc("/api/admin/customers/ban", secure(handler.HandleAdminCustomerBan(app)))
	http.HandleFunc("/api/admin/customers/unban", secure(handler.HandleAdminCustomerUnban(app)))
	http.HandleFunc("/api/admin/customers/delete", secure(handler.HandleAdminCustomerDelete(app)))

	// ── Login ban management ──
	http.HandleFunc("/api/admin/bans", secure(handler.HandleAdminBans(app)))
	http.HandleFunc("/api/admin/bans/unban", secure(handler.HandleAdminUnban(app)))
	http.HandleFunc("/api/admin/bans/add", secure(handler.HandleAdminAddBan(app)))

	// ── Products ──
	http.HandleFunc("/api/products/my", secure(handler.HandleMyProducts(app)))
	http.HandleFunc("/api/products/", secure(handler.HandleProductByID(app)))
	http.HandleFunc("/api/products", secure(handler.HandleProducts(app)))

	// ── Knowledge ──
	http.HandleFunc("/api/knowledge", secure(handler.HandleKnowledgeEntry(app)))

	// ── Image upload ──
	http.HandleFunc("/api/images/upload", secure(handler.HandleImageUpload(app)))

	// ── Video upload ──
	http.HandleFunc("/api/videos/upload", secure(handler.HandleKnowledgeVideoUpload(app)))

	// ── Static file serving ──
	http.HandleFunc("/api/images/", handler.ServeImages())
	http.HandleFunc("/api/videos/knowledge/", handler.ServeKnowledgeVideos())

	// ── Batch import (SSE streaming) ──
	http.HandleFunc("/api/batch-import", secure(handler.HandleBatchImport(app)))

	// ── Log management (admin only) ──
	http.HandleFunc("/api/logs/recent", secure(handler.HandleLogsRecent(app)))
	http.HandleFunc("/api/logs/rotation", secure(handler.HandleLogsRotation(app)))
	http.HandleFunc("/api/logs/download", secure(handler.HandleLogsDownload(app)))

	// ── Public media streaming ──
	http.HandleFunc("/api/media/", secure(handler.HandleMediaStream(app)))

	// Return cleanup function to stop rate limiter goroutines
	return func() {
		authRL.Stop()
		apiRL.Stop()
	}
}
