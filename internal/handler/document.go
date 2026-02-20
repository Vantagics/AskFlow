package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"askflow/internal/document"
	"askflow/internal/errlog"
)

// SupportedExtensions lists file extensions that can be imported.
var SupportedExtensions = map[string]string{
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

// HandleDocuments returns the list of documents, optionally filtered by product ID.
func HandleDocuments(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Require admin session for document listing
		_, _, err := GetAdminSession(app, r)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}
		productID := r.URL.Query().Get("product_id")
		if !IsValidOptionalID(productID) {
			WriteError(w, http.StatusBadRequest, "invalid product_id")
			return
		}
		docs, err := app.ListDocuments(productID)
		if err != nil {
			log.Printf("[Documents] list error: %v", err)
			WriteError(w, http.StatusInternalServerError, "获取文档列表失败")
			return
		}
		if docs == nil {
			docs = []document.DocumentInfo{}
		}
		WriteJSON(w, http.StatusOK, map[string]interface{}{"documents": docs})
	}
}

// HandleDocumentUpload handles file upload for documents.
func HandleDocumentUpload(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		// Require admin session
		_, _, err := GetAdminSession(app, r)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}

		// Limit request body size to prevent memory exhaustion
		cfg := app.configManager.Get()
		if cfg == nil {
			WriteError(w, http.StatusInternalServerError, "config not loaded")
			return
		}
		maxUploadSizeMB := cfg.Video.MaxUploadSizeMB
		maxUploadSize := int64(maxUploadSizeMB)<<20 + 10<<20 // file limit + 10MB overhead
		r.Body = http.MaxBytesReader(w, r.Body, maxUploadSize)

		// Parse multipart form (32MB in memory, rest goes to temp files)
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			WriteError(w, http.StatusBadRequest, "failed to parse multipart form")
			return
		}

		file, header, err := r.FormFile("file")
		if err != nil {
			WriteError(w, http.StatusBadRequest, "missing file in upload")
			return
		}
		defer file.Close()

		// Check file size against configured max
		maxSize := int64(maxUploadSizeMB) << 20
		fileData, err := io.ReadAll(io.LimitReader(file, maxSize+1))
		if err != nil {
			WriteError(w, http.StatusInternalServerError, "failed to read file")
			return
		}
		if int64(len(fileData)) > maxSize {
			WriteError(w, http.StatusBadRequest, fmt.Sprintf("文件大小超过限制 (%dMB)", maxUploadSizeMB))
			return
		}

		// Determine file type from extension
		fileType := DetectFileType(header.Filename)

		req := document.UploadFileRequest{
			FileName:  header.Filename,
			FileData:  fileData,
			FileType:  fileType,
			ProductID: r.FormValue("product_id"),
		}
		doc, err := app.UploadFile(req)
		if err != nil {
			errlog.Logf("[API] file upload rejected file=%q type=%s: %v", header.Filename, fileType, err)
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, doc)
	}
}

// HandleDocumentURLPreview fetches and parses URL content for preview.
func HandleDocumentURLPreview(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		_, _, err := GetAdminSession(app, r)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}
		var req struct {
			URL string `json:"url"`
		}
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		result, err := app.PreviewURL(req.URL)
		if err != nil {
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, result)
	}
}

// HandleDocumentURL uploads a document from a URL.
func HandleDocumentURL(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Require admin session
		_, _, err := GetAdminSession(app, r)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}
		var req document.UploadURLRequest
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		doc, err := app.UploadURL(req)
		if err != nil {
			errlog.Logf("[API] URL upload rejected url=%q: %v", req.URL, err)
			WriteError(w, http.StatusBadRequest, err.Error())
			return
		}
		WriteJSON(w, http.StatusOK, doc)
	}
}

