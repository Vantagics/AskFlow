// Package document provides the Document Manager for handling document upload,
// processing, deletion, and listing in the helpdesk system.
package document

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"helpdesk/internal/chunker"
	"helpdesk/internal/config"
	"helpdesk/internal/embedding"
	"helpdesk/internal/parser"
	"helpdesk/internal/vectorstore"
	"helpdesk/internal/video"
)

// supportedFileTypes lists the file types accepted for upload.
var supportedFileTypes = map[string]bool{
	"pdf":      true,
	"word":     true,
	"excel":    true,
	"ppt":      true,
	"markdown": true,
	"html":     true,
	"mp4":      true,
	"avi":      true,
	"mkv":      true,
	"mov":      true,
	"webm":     true,
}

// videoFileTypes identifies which file types are video formats.
var videoFileTypes = map[string]bool{
	"mp4": true, "avi": true, "mkv": true, "mov": true, "webm": true,
}

// DocumentManager orchestrates document upload, processing, and lifecycle management.
type DocumentManager struct {
	parser           *parser.DocumentParser
	chunker          *chunker.TextChunker
	mu               sync.RWMutex
	embeddingService embedding.EmbeddingService
	vectorStore      vectorstore.VectorStore
	db               *sql.DB
	httpClient       *http.Client
	videoConfig      config.VideoConfig
	// validateURL is a hook for URL validation (SSRF protection).
	// Defaults to validateExternalURL. Tests can override to allow localhost.
	validateURL func(string) error
}

// DocumentInfo holds metadata about a document stored in the system.
type DocumentInfo struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Status    string    `json:"status"` // "processing", "success", "failed"
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	ProductID string    `json:"product_id"`
}


// UploadFileRequest represents a file upload request.
type UploadFileRequest struct {
	FileName  string `json:"file_name"`
	FileData  []byte `json:"file_data"`
	FileType  string `json:"file_type"`
	ProductID string `json:"product_id"`
}

func (dm *DocumentManager) UploadFile(req UploadFileRequest) (*DocumentInfo, error) {
	fileType := strings.ToLower(req.FileType)
	if !supportedFileTypes[fileType] {
		return nil, fmt.Errorf("不支持的文件格式")
	}

	docID, err := generateID()
	if err != nil {
		return nil, err
	}

	doc := &DocumentInfo{
		ID:        docID,
		Name:      req.FileName,
		Type:      fileType,
		Status:    "processing",
		CreatedAt: time.Now(),
		ProductID: req.ProductID,
	}

	if err := dm.insertDocument(doc); err != nil {
		return nil, fmt.Errorf("failed to insert document record: %w", err)
	}

	// Save original file to disk
	if err := dm.saveOriginalFile(docID, req.FileName, req.FileData); err != nil {
		// Non-fatal: log but continue processing
		fmt.Printf("Warning: failed to save original file: %v\n", err)
	}

	// For video files, process asynchronously to avoid HTTP timeout
	if videoFileTypes[fileType] {
		go func() {
			if processErr := dm.processVideo(docID, req.FileName, req.FileData, req.ProductID); processErr != nil {
				dm.updateDocumentStatus(docID, "failed", processErr.Error())
				fmt.Printf("Video processing failed for %s: %v\n", docID, processErr)
			} else {
				dm.updateDocumentStatus(docID, "success", "")
				fmt.Printf("Video processing completed for %s\n", docID)
			}
		}()
		return doc, nil
	}

	// Non-video files: process synchronously
	if processErr := dm.processFile(docID, req.FileName, req.FileData, fileType, req.ProductID); processErr != nil {
		dm.updateDocumentStatus(docID, "failed", processErr.Error())
		doc.Status = "failed"
		doc.Error = processErr.Error()
		return doc, nil
	}

	dm.updateDocumentStatus(docID, "success", "")
	doc.Status = "success"
	return doc, nil
}


// UploadURLRequest represents a URL upload request.
type UploadURLRequest struct {
	URL       string `json:"url"`
	ProductID string `json:"product_id"`
}

