package handler

import (
	"net/http"
	"strings"

	"askflow/internal/captcha"
	"askflow/internal/middleware"
)

// --- OAuth handlers ---

// HandleOAuthURL returns the OAuth authorization URL for the given provider.
func HandleOAuthURL(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		provider := r.URL.Query().Get("provider")
		if provider == "" {
			WriteError(w, http.StatusBadRequest, "missing provider parameter")
			return
		}
		url, err := app.GetOAuthURL(provider)
		if err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"url": url})
	}
}

// HandleOAuthCallback exchanges the auth code for user info and creates a session.
func HandleOAuthCallback(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			Provider string `json:"provider"`
			Code     string `json:"code"`
			State    string `json:"state"`
		}
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		// Validate OAuth state to prevent CSRF (state is required)
		if req.State == "" || !app.oauthClient.ValidateState(req.State) {
			WriteError(w, http.StatusBadRequest, "invalid or expired OAuth state")
			return
		}
		resp, err := app.HandleOAuthCallback(req.Provider, req.Code)
		if err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, resp)
	}
}

// HandleOAuthProviderDelete removes an OAuth provider configuration.
func HandleOAuthProviderDelete(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Require admin session
		_, role, err := GetAdminSession(app, r)
		if err != nil || role != "super_admin" {
			WriteError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		// Extract provider name from URL: /api/oauth/providers/{name}
		provider := strings.TrimPrefix(r.URL.Path, "/api/oauth/providers/")
		if provider == "" {
			WriteError(w, http.StatusBadRequest, "missing provider name")
			return
		}
		if err := app.DeleteOAuthProvider(provider); err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

// --- Admin login handler ---

// HandleAdminLogin authenticates an admin user with username, password, and captcha.
func HandleAdminLogin(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			Username      string `json:"username"`
			Password      string `json:"password"`
			CaptchaID     string `json:"captcha_id"`
			CaptchaAnswer string `json:"captcha_answer"`
		}
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if !captcha.Validate(req.CaptchaID, req.CaptchaAnswer) {
			WriteError(w, http.StatusBadRequest, "验证码错误")
			return
		}
		resp, err := app.AdminLogin(req.Username, req.Password, middleware.GetClientIP(r))
		if err != nil {
			WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, resp)
	}
}

// HandleAdminSetup sets up the initial admin account.
func HandleAdminSetup(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			Username string `json:"username"`
			Password string `json:"password"`
		}
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		resp, err := app.AdminSetup(req.Username, req.Password)
		if err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, resp)
	}
}

// HandleAdminStatus returns whether the admin account has been configured.
func HandleAdminStatus(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		cfg := app.configManager.Get()
		WriteJSON(w, http.StatusOK, map[string]interface{}{
			"configured":  app.IsAdminConfigured(),
			"login_route": cfg.Admin.LoginRoute,
		})
	}
}

// --- User registration & login handlers ---

// HandleCaptcha generates a math captcha (text-based).
func HandleCaptcha() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		cap := GenerateCaptcha()
		WriteJSON(w, http.StatusOK, cap)
	}
}

// HandleCaptchaImage generates an image captcha.
func HandleCaptchaImage() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		cap := captcha.Generate()
		WriteJSON(w, http.StatusOK, cap)
	}
}

// HandleRegister creates a new user account with captcha validation.
func HandleRegister(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			RegisterRequest
			CaptchaID     string `json:"captcha_id"`
			CaptchaAnswer int    `json:"captcha_answer"`
		}
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if !ValidateCaptcha(req.CaptchaID, req.CaptchaAnswer) {
			WriteError(w, http.StatusBadRequest, "验证码错误")
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
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "注册成功，请查收验证邮件"})
	}
}

// HandleUserPreferences handles GET/PUT for user default product preference.
func HandleUserPreferences(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		userID, err := GetUserSession(app, r)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}
		switch r.Method {
		case http.MethodGet:
			defaultProductID, err := app.GetUserDefaultProduct(userID)
			if err != nil {
				WriteError(w, http.StatusInternalServerError, "获取用户偏好失败")
				return
			}
			WriteJSON(w, http.StatusOK, map[string]string{"default_product_id": defaultProductID})
		case http.MethodPut:
			var req struct {
				DefaultProductID string `json:"default_product_id"`
			}
			if err := ReadJSONBody(r, &req); err != nil {
				WriteError(w, http.StatusBadRequest, "invalid request body")
				return
			}
			if err := app.SetUserDefaultProduct(userID, req.DefaultProductID); err != nil {
				WriteError(w, http.StatusInternalServerError, "保存用户偏好失败")
				return
			}
			WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		default:
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// HandleUserLogin authenticates a user with email, password, and captcha.
func HandleUserLogin(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			Email         string `json:"email"`
			Password      string `json:"password"`
			CaptchaID     string `json:"captcha_id"`
			CaptchaAnswer int    `json:"captcha_answer"`
		}
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if !ValidateCaptcha(req.CaptchaID, req.CaptchaAnswer) {
			WriteError(w, http.StatusBadRequest, "验证码错误")
			return
		}
		resp, err := app.UserLogin(req.Email, req.Password, middleware.GetClientIP(r))
		if err != nil {
			WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, resp)
	}
}

