package handler

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// NoDirListing wraps an http.Handler to prevent directory listing.
// Requests ending with "/" or with an empty path receive a 404 response.
func NoDirListing(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/") || r.URL.Path == "" {
			http.NotFound(w, r)
			return
		}
		h.ServeHTTP(w, r)
	})
}

// SpaHandler serves static files from dir, falling back to index.html for SPA routes.
// IMPORTANT: /api/* and /auth/* paths are never served by the SPA — if they reach here
// it means no backend handler matched, so we return a proper JSON 404 or HTTP 404.
func SpaHandler(dir string) http.Handler {
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
		if !strings.HasPrefix(absP, absDir+string(filepath.Separator)) && absP != absDir {
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

// HandleMediaStream serves video/audio files with proper content types and range request support.
// Requires a valid user session (via Authorization header or ?token= query param).
func HandleMediaStream(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Authenticate: support both Authorization header and ?token= query param
		// (query param needed for <video> src attributes that can't set headers)
		authHeader := r.Header.Get("Authorization")
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == "" || token == authHeader {
			token = r.URL.Query().Get("token")
		}
		if token == "" {
			WriteError(w, http.StatusUnauthorized, "未登录")
			return
		}
		if _, sErr := app.sessionManager.ValidateSession(token); sErr != nil {
			WriteError(w, http.StatusUnauthorized, "会话已过期")
			return
		}
		docID := strings.TrimPrefix(r.URL.Path, "/api/media/")
		if docID == "" || docID == r.URL.Path {
			WriteError(w, http.StatusBadRequest, "missing document ID")
			return
		}
		// Validate document ID format to prevent path traversal
		for _, c := range docID {
			if !((c >= 'a' && c <= 'f') || (c >= '0' && c <= '9')) {
				WriteError(w, http.StatusBadRequest, "invalid document ID")
				return
			}
		}
		filePath, fileName, err := app.docManager.GetFilePath(docID)
		if err != nil {
			WriteError(w, http.StatusNotFound, "media not found")
			return
		}
		// Verify file path stays within expected data directory
		absPath, _ := filepath.Abs(filePath)
		absDataDir, _ := filepath.Abs(filepath.Join(".", "data"))
		if realPath, err := filepath.EvalSymlinks(absPath); err == nil {
			absPath = realPath
		}
		if realDataDir, err := filepath.EvalSymlinks(absDataDir); err == nil {
			absDataDir = realDataDir
		}
		if !strings.HasPrefix(absPath, absDataDir+string(filepath.Separator)) {
			WriteError(w, http.StatusForbidden, "forbidden")
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
		// Cache media files for 1 hour (they rarely change once uploaded)
		w.Header().Set("Cache-Control", "public, max-age=3600")
		// ServeFile handles Range requests and Accept-Ranges automatically for seeking/streaming
		http.ServeFile(w, r, filePath)
	}
}

// ServeImages returns an http.HandlerFunc that serves uploaded images with path validation.
// It prevents directory listing and path traversal attacks.
func ServeImages() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/images/")
		if name == "" || name == "upload" || strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
			http.NotFound(w, r)
			return
		}
		filePath := filepath.Join(".", "data", "images", name)
		// Verify the resolved path stays within the images directory
		absDir, _ := filepath.Abs(filepath.Join(".", "data", "images"))
		absFile, _ := filepath.Abs(filePath)
		if !strings.HasPrefix(absFile, absDir+string(filepath.Separator)) && absFile != absDir {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, filePath)
	}
}

// ServeKnowledgeVideos returns an http.HandlerFunc that serves uploaded knowledge videos
// with path validation. It prevents directory listing and path traversal attacks.
func ServeKnowledgeVideos() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimPrefix(r.URL.Path, "/api/videos/knowledge/")
		if name == "" || strings.Contains(name, "..") || strings.Contains(name, "/") || strings.Contains(name, "\\") {
			http.NotFound(w, r)
			return
		}
		filePath := filepath.Join(".", "data", "videos", "knowledge", name)
		// Verify the resolved path stays within the videos directory
		absDir, _ := filepath.Abs(filepath.Join(".", "data", "videos", "knowledge"))
		absFile, _ := filepath.Abs(filePath)
		if !strings.HasPrefix(absFile, absDir+string(filepath.Separator)) && absFile != absDir {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("X-Content-Type-Options", "nosniff")
		http.ServeFile(w, r, filePath)
	}
}