// NewDocumentManager creates a new DocumentManager with the given dependencies.
func NewDocumentManager(
	p *parser.DocumentParser,
	c *chunker.TextChunker,
	es embedding.EmbeddingService,
	vs vectorstore.VectorStore,
	db *sql.DB,
) *DocumentManager {
	return &DocumentManager{
		parser:           p,
		chunker:          c,
		embeddingService: es,
		vectorStore:      vs,
		db:               db,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
		validateURL: validateExternalURL,
	}
}

// UpdateEmbeddingService replaces the embedding service (used after config change).
func (dm *DocumentManager) UpdateEmbeddingService(es embedding.EmbeddingService) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	dm.embeddingService = es
}

// SetVideoConfig updates the video processing configuration.
func (dm *DocumentManager) SetVideoConfig(cfg config.VideoConfig) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	dm.videoConfig = cfg
}

// generateID creates a random UUID-like hex string.
func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// contentHash computes a SHA-256 hash of the given text for deduplication.
func contentHash(text string) string {
	h := sha256.Sum256([]byte(text))
	return hex.EncodeToString(h[:])
}

// findDocumentByContentHash checks if a document with the same content hash already exists.
// Returns the document ID if found, empty string otherwise.
func (dm *DocumentManager) findDocumentByContentHash(hash string) string {
	var docID string
	err := dm.db.QueryRow(
		`SELECT id FROM documents WHERE content_hash = ? AND status = 'success' LIMIT 1`, hash,
	).Scan(&docID)
	if err != nil {
		return ""
	}
	return docID
}

// getExistingChunkEmbeddings looks up embeddings for chunk texts that already exist in the DB.
// Returns a map of chunk_text -> embedding vector for reuse, saving API calls.
// Uses batch queries to minimize database round-trips.
func (dm *DocumentManager) getExistingChunkEmbeddings(texts []string) map[string][]float64 {
	result := make(map[string][]float64)
	if len(texts) == 0 {
		return result
	}

	// Batch query in groups of 100 to stay within SQLite variable limits
	const batchSize = 100
	for i := 0; i < len(texts); i += batchSize {
		end := i + batchSize
		if end > len(texts) {
			end = len(texts)
		}
		batch := texts[i:end]

		placeholders := make([]string, len(batch))
		args := make([]interface{}, len(batch))
		for j, t := range batch {
			placeholders[j] = "?"
			args[j] = t
		}

		query := fmt.Sprintf(
			`SELECT chunk_text, embedding FROM chunks WHERE chunk_text IN (%s)`,
			strings.Join(placeholders, ","),
		)
		rows, err := dm.db.Query(query, args...)
		if err != nil {
			continue
		}
		for rows.Next() {
			var chunkText string
			var embeddingBytes []byte
			if err := rows.Scan(&chunkText, &embeddingBytes); err == nil && len(embeddingBytes) > 0 {
				vec := vectorstore.DeserializeVector(embeddingBytes)
				if len(vec) > 0 {
					result[chunkText] = vec
				}
			}
		}
		rows.Close()
	}
	return result
}

// UploadURL fetches the content at the given URL, chunks it, generates embeddings,
// and stores everything. The document type is recorded as "url".
func (dm *DocumentManager) UploadURL(req UploadURLRequest) (*DocumentInfo, error) {
	if req.URL == "" {
		return nil, fmt.Errorf("URL不能为空")
	}

	docID, err := generateID()
	if err != nil {
		return nil, err
	}

	doc := &DocumentInfo{
		ID:        docID,
		Name:      req.URL,
		Type:      "url",
		Status:    "processing",
		CreatedAt: time.Now(),
		ProductID: req.ProductID,
	}

	if err := dm.insertDocument(doc); err != nil {
		return nil, fmt.Errorf("failed to insert document record: %w", err)
	}

	// Fetch → Chunk → Embed → Store
	if err := dm.processURL(docID, req.URL, req.ProductID); err != nil {
		dm.updateDocumentStatus(docID, "failed", err.Error())
		doc.Status = "failed"
		doc.Error = err.Error()
		return doc, nil
	}

	dm.updateDocumentStatus(docID, "success", "")
	doc.Status = "success"
	return doc, nil
}