// HandlePublicDocumentDownload allows regular users to download source documents
// if the product has allow_download enabled and the document type is downloadable.
func HandlePublicDocumentDownload(app *App) http.HandlerFunc {
	downloadableTypes := map[string]bool{
		"pdf": true, "doc": true, "docx": true, "word": true,
		"xls": true, "xlsx": true, "excel": true,
		"ppt": true, "pptx": true,
		"mp4": true, "avi": true, "mkv": true, "mov": true, "webm": true,
		"video": true,
	}
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		// Require user session (support token in query param for direct download links)
		authHeader := r.Header.Get("Authorization")
		token := strings.TrimPrefix(authHeader, "Bearer ")
		if token == "" || token == authHeader {
			token = r.URL.Query().Get("token")
		}
		if token == "" {
			WriteError(w, http.StatusUnauthorized, "未登录")
			return
		}
		session, sErr := app.sessionManager.ValidateSession(token)
		if sErr != nil {
			WriteError(w, http.StatusUnauthorized, "会话已过期")
			return
		}
		_ = session
		docID := strings.TrimPrefix(r.URL.Path, "/api/documents/public-download/")
		if docID == "" || !IsValidHexID(docID) {
			WriteError(w, http.StatusBadRequest, "invalid document ID")
			return
		}
		productID := r.URL.Query().Get("product_id")
		if productID == "" {
			WriteError(w, http.StatusBadRequest, "product_id is required")
			return
		}
		// Check product allows download
		p, pErr := app.GetProduct(productID)
		if pErr != nil || p == nil || !p.AllowDownload {
			WriteError(w, http.StatusForbidden, "该产品不允许下载参考文档")
			return
		}
		// Check document type is downloadable
		docInfo, dErr := app.GetDocumentInfo(docID)
		if dErr != nil {
			WriteError(w, http.StatusNotFound, "文档未找到")
			return
		}
		docType := strings.ToLower(docInfo.Type)
		if !downloadableTypes[docType] {
			WriteError(w, http.StatusForbidden, "该文档类型不支持下载")
			return
		}
		// Verify document belongs to the product
		if docInfo.ProductID != productID && docInfo.ProductID != "" {
			WriteError(w, http.StatusForbidden, "文档不属于该产品")
			return
		}
		filePath, fileName, fErr := app.docManager.GetFilePath(docID)
		if fErr != nil {
			WriteError(w, http.StatusNotFound, "文件未找到")
			return
		}
		// Verify file path stays within expected data directory
		absPath, _ := filepath.Abs(filePath)
		absDataDir, _ := filepath.Abs(filepath.Join(".", "data"))
		if !strings.HasPrefix(absPath, absDataDir+string(filepath.Separator)) {
			WriteError(w, http.StatusForbidden, "forbidden")
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

// HandleDocumentByID handles GET (download) and DELETE for a specific document.
func HandleDocumentByID(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		// Extract path after /api/documents/
		path := strings.TrimPrefix(r.URL.Path, "/api/documents/")
		if path == "" || path == r.URL.Path {
			WriteError(w, http.StatusBadRequest, "missing document ID")
			return
		}

		// Handle /api/documents/{id}/download
		if strings.HasSuffix(path, "/download") {
			docID := strings.TrimSuffix(path, "/download")
			if !IsValidHexID(docID) {
				WriteError(w, http.StatusBadRequest, "invalid document ID")
				return
			}
			if r.Method != http.MethodGet {
				WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			// Require admin session for downloads
			_, _, err := GetAdminSession(app, r)
			if err != nil {
				WriteError(w, http.StatusUnauthorized, err.Error())
				return
			}
			filePath, fileName, err := app.docManager.GetFilePath(docID)
			if err != nil {
				WriteError(w, http.StatusNotFound, "文件未找到")
				return
			}
			// Verify file path stays within expected data directory
			absPath, _ := filepath.Abs(filePath)
			absDataDir, _ := filepath.Abs(filepath.Join(".", "data"))
			if !strings.HasPrefix(absPath, absDataDir+string(filepath.Separator)) {
				WriteError(w, http.StatusForbidden, "forbidden")
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

		// Handle /api/documents/{id}/review
		if strings.HasSuffix(path, "/review") {
			docID := strings.TrimSuffix(path, "/review")
			if !IsValidHexID(docID) {
				WriteError(w, http.StatusBadRequest, "invalid document ID")
				return
			}
			if r.Method != http.MethodGet {
				WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
				return
			}
			_, _, err := GetAdminSession(app, r)
			if err != nil {
				WriteError(w, http.StatusUnauthorized, err.Error())
				return
			}
			review, err := app.GetDocumentReview(docID)
			if err != nil {
				WriteError(w, http.StatusNotFound, "文档未找到")
				return
			}
			WriteJSON(w, http.StatusOK, review)
			return
		}

		// Handle DELETE /api/documents/{id}
		docID := path
		if !IsValidHexID(docID) {
			WriteError(w, http.StatusBadRequest, "invalid document ID")
			return
		}
		if r.Method != http.MethodDelete {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		// Require admin session for deletion
		_, _, err := GetAdminSession(app, r)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, err.Error())
			return
		}

		if err := app.DeleteDocument(docID); err != nil {
			log.Printf("[Documents] delete error for %s: %v", docID, err)
			errlog.Logf("[Documents] delete failed for doc=%s: %v", docID, err)
			WriteError(w, http.StatusInternalServerError, "删除文档失败")
			return
		}
		WriteJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
	}
}

// HandleBatchImport handles batch file import via SSE (Server-Sent Events).
func HandleBatchImport(app *App) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			WriteError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}

		// Require admin session with batch_import permission
		userID, role, err := GetAdminSession(app, r)
		if err != nil {
			WriteError(w, http.StatusUnauthorized, err.Error())
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
				WriteError(w, http.StatusForbidden, "无批量导入权限")
				return
			}
		}

		var req struct {
			Path      string `json:"path"`
			ProductID string `json:"product_id"`
		}
		if err := ReadJSONBody(r, &req); err != nil {
			WriteError(w, http.StatusBadRequest, "invalid request body")
			return
		}
		if req.Path == "" {
			WriteError(w, http.StatusBadRequest, "path is required")
			return
		}

		// Validate product ID if provided
		if req.ProductID != "" {
			p, err := app.productService.GetByID(req.ProductID)
			if err != nil || p == nil {
				WriteError(w, http.StatusBadRequest, fmt.Sprintf("产品不存在 (ID: %s)", req.ProductID))
				return
			}
		}

		// Validate path exists
		info, err := os.Stat(req.Path)
		if err != nil {
			WriteError(w, http.StatusBadRequest, fmt.Sprintf("无法访问路径: %v", err))
			return
		}

		// Collect files
		var files []string
		if !info.IsDir() {
			ext := strings.ToLower(filepath.Ext(req.Path))
			if _, ok := SupportedExtensions[ext]; ok {
				files = append(files, req.Path)
			} else {
				WriteError(w, http.StatusBadRequest, "不支持的文件格式")
				return
			}
		} else {
			filepath.Walk(req.Path, func(path string, fi os.FileInfo, err error) error {
				if err != nil || fi.IsDir() {
					return nil
				}
				ext := strings.ToLower(filepath.Ext(fi.Name()))
				if _, ok := SupportedExtensions[ext]; ok {
					files = append(files, path)
				}
				return nil
			})
		}

		if len(files) == 0 {
			WriteError(w, http.StatusBadRequest, "未找到支持的文件")
			return
		}

		// Set SSE headers
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		w.Header().Set("X-Accel-Buffering", "no")

		flusher, ok := w.(http.Flusher)
		if !ok {
			WriteError(w, http.StatusInternalServerError, "streaming not supported")
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
			// Check if client disconnected before processing next file
			select {
			case <-r.Context().Done():
				sendSSE("done", map[string]interface{}{
					"total":        len(files),
					"success":      success,
					"failed":       failed,
					"failed_files": failedFiles,
					"cancelled":    true,
				})
				return
			default:
			}

			fileName := filepath.Base(filePath)
			ext := strings.ToLower(filepath.Ext(fileName))
			fileType := SupportedExtensions[ext]

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
