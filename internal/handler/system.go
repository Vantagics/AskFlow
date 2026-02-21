package handler

import (
	"compress/gzip"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"

	"askflow/internal/config"
	"askflow/internal/email"
	"askflow/internal/embedding"
	"askflow/internal/errlog"
	"askflow/internal/llm"
)

// --- System status handler (public) ---

// HandleSystemStatus returns whether the system is ready (configured and has products).
func HandleSystemStatus(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
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
		WriteJSON(w, http.StatusOK, map[string]interface{}{
			"ready": ready,
		})
	}
}

// --- LLM test handler (admin only) ---

// HandleTestLLM tests LLM connectivity with the provided or saved configuration.
func HandleTestLLM(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, _, err := GetAdminSession(app, r)
		if err != nil {
			WriteAdminSessionError(w, err)
			return
		}
		var req struct {
			Endpoint    string  `json:"endpoint"`
			APIKey      string  `json:"api_key"`
			ModelName   string  `json:"model_name"`
			Temperature float64 `json:"temperature"`
			MaxTokens   int     `json:"max_tokens"`
		}
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
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
			WriteError(w, http.StatusBadRequest, "endpoint, api_key, model_name are required")
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
			WriteError(w, http.StatusBadRequest, "LLM 连接测试失败，请检查配�?)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "reply": answer})
	}
}

// --- Embedding test handler (admin only) ---

// HandleTestEmbedding tests embedding service connectivity with the provided or saved configuration.
func HandleTestEmbedding(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, _, err := GetAdminSession(app, r)
		if err != nil {
			WriteAdminSessionError(w, err)
			return
		}
		var req struct {
			Endpoint      string `json:"endpoint"`
			APIKey        string `json:"api_key"`
			ModelName     string `json:"model_name"`
			UseMultimodal bool   `json:"use_multimodal"`
		}
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
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
			WriteError(w, http.StatusBadRequest, "endpoint, api_key, model_name are required")
			return
		}
		svc := embedding.NewAPIEmbeddingService(req.Endpoint, req.APIKey, req.ModelName, req.UseMultimodal)
		vec, err := svc.Embed("hello")
		if err != nil {
			log.Printf("[TestEmbedding] error: %v", err)
			WriteError(w, http.StatusBadRequest, "Embedding 连接测试失败，请检查配�?)
			return
		}
		WriteJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "dimensions": len(vec)})
	}
}

// --- Config handler with role check ---

// HandleConfigWithRole handles GET (read config) and PUT (update config, super_admin only).
func HandleConfigWithRole(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, role, err := GetAdminSession(app, r)
		if err != nil {
			WriteAdminSessionError(w, err)
			return
		}

		switch r.Method {
		case http.MethodGet:
			cfg := app.GetConfig()
			if cfg == nil {
				WriteError(w, http.StatusInternalServerError, "config not loaded")
				return
			}
			WriteJSON(w, http.StatusOK, cfg)
		case http.MethodPut:
			if role != "super_admin" {
				WriteError(w, http.StatusForbidden, "仅超级管理员可修改系统设�?)
				return
			}
			var updates map[string]interface{}
			if err := ReadJSONBody(r, &updates); err != nil {
				WriteError(w, http.StatusBadRequest, "invalid request body")
				return
			}
			if err := app.UpdateConfig(updates); err != nil {
				log.Printf("[Config] update error: %v", err)
				errlog.Logf("[Config] update failed: %v", err)
				WriteError(w, http.StatusInternalServerError, "更新配置失败")
				return
			}
			WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		default:
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// --- Email test handler ---

// HandleEmailTest sends a test email using provided or saved SMTP configuration.
func HandleEmailTest(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Require admin session for email testing
		_, _, err := GetAdminSession(app, r)
		if err != nil {
			WriteAdminSessionError(w, err)
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
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
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
				errlog.Logf("[Email] test send failed to=%s host=%s:%d: %v", req.Email, req.Host, req.Port, err)
				WriteError(w, http.StatusBadRequest, "发送测试邮件失败，请检查SMTP配置")
				return
			}
		} else {
			if err := app.TestEmail(req.Email); err != nil {
				log.Printf("[EmailTest] error: %v", err)
				errlog.Logf("[Email] test send failed to=%s: %v", req.Email, err)
				WriteError(w, http.StatusBadRequest, "发送测试邮件失败，请检查SMTP配置")
				return
			}
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok", "message": "测试邮件已发�?})
	}
}