// DeleteDocument removes a document's vectors from the vector store, its
// record from the documents table, and the original file from disk.
func (dm *DocumentManager) DeleteDocument(docID string) error {
	// Validate docID to prevent path traversal in file deletion
	if strings.ContainsAny(docID, "/\\..") {
		return fmt.Errorf("invalid document ID")
	}

	if err := dm.vectorStore.DeleteByDocID(docID); err != nil {
		return fmt.Errorf("failed to delete vectors: %w", err)
	}
	// Delete associated video_segments records (cascade cleanup for video documents)
	_, err := dm.db.Exec(`DELETE FROM video_segments WHERE document_id = ?`, docID)
	if err != nil {
		return fmt.Errorf("failed to delete video segments: %w", err)
	}
	_, err = dm.db.Exec(`DELETE FROM documents WHERE id = ?`, docID)
	if err != nil {
		return fmt.Errorf("failed to delete document record: %w", err)
	}
	// Remove original file directory
	dir := filepath.Join(".", "data", "uploads", docID)
	os.RemoveAll(dir)
	return nil
}

// ListDocuments returns all documents ordered by creation time descending.
func (dm *DocumentManager) ListDocuments(productID string) ([]DocumentInfo, error) {
	var rows *sql.Rows
	var err error

	if productID != "" {
		rows, err = dm.db.Query(
			`SELECT id, name, type, status, error, created_at, product_id FROM documents WHERE product_id = ? OR product_id = '' ORDER BY created_at DESC`,
			productID,
		)
	} else {
		rows, err = dm.db.Query(`SELECT id, name, type, status, error, created_at, product_id FROM documents ORDER BY created_at DESC`)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to query documents: %w", err)
	}
	defer rows.Close()

	var docs []DocumentInfo
	for rows.Next() {
		var d DocumentInfo
		var errStr sql.NullString
		var createdAt sql.NullTime
		if err := rows.Scan(&d.ID, &d.Name, &d.Type, &d.Status, &errStr, &createdAt, &d.ProductID); err != nil {
			return nil, fmt.Errorf("failed to scan document row: %w", err)
		}
		if errStr.Valid {
			d.Error = errStr.String
		}
		if createdAt.Valid {
			d.CreatedAt = createdAt.Time
		}
		docs = append(docs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating document rows: %w", err)
	}
	return docs, nil
}


// processFile parses a file, chunks the text, embeds, and stores vectors.
// It performs content-level deduplication: if a document with the same content
// hash already exists, the upload is skipped to save API calls.
func (dm *DocumentManager) processFile(docID, docName string, fileData []byte, fileType string, productID string) error {
	result, err := dm.parser.Parse(fileData, fileType)
	if err != nil {
		return fmt.Errorf("parse error: %w", err)
	}
	if result.Text == "" && len(result.Images) == 0 {
		return fmt.Errorf("文档内容为空")
	}

	// Document-level dedup: check if identical content already exists
	if result.Text != "" {
		hash := contentHash(result.Text)
		if existingID := dm.findDocumentByContentHash(hash); existingID != "" {
			return fmt.Errorf("文档内容重复，与已有文档相同 (ID: %s)", existingID)
		}
		// Store the content hash for future dedup checks
		dm.db.Exec(`UPDATE documents SET content_hash = ? WHERE id = ?`, hash, docID)
	}

	// Store text chunks
	if result.Text != "" {
		if err := dm.chunkEmbedStore(docID, docName, result.Text, productID); err != nil {
			return err
		}
	}

	// Store image embeddings
	for i, img := range result.Images {
		imgURL := img.URL
		// For embedded images (e.g. from PDF), save to disk and generate URL
		if imgURL == "" && len(img.Data) > 0 {
			savedURL, saveErr := dm.saveExtractedImage(img.Data)
			if saveErr != nil {
				fmt.Printf("Warning: failed to save extracted image %d: %v\n", i, saveErr)
				continue
			}
			imgURL = savedURL
		}
		if imgURL == "" {
			continue
		}
		vec, err := dm.embeddingService.EmbedImageURL(imgURL)
		if err != nil {
			// Non-fatal: skip images that fail to embed
			fmt.Printf("Warning: failed to embed image %d (%s): %v\n", i, img.Alt, err)
			continue
		}
		imgChunk := []vectorstore.VectorChunk{{
			ChunkText:    fmt.Sprintf("[图片: %s]", img.Alt),
			ChunkIndex:   1000 + i, // offset to avoid collision with text chunks
			DocumentID:   docID,
			DocumentName: docName,
			Vector:       vec,
			ImageURL:     imgURL,
			ProductID:    productID,
		}}
		if err := dm.vectorStore.Store(docID, imgChunk); err != nil {
			fmt.Printf("Warning: failed to store image vector %d: %v\n", i, err)
		}
	}

	return nil
}

// processVideo handles video file processing: extract transcript and keyframes,
// embed them, store vectors, and create video_segments records.
func (dm *DocumentManager) processVideo(docID, docName string, fileData []byte, productID string) error {
	dm.mu.RLock()
	cfg := dm.videoConfig
	dm.mu.RUnlock()

	// Check VideoConfig is configured
	if cfg.FFmpegPath == "" && cfg.RapidSpeechPath == "" {
		return fmt.Errorf("视频检索功能未启用，请先在设置中配置 ffmpeg 和 rapidspeech 路径")
	}

	// Save video file to disk
	uploadDir := filepath.Join(".", "data", "uploads", docID)
	if err := os.MkdirAll(uploadDir, 0755); err != nil {
		return fmt.Errorf("创建上传目录失败: %w", err)
	}
	// Sanitize filename: replace characters illegal on Windows (: * ? " < > |)
	safeName := filepath.Base(docName)
	safeName = strings.NewReplacer(":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_").Replace(safeName)
	if safeName == "" || safeName == "." || safeName == ".." {
		safeName = docID + ".video"
	}
	videoPath := filepath.Join(uploadDir, safeName)
	if err := os.WriteFile(videoPath, fileData, 0644); err != nil {
		return fmt.Errorf("保存视频文件失败: %w", err)
	}

	// Create video parser and parse
	vp := video.NewParser(cfg)
	parseResult, err := vp.Parse(videoPath)
	if err != nil {
		return fmt.Errorf("视频解析失败: %w", err)
	}

	chunkIndex := 0

	// Process transcript: join all segment texts → chunk → embed → store → create video_segments
	if len(parseResult.Transcript) > 0 {
		// Join all transcript text
		var fullText strings.Builder
		for _, seg := range parseResult.Transcript {
			if fullText.Len() > 0 {
				fullText.WriteString(" ")
			}
			fullText.WriteString(strings.TrimSpace(seg.Text))
		}

		if fullText.Len() > 0 {
			// Chunk the transcript text
			chunks := dm.chunker.Split(fullText.String(), docID)
			if len(chunks) > 0 {
				// Collect chunk texts for embedding
				texts := make([]string, len(chunks))
				for i, c := range chunks {
					texts[i] = c.Text
				}

				// Embed
				embeddings, err := dm.embeddingService.EmbedBatch(texts)
				if err != nil {
					return fmt.Errorf("转录文本嵌入失败: %w", err)
				}

				// Store vectors
				vectorChunks := make([]vectorstore.VectorChunk, len(chunks))
				for i, c := range chunks {
					vectorChunks[i] = vectorstore.VectorChunk{
						ChunkText:    c.Text,
						ChunkIndex:   chunkIndex + i,
						DocumentID:   docID,
						DocumentName: docName,
						Vector:       embeddings[i],
						ProductID:    productID,
					}
				}
				if err := dm.vectorStore.Store(docID, vectorChunks); err != nil {
					return fmt.Errorf("转录向量存储失败: %w", err)
				}

				// Create video_segments records for each transcript chunk
				for i, c := range chunks {
					startTime, endTime := dm.mapChunkToTimeRange(c.Text, parseResult.Transcript)
					segID, err := generateID()
					if err != nil {
						return fmt.Errorf("生成 segment ID 失败: %w", err)
					}
					chunkID := fmt.Sprintf("%s-%d", docID, chunkIndex+i)
					_, err = dm.db.Exec(
						`INSERT INTO video_segments (id, document_id, segment_type, start_time, end_time, content, chunk_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
						segID, docID, "transcript", startTime, endTime, c.Text, chunkID,
					)
					if err != nil {
						return fmt.Errorf("插入 video_segments 记录失败: %w", err)
					}
				}

				chunkIndex += len(chunks)
			}
		}
	}

	// Process keyframes: base64 → EmbedImageURL → store → create video_segments
	for i, kf := range parseResult.Keyframes {
		if len(kf.Data) == 0 {
			fmt.Printf("Warning: keyframe %d has no data\n", i)
			continue
		}

		// Base64 encode as data URL for EmbedImageURL
		b64 := base64.StdEncoding.EncodeToString(kf.Data)
		dataURL := "data:image/jpeg;base64," + b64

		vec, err := dm.embeddingService.EmbedImageURL(dataURL)
		if err != nil {
			// Non-fatal: skip frames that fail to embed (Requirement 4.5)
			fmt.Printf("Warning: failed to embed keyframe %d (%.1fs): %v\n", i, kf.Timestamp, err)
			continue
		}

		frameChunkIndex := chunkIndex + i
		frameChunk := []vectorstore.VectorChunk{{
			ChunkText:    fmt.Sprintf("[视频关键帧: %.1fs]", kf.Timestamp),
			ChunkIndex:   frameChunkIndex,
			DocumentID:   docID,
			DocumentName: docName,
			Vector:       vec,
			ImageURL:     dataURL,
			ProductID:    productID,
		}}
		if err := dm.vectorStore.Store(docID, frameChunk); err != nil {
			fmt.Printf("Warning: failed to store keyframe vector %d: %v\n", i, err)
			continue
		}

		// Create video_segments record for keyframe
		segID, err := generateID()
		if err != nil {
			fmt.Printf("Warning: failed to generate segment ID for keyframe %d: %v\n", i, err)
			continue
		}
		chunkID := fmt.Sprintf("%s-%d", docID, frameChunkIndex)
		_, err = dm.db.Exec(
			`INSERT INTO video_segments (id, document_id, segment_type, start_time, end_time, content, chunk_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
			segID, docID, "keyframe", kf.Timestamp, kf.Timestamp, kf.FilePath, chunkID,
		)
		if err != nil {
			fmt.Printf("Warning: failed to insert keyframe video_segment: %v\n", err)
		}
	}

	return nil
}

// mapChunkToTimeRange maps a chunk text back to the transcript segments to determine
// the time range covered by the chunk. Returns the start time of the first matching
// segment and the end time of the last matching segment.
func (dm *DocumentManager) mapChunkToTimeRange(chunkText string, segments []video.TranscriptSegment) (float64, float64) {
	if len(segments) == 0 {
		return 0, 0
	}

	startTime := segments[len(segments)-1].End
	endTime := segments[0].Start
	found := false

	for _, seg := range segments {
		segText := strings.TrimSpace(seg.Text)
		if segText == "" {
			continue
		}
		if strings.Contains(chunkText, segText) {
			found = true
			if seg.Start < startTime {
				startTime = seg.Start
			}
			if seg.End > endTime {
				endTime = seg.End
			}
		}
	}

	if !found {
		// Fallback: use the full range of all segments
		return segments[0].Start, segments[len(segments)-1].End
	}

	return startTime, endTime
}

// URLPreviewResult holds the preview of fetched URL content.
type URLPreviewResult struct {
	URL   string   `json:"url"`
	Text  string   `json:"text"`
	Images []string `json:"images,omitempty"` // image URLs found in HTML
}

// PreviewURL fetches and parses URL content for user preview before committing.
func (dm *DocumentManager) PreviewURL(rawURL string) (*URLPreviewResult, error) {
	if rawURL == "" {
		return nil, fmt.Errorf("URL不能为空")
	}
	if err := dm.validateURL(rawURL); err != nil {
		return nil, err
	}

	resp, err := dm.httpClient.Get(rawURL)
	if err != nil {
		return nil, fmt.Errorf("无法访问该URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == 403 || resp.StatusCode == 401 {
		return nil, fmt.Errorf("访问被拒绝 (HTTP %d)，该网站可能禁止抓取", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("请求失败 (HTTP %d)", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10MB limit
	if err != nil {
		return nil, fmt.Errorf("读取内容失败: %w", err)
	}

	text := strings.TrimSpace(string(body))
	if text == "" {
		return nil, fmt.Errorf("URL内容为空")
	}

	result := &URLPreviewResult{URL: rawURL}

	contentType := resp.Header.Get("Content-Type")
	isHTML := strings.Contains(contentType, "text/html") || looksLikeHTML(text)
	if isHTML {
		parsed, err := dm.parser.ParseWithBaseURL(body, "html", rawURL)
		if err != nil {
			return nil, fmt.Errorf("HTML解析失败: %w", err)
		}
		result.Text = parsed.Text
		for _, img := range parsed.Images {
			if img.URL != "" {
				result.Images = append(result.Images, img.URL)
			}
		}
	} else {
		result.Text = text
	}

	if result.Text == "" {
		return nil, fmt.Errorf("解析后内容为空")
	}

	// Truncate preview text to 5000 chars
	if len(result.Text) > 5000 {
		result.Text = result.Text[:5000] + "\n...(内容已截断，共 " + fmt.Sprintf("%d", len(text)) + " 字符)"
	}

	return result, nil
}

// processURL fetches URL content and processes it as plain text.
func (dm *DocumentManager) processURL(docID, url string, productID string) error {
	if err := dm.validateURL(url); err != nil {
		return err
	}

	resp, err := dm.httpClient.Get(url)
	if err != nil {
		return fmt.Errorf("failed to fetch URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("URL returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10MB limit
	if err != nil {
		return fmt.Errorf("failed to read URL content: %w", err)
	}

	text := strings.TrimSpace(string(body))
	if text == "" {
		return fmt.Errorf("URL内容为空")
	}

	// Detect HTML content and parse it with image extraction
	contentType := resp.Header.Get("Content-Type")
	isHTML := strings.Contains(contentType, "text/html") || looksLikeHTML(text)
	if isHTML {
		result, err := dm.parser.ParseWithBaseURL(body, "html", url)
		if err != nil {
			return fmt.Errorf("HTML parse error: %w", err)
		}
		// Document-level dedup for HTML content
		if result.Text != "" {
			hash := contentHash(result.Text)
			if existingID := dm.findDocumentByContentHash(hash); existingID != "" {
				return fmt.Errorf("文档内容重复，与已有文档相同 (ID: %s)", existingID)
			}
			dm.db.Exec(`UPDATE documents SET content_hash = ? WHERE id = ?`, hash, docID)
		}
		if result.Text != "" {
			if err := dm.chunkEmbedStore(docID, url, result.Text, productID); err != nil {
				return err
			}
		}
		// Embed images found in the HTML
		for i, img := range result.Images {
			if img.URL == "" {
				continue
			}
			vec, err := dm.embeddingService.EmbedImageURL(img.URL)
			if err != nil {
				fmt.Printf("Warning: failed to embed HTML image %d (%s): %v\n", i, img.Alt, err)
				continue
			}
			imgChunk := []vectorstore.VectorChunk{{
				ChunkText:    fmt.Sprintf("[图片: %s]", img.Alt),
				ChunkIndex:   1000 + i,
				DocumentID:   docID,
				DocumentName: url,
				Vector:       vec,
				ImageURL:     img.URL,
				ProductID:    productID,
			}}
			if err := dm.vectorStore.Store(docID, imgChunk); err != nil {
				fmt.Printf("Warning: failed to store HTML image vector %d: %v\n", i, err)
			}
		}
		return nil
	}

	// Document-level dedup for plain text URL content
	hash := contentHash(text)
	if existingID := dm.findDocumentByContentHash(hash); existingID != "" {
		return fmt.Errorf("文档内容重复，与已有文档相同 (ID: %s)", existingID)
	}
	dm.db.Exec(`UPDATE documents SET content_hash = ? WHERE id = ?`, hash, docID)

	return dm.chunkEmbedStore(docID, url, text, productID)
}

// validateExternalURL checks that a URL is a valid external HTTP(S) URL
// to prevent SSRF attacks against internal services.
func validateExternalURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("URL不能为空")
	}
	lower := strings.ToLower(rawURL)
	if !strings.HasPrefix(lower, "http://") && !strings.HasPrefix(lower, "https://") {
		return fmt.Errorf("仅支持 HTTP/HTTPS 协议")
	}
	// Block common internal/private hostnames and IPs
	host := strings.TrimPrefix(strings.TrimPrefix(lower, "https://"), "http://")
	if idx := strings.Index(host, "/"); idx >= 0 {
		host = host[:idx]
	}
	if idx := strings.Index(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	blockedHosts := []string{"localhost", "127.0.0.1", "0.0.0.0", "[::1]", "169.254.169.254", "metadata.google.internal"}
	for _, blocked := range blockedHosts {
		if host == blocked {
			return fmt.Errorf("不允许访问内部地址")
		}
	}
	// Block private IP ranges
	if strings.HasPrefix(host, "10.") || strings.HasPrefix(host, "192.168.") || strings.HasPrefix(host, "172.") {
		return fmt.Errorf("不允许访问内部网络地址")
	}
	return nil
}

// looksLikeHTML checks if content appears to be HTML by looking for common HTML markers.
func looksLikeHTML(content string) bool {
	lower := strings.ToLower(content[:min(512, len(content))])
	return strings.Contains(lower, "<!doctype html") ||
		strings.Contains(lower, "<html") ||
		strings.Contains(lower, "<head") ||
		strings.Contains(lower, "<body")
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// chunkEmbedStore splits text into chunks, embeds them in batch, and stores vectors.
// It performs chunk-level deduplication: if a chunk with identical text already exists
// in the database, its embedding is reused instead of calling the embedding API.
func (dm *DocumentManager) chunkEmbedStore(docID, docName, text string, productID string) error {
	chunks := dm.chunker.Split(text, docID)
	if len(chunks) == 0 {
		return fmt.Errorf("分块结果为空")
	}

	// Collect chunk texts
	texts := make([]string, len(chunks))
	for i, c := range chunks {
		texts[i] = c.Text
	}

	// Chunk-level dedup: look up existing embeddings for identical chunk texts
	existingEmbeddings := dm.getExistingChunkEmbeddings(texts)

	// Identify which chunks need new embeddings
	var newTexts []string
	var newIndices []int
	for i, t := range texts {
		if _, ok := existingEmbeddings[t]; !ok {
			newTexts = append(newTexts, t)
			newIndices = append(newIndices, i)
		}
	}

	// Only call embedding API for chunks that don't have existing embeddings
	if len(newTexts) > 0 {
		newEmbeddings, err := dm.embeddingService.EmbedBatch(newTexts)
		if err != nil {
			return fmt.Errorf("embedding error: %w", err)
		}
		for j, idx := range newIndices {
			existingEmbeddings[texts[idx]] = newEmbeddings[j]
		}
	}

	if len(newTexts) < len(texts) {
		fmt.Printf("去重: %d/%d 个分块复用了已有向量，节省 %d 次API调用\n",
			len(texts)-len(newTexts), len(texts), len(texts)-len(newTexts))
	}

	// Build VectorChunks for storage
	vectorChunks := make([]vectorstore.VectorChunk, len(chunks))
	for i, c := range chunks {
		vectorChunks[i] = vectorstore.VectorChunk{
			ChunkText:    c.Text,
			ChunkIndex:   c.Index,
			DocumentID:   docID,
			DocumentName: docName,
			Vector:       existingEmbeddings[c.Text],
			ProductID:    productID,
		}
	}

	if err := dm.vectorStore.Store(docID, vectorChunks); err != nil {
		return fmt.Errorf("vector store error: %w", err)
	}
	return nil
}

// insertDocument inserts a new document record into the documents table.
func (dm *DocumentManager) insertDocument(doc *DocumentInfo) error {
	_, err := dm.db.Exec(
		`INSERT INTO documents (id, name, type, status, error, created_at, product_id) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		doc.ID, doc.Name, doc.Type, doc.Status, doc.Error, doc.CreatedAt, doc.ProductID,
	)
	return err
}

// updateDocumentStatus updates the status and error fields of a document.
func (dm *DocumentManager) updateDocumentStatus(docID, status, errMsg string) {
	dm.db.Exec(`UPDATE documents SET status = ?, error = ? WHERE id = ?`, status, errMsg, docID)
}

// saveOriginalFile saves the uploaded file to data/uploads/{docID}/{filename}.
func (dm *DocumentManager) saveOriginalFile(docID, filename string, data []byte) error {
	// Sanitize filename to prevent path traversal
	filename = filepath.Base(filename)
	if filename == "." || filename == ".." {
		return fmt.Errorf("invalid filename")
	}

	dir := filepath.Join(".", "data", "uploads", docID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create upload dir: %w", err)
	}
	filePath := filepath.Join(dir, filename)
	return os.WriteFile(filePath, data, 0644)
}

// saveExtractedImage saves embedded image data (e.g. from PDF) to data/images/
// and returns the URL path for accessing it.
func (dm *DocumentManager) saveExtractedImage(data []byte) (string, error) {
	// Detect image format from magic bytes
	ext := ".png"
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		ext = ".jpg"
	} else if len(data) >= 4 && string(data[:4]) == "\x89PNG" {
		ext = ".png"
	} else if len(data) >= 4 && string(data[:4]) == "RIFF" {
		ext = ".webp"
	} else if len(data) >= 3 && string(data[:3]) == "GIF" {
		ext = ".gif"
	}

	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate image ID: %w", err)
	}
	filename := hex.EncodeToString(b) + ext

	imgDir := filepath.Join(".", "data", "images")
	if err := os.MkdirAll(imgDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create image dir: %w", err)
	}
	if err := os.WriteFile(filepath.Join(imgDir, filename), data, 0644); err != nil {
		return "", fmt.Errorf("failed to write image: %w", err)
	}

	return "/api/images/" + filename, nil
}

// GetFilePath returns the path to the original uploaded file for a document.
// Returns empty string if the file doesn't exist.
func (dm *DocumentManager) GetFilePath(docID string) (string, string, error) {
	// Validate docID to prevent path traversal
	if strings.ContainsAny(docID, "/\\..") {
		return "", "", fmt.Errorf("invalid document ID")
	}

	var name string
	err := dm.db.QueryRow(`SELECT name FROM documents WHERE id = ?`, docID).Scan(&name)
	if err != nil {
		return "", "", fmt.Errorf("document not found: %w", err)
	}

	dir := filepath.Join(".", "data", "uploads", docID)
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) == 0 {
		return "", name, fmt.Errorf("original file not found")
	}

	filePath := filepath.Join(dir, entries[0].Name())
	return filePath, entries[0].Name(), nil
}

// ChunkEmbedStore is a public wrapper around chunkEmbedStore for external callers.
func (dm *DocumentManager) ChunkEmbedStore(docID, docName, text string, productID string) error {
	return dm.chunkEmbedStore(docID, docName, text, productID)
}

// GetEmbeddingService returns the current embedding service.
func (dm *DocumentManager) GetEmbeddingService() embedding.EmbeddingService {
	dm.mu.RLock()
	defer dm.mu.RUnlock()
	return dm.embeddingService
}

// StoreChunks stores pre-built vector chunks into the vector store.
func (dm *DocumentManager) StoreChunks(docID string, chunks []vectorstore.VectorChunk) error {
	return dm.vectorStore.Store(docID, chunks)
}

// ProcessVideoForKnowledge is a public wrapper for processing video files in knowledge entries.
// It saves the video file to a permanent location and processes it for transcript and keyframes.
func (dm *DocumentManager) ProcessVideoForKnowledge(docID, docName string, fileData []byte, videoURL string, productID string) error {
	return dm.processVideo(docID, docName, fileData, productID)
}
