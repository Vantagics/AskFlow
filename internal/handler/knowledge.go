package handler

import (
	"crypto/rand"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

// --- Knowledge entry handler ---

// HandleImageUpload handles image uploads for knowledge entries.
func HandleImageUpload(app *App) http.HandlerFunc {
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

		// Parse multipart form (max 10MB)
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			WriteError(w, http.StatusBadRequest, "failed to parse form")
			return
		}

		file, header, err := r.FormFile("image")
		if err != nil {
			WriteError(w, http.StatusBadRequest, "missing image in upload")
			return
		}
		defer file.Close()

		// Limit file size to 10MB
		if header.Size > 10<<20 {
			WriteError(w, http.StatusBadRequest, "图片文件过大（最�?0MB�?)
			return
		}

		// Validate image type
		ext := strings.ToLower(filepath.Ext(header.Filename))
		allowedExts := map[string]bool{".jpg": true, ".jpeg": true, ".png": true, ".gif": true, ".webp": true, ".bmp": true}
		if !allowedExts[ext] {
			WriteError(w, http.StatusBadRequest, "不支持的图片格式，支�?jpg/png/gif/webp/bmp")
			return
		}

		data, err := io.ReadAll(io.LimitReader(file, 10<<20+1))
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "failed to read image")
			return
		}
		if len(data) > 10<<20 {
			WriteError(w, http.StatusBadRequest, "图片文件过大（最�?0MB�?)
			return
		}

		// Validate image content by checking magic bytes
		contentType := http.DetectContentType(data)
		if !strings.HasPrefix(contentType, "image/") {
			WriteError(w, http.StatusBadRequest, "文件内容不是有效的图�?)
			return
		}

		// Generate unique filename
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			WriteError(w, http.StatusInternalServerError, "failed to generate ID")
			return
		}
		filename := fmt.Sprintf("%x%s", b, ext)

		// Save to data/images/
		imgDir := filepath.Join(".", "data", "images")
		if err := os.MkdirAll(imgDir, 0755); err != nil {
			WriteError(w, http.StatusInternalServerError, "failed to create image dir")
			return
		}
		if err := os.WriteFile(filepath.Join(imgDir, filename), data, 0644); err != nil {
			WriteError(w, http.StatusInternalServerError, "failed to save image")
			return
		}

		url := "/api/images/" + filename
		WriteJSON(w, http.StatusOK, map[string]string{"url": url})
	}
}

// HandleKnowledgeVideoUpload handles video uploads for knowledge entries.
func HandleKnowledgeVideoUpload(app *App) http.HandlerFunc {
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

		// Parse multipart form (32MB in memory, rest goes to temp files)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			WriteError(w, http.StatusBadRequest, "failed to parse form")
			return
		}

		file, header, err := r.FormFile("video")
		if err != nil {
			WriteError(w, http.StatusBadRequest, "missing video in upload")
			return
		}
		defer file.Close()

		// Validate video type
		ext := strings.ToLower(filepath.Ext(header.Filename))
		allowedExts := map[string]bool{".mp4": true, ".avi": true, ".mkv": true, ".mov": true, ".webm": true}
		if !allowedExts[ext] {
			WriteError(w, http.StatusBadRequest, "不支持的视频格式，支持MP4/AVI/MKV/MOV/WebM")
			return
		}

		// Read with size limit
		cfg := app.configManager.Get()
		if cfg == nil {
			WriteError(w, http.StatusInternalServerError, "config not loaded")
			return
		}
		maxUploadSizeMB := cfg.Video.MaxUploadSizeMB
		maxSize := int64(maxUploadSizeMB) << 20
		data, err := io.ReadAll(io.LimitReader(file, maxSize+1))
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "failed to read video")
			return
		}
		if int64(len(data)) > maxSize {
			WriteError(w, http.StatusBadRequest, fmt.Sprintf("视频文件大小超过限制 (%dMB)", maxUploadSizeMB))
			return
		}

		// Validate video content by checking magic bytes
		if !IsValidVideoMagicBytes(data) {
			WriteError(w, http.StatusBadRequest, "文件内容不是有效的视频格�?)
			return
		}

		// Generate unique filename
		b := make([]byte, 16)
		if _, err := rand.Read(b); err != nil {
			WriteError(w, http.StatusInternalServerError, "failed to generate ID")
			return
		}
		filename := fmt.Sprintf("%x%s", b, ext)

		// Save to data/videos/knowledge/
		videoDir := filepath.Join(".", "data", "videos", "knowledge")
		if err := os.MkdirAll(videoDir, 0755); err != nil {
			WriteError(w, http.StatusInternalServerError, "failed to create video dir")
			return
		}
		if err := os.WriteFile(filepath.Join(videoDir, filename), data, 0644); err != nil {
			WriteError(w, http.StatusInternalServerError, "failed to save video")
			return
		}

		url := "/api/videos/knowledge/" + filename
		WriteJSON(w, http.StatusOK, map[string]string{"url": url})
	}
}

// HandleKnowledgeEntry handles direct knowledge entry creation (text + images).
func HandleKnowledgeEntry(app *App) http.HandlerFunc {
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
		var req KnowledgeEntryRequest
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if err := app.AddKnowledgeEntry(req); err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}