// --- Log handlers (super_admin only) ---

// HandleLogsRecent returns the most recent log lines.
// GET /api/logs/recent?lines=50
func HandleLogsRecent(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, role, err := GetAdminSession(app, r)
		if err != nil {
			WriteAdminSessionError(w, err)
			return
		}
		if role != "super_admin" {
			WriteError(w, http.StatusForbidden, "无权限")
			return
		}
		n := 50
		if v := r.URL.Query().Get("lines"); v != "" {
			if parsed, err := strconv.Atoi(v); err == nil && parsed >= 1 {
				n = parsed
			}
			if n > 500 {
				n = 500
			}
		}
		lines, err := errlog.RecentLines(n)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "读取日志失败: "+err.Error())
			return
		}
		if lines == nil {
			lines = []string{}
		}
		WriteJSON(w, http.StatusOK, map[string]interface{}{
			"lines":       lines,
			"rotation_mb": errlog.GetRotationSizeMB(),
		})
	}
}

// HandleLogsRotation gets or sets the log rotation size.
// GET  /api/logs/rotation �?{ "rotation_mb": 100 }
// PUT  /api/logs/rotation { "rotation_mb": 200 }
func HandleLogsRotation(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		_, role, err := GetAdminSession(app, r)
		if err != nil {
			WriteAdminSessionError(w, err)
			return
		}
		if role != "super_admin" {
			WriteError(w, http.StatusForbidden, "无权限")
			return
		}
		switch r.Method {
		case http.MethodGet:
			WriteJSON(w, http.StatusOK, map[string]int{"rotation_mb": errlog.GetRotationSizeMB()})
		case http.MethodPut:
			var req struct {
				RotationMB int `json:"rotation_mb"`
			}
			if err := ReadJSONBody(r, &req); err != nil {
				WriteError(w, http.StatusBadRequest, "invalid request body")
				return
			}
			if req.RotationMB < 1 || req.RotationMB > 10240 {
				WriteError(w, http.StatusBadRequest, "rotation_mb 必须�?1-10240 之间")
				return
			}
			errlog.SetRotationSizeMB(req.RotationMB)
			WriteJSON(w, http.StatusOK, map[string]interface{}{"status": "ok", "rotation_mb": req.RotationMB})
		default:
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

// HandleLogsDownload streams the current error.log as a gzip download.
// GET /api/logs/download
func HandleLogsDownload(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, role, err := GetAdminSession(app, r)
		if err != nil {
			WriteAdminSessionError(w, err)
			return
		}
		if role != "super_admin" {
			WriteError(w, http.StatusForbidden, "无权限")
			return
		}
		logPath := errlog.GetLogPath()
		f, err := os.Open(logPath)
		if err != nil {
			if os.IsNotExist(err) {
				WriteError(w, http.StatusNotFound, "日志文件不存�?)
				return
			}
			WriteError(w, http.StatusInternalServerError, "打开日志文件失败")
			return
		}
		defer f.Close()

		// Limit download to 512MB to prevent abuse
		info, err := f.Stat()
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "读取日志文件信息失败")
			return
		}
		maxDownloadSize := int64(512 * 1024 * 1024) // 512MB
		downloadSize := info.Size()
		if downloadSize > maxDownloadSize {
			downloadSize = maxDownloadSize
		}

		w.Header().Set("Content-Type", "application/gzip")
		w.Header().Set("Content-Disposition", "attachment; filename=error_log.gz")
		w.Header().Set("X-Content-Type-Options", "nosniff")

		gw, err := gzip.NewWriterLevel(w, gzip.BestSpeed)
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "压缩初始化失�?)
			return
		}
		defer gw.Close()
		io.Copy(gw, io.LimitReader(f, downloadSize))
	}
}