// HandleVerifyEmail verifies a user's email using a token from the URL.
func HandleVerifyEmail(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		token := r.URL.Query().Get("token")
		// Validate token format (32 hex chars)
		if len(token) != 32 || !IsValidHexID(token) {
			WriteError(w, http.StatusBadRequest, "无效的验证链接")
			return
		}
		if err := app.VerifyEmail(token); err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "邮箱验证成功，请登录"})
	}
}

// HandleForgotPassword handles POST /api/auth/forgot-password — sends a password reset email.
func HandleForgotPassword(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			Email string `json:"email"`
		}
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		baseURL := "http://" + r.Host
		if r.TLS != nil {
			baseURL = "https://" + r.Host
		}
		if fwd := r.Header.Get("X-Forwarded-Proto"); fwd == "https" || fwd == "http" {
			baseURL = fwd + "://" + r.Host
		}
		if err := app.RequestPasswordReset(req.Email, baseURL); err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "如果该邮箱已注册，重置链接将发送到您的邮箱"})
	}
}

// HandleResetPassword handles POST /api/auth/reset-password — resets the password using a token.
func HandleResetPassword(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			Token    string `json:"token"`
			Password string `json:"password"`
		}
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if len(req.Token) != 32 || !IsValidHexID(req.Token) {
			WriteError(w, http.StatusBadRequest, "无效的重置链接")
			return
		}
		if err := app.ResetPassword(req.Token, req.Password); err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "密码重置成功，请登录"})
	}
}

// HandleSNLogin handles POST /api/auth/sn-login — verifies a license server token
// and returns a one-time login ticket.
func HandleSNLogin(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req SNLoginRequest
		if err := ReadJSONBody(r, &req); err != nil {
			WriteJSON(w, http.StatusBadRequest, SNLoginResponse{Success: false, Message: "token is required"})
			return
		}
		resp, status, err := app.HandleSNLogin(req.Token)
		if err != nil {
			WriteJSON(w, http.StatusInternalServerError, SNLoginResponse{Success: false, Message: "internal error"})
			return
		}
		WriteJSON(w, status, resp)
	}
}

// HandleTicketLogin handles GET /auth/ticket-login?ticket=xxx — redirects to the
// SPA with the ticket as a query parameter so the frontend can exchange it via JS.
func HandleTicketLogin(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Redirect(w, r, "/login?error=method_not_allowed", http.StatusFound)
			return
		}
		ticket := r.URL.Query().Get("ticket")
		if ticket == "" || len(ticket) > 128 {
			http.Redirect(w, r, "/login?error=invalid_ticket", http.StatusFound)
			return
		}
		// Validate ticket contains only safe characters (hex + dashes)
		for _, c := range ticket {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f') || c == '-') {
				http.Redirect(w, r, "/login?error=invalid_ticket", http.StatusFound)
				return
			}
		}
		// Pass ticket to frontend — the SPA will call /api/auth/ticket-exchange to
		// validate it and store the session in localStorage (same pattern as OAuth).
		http.Redirect(w, r, "/?ticket="+ticket, http.StatusFound)
	}
}

// HandleTicketExchange handles POST /api/auth/ticket-exchange — validates a one-time
// login ticket and returns {session, user} JSON for the frontend to store in localStorage.
func HandleTicketExchange(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		var req struct {
			Ticket string `json:"ticket"`
		}
		if err := ReadJSONBody(r, &req); err != nil {
			WriteJSON(w, http.StatusBadRequest, map[string]interface{}{
				"success": false, "message": "ticket is required",
			})
			return
		}
		if req.Ticket == "" {
			WriteJSON(w, http.StatusBadRequest, map[string]interface{}{
				"success": false, "message": "ticket is required",
			})
			return
		}

		sessionID, err := app.ValidateLoginTicket(req.Ticket)
		if err != nil {
			status := http.StatusUnauthorized
			WriteJSON(w, status, map[string]interface{}{
				"success": false, "message": err.Error(),
			})
			return
		}

		// Fetch session details
		session, err := app.sessionManager.ValidateSession(sessionID)
		if err != nil {
			WriteJSON(w, http.StatusInternalServerError, map[string]interface{}{
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

		WriteJSON(w, http.StatusOK, map[string]interface{}{
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
