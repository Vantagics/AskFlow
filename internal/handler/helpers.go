package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// ForbiddenError represents a 403 Forbidden error, distinct from 401 Unauthorized.
type ForbiddenError struct {
	Message string
}

func (e *ForbiddenError) Error() string {
	return e.Message
}

// GetBaseURL derives the public base URL from the request, respecting
// X-Forwarded-Proto for reverse-proxy setups.
func GetBaseURL(r *http.Request) string {
	host := r.Host
	if fwdHost := r.Header.Get("X-Forwarded-Host"); fwdHost != "" {
		host = fwdHost
	}
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if fwd := r.Header.Get("X-Forwarded-Proto"); fwd == "https" || fwd == "http" {
		scheme = fwd
	}
	return scheme + "://" + host
}

// WriteJSON encodes data as JSON and writes it to the response with the given status code.
func WriteJSON(w http.ResponseWriter, status int, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// WriteError writes a JSON error response with the given status code and message.
func WriteError(w http.ResponseWriter, status int, message string) {
	WriteJSON(w, status, map[string]string{"error": message})
}

// ReadJSONBody decodes the request body as JSON into v.
// It validates Content-Type, limits body size to 1MB, and rejects trailing data.
func ReadJSONBody(r *http.Request, v interface{}) error {
	// Validate content type
	ct := r.Header.Get("Content-Type")
	if ct != "" && !strings.HasPrefix(ct, "application/json") {
		return fmt.Errorf("expected Content-Type application/json")
	}
	defer r.Body.Close()
	// Limit request body to 1MB to prevent large payload attacks
	limited := io.LimitReader(r.Body, 1<<20)
	decoder := json.NewDecoder(limited)
	if err := decoder.Decode(v); err != nil {
		return err
	}
	// Ensure no trailing data (prevents request smuggling)
	if decoder.More() {
		return fmt.Errorf("unexpected trailing data in request body")
	}
	return nil
}

// GetUserSession validates the Authorization bearer token and returns the user ID.
func GetUserSession(app *App, r *http.Request) (string, error) {
	authHeader := r.Header.Get("Authorization")
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == "" || token == authHeader {
		// token is empty, or Authorization header didn't have "Bearer " prefix
		return "", fmt.Errorf("未登录")
	}
	session, err := app.sessionManager.ValidateSession(token)
	if err != nil {
		return "", fmt.Errorf("会话已过期")
	}
	return session.UserID, nil
}

// GetAdminSession validates the session and checks if it's an admin session.
// Returns (userID, role, error). role is "super_admin", "editor", or "anonymous_viewer".
// Anonymous viewers are restricted to GET requests only.
func GetAdminSession(app *App, r *http.Request) (string, string, error) {
	authHeader := r.Header.Get("Authorization")
	token := strings.TrimPrefix(authHeader, "Bearer ")
	if token == "" || token == authHeader {
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
	// Anonymous viewers can only perform read operations
	if role == "anonymous_viewer" && r.Method != http.MethodGet {
		return "", "", &ForbiddenError{Message: "此为参观模式，一切更改都不会生效"}
	}
	return session.UserID, role, nil
}

// WriteAdminSessionError writes the appropriate HTTP error for a GetAdminSession failure.
// Returns 403 for ForbiddenError (anonymous write rejection), 401 for all other errors.
func WriteAdminSessionError(w http.ResponseWriter, err error) {
	if _, ok := err.(*ForbiddenError); ok {
		WriteError(w, http.StatusForbidden, err.Error())
		return
	}
	WriteError(w, http.StatusUnauthorized, err.Error())
}

// IsValidHexID checks if the given string is a valid 32-character lowercase hex ID.
func IsValidHexID(id string) bool {
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

// IsValidVideoMagicBytes checks if the file data starts with known video format magic bytes.
func IsValidVideoMagicBytes(data []byte) bool {
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

// IsValidOptionalID validates an optional ID parameter (empty is allowed, non-empty must be hex).
func IsValidOptionalID(id string) bool {
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

// DetectFileType maps file extensions to the internal file type names.
func DetectFileType(filename string) string {
	lower := strings.ToLower(filename)
	switch {
	case strings.HasSuffix(lower, ".pdf"):
		return "pdf"
	case strings.HasSuffix(lower, ".docx"):
		return "word"
	case strings.HasSuffix(lower, ".doc"):
		return "word_legacy"
	case strings.HasSuffix(lower, ".xlsx"):
		return "excel"
	case strings.HasSuffix(lower, ".xls"):
		return "excel_legacy"
	case strings.HasSuffix(lower, ".pptx"):
		return "ppt"
	case strings.HasSuffix(lower, ".ppt"):
		return "ppt_legacy"
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

// ValidatePassword checks password length and complexity requirements.
// Returns an error message if validation fails, or empty string if valid.
func ValidatePassword(password string) string {
	if len(password) < 8 {
		return "密码至少8位"
	}
	if len(password) > 72 {
		return "密码不能超过72位"
	}
	hasLetter := false
	hasDigit := false
	for _, c := range password {
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			hasLetter = true
		}
		if c >= '0' && c <= '9' {
			hasDigit = true
		}
	}
	if !hasLetter || !hasDigit {
		return "密码必须包含字母和数字"
	}
	return ""
}
