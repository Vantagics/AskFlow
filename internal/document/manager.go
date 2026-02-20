// Package document provides the Document Manager for handling document upload,
// processing, deletion, and listing in the askflow system.
package document

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"askflow/internal/chunker"
	"askflow/internal/config"
	"askflow/internal/embedding"
	"askflow/internal/errlog"
	"askflow/internal/parser"
	"askflow/internal/vectorstore"
	"askflow/internal/video"

	"golang.org/x/image/draw"
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

// LLMService defines the subset of LLM capabilities needed by DocumentManager.
type LLMService interface {
	GenerateWithImage(prompt string, context []string, question string, imageDataURL string) (string, error)
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
	llmService       LLMService
	// validateURL is a hook for URL validation (SSRF protection).
	// Defaults to validateExternalURL. Tests can override to allow localhost.
	validateURL func(string) error
}

// ImportStats holds statistics about the imported document content.
type ImportStats struct {
	TextChars  int `json:"text_chars"`
	ImageCount int `json:"image_count"`
}

// DocumentInfo holds metadata about a document stored in the system.
type DocumentInfo struct {
	ID        string       `json:"id"`
	Name      string       `json:"name"`
	Type      string       `json:"type"`
	Status    string       `json:"status"` // "processing", "success", "failed"
	Error     string       `json:"error,omitempty"`
	CreatedAt time.Time    `json:"created_at"`
	ProductID string       `json:"product_id"`
	Stats     *ImportStats `json:"stats,omitempty"`
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

	// Validate file name
	if req.FileName == "" {
		return nil, fmt.Errorf("文件名不能为空")
	}
	if len(req.FileName) > 500 {
		return nil, fmt.Errorf("文件名过长")
	}

	// Validate file data is not empty
	if len(req.FileData) == 0 {
		return nil, fmt.Errorf("文件内容为空")
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
		log.Printf("Warning: failed to save original file: %v", err)
		errlog.Logf("[Upload] failed to save original file %q (doc=%s): %v", req.FileName, docID, err)
	}

	// For video files and PDF files, process asynchronously to avoid HTTP timeout.
	// PDF files (especially scanned PDFs) may require per-page OCR via LLM vision API,
	// which can take a very long time for multi-page documents.
	if videoFileTypes[fileType] || fileType == "pdf" {
		go func() {
			defer func() {
				if r := recover(); r != nil {
					dm.updateDocumentStatus(docID, "failed", fmt.Sprintf("panic: %v", r))
					log.Printf("Async processing panic for %s: %v", docID, r)
				}
			}()

			// Use configurable timeout for async processing
			dm.mu.RLock()
			timeoutMin := dm.videoConfig.ProcessingTimeoutMin
			dm.mu.RUnlock()
			if timeoutMin <= 0 {
				timeoutMin = 120
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutMin)*time.Minute)
			defer cancel()

			done := make(chan error, 1)
			go func() {
				defer func() {
					if r := recover(); r != nil {
						done <- fmt.Errorf("panic in async processing: %v", r)
					}
				}()
				if videoFileTypes[fileType] {
					done <- dm.processVideo(docID, req.FileName, req.FileData, req.ProductID)
				} else {
					_, processErr := dm.processFile(docID, req.FileName, req.FileData, fileType, req.ProductID)
					done <- processErr
				}
			}()

			select {
			case processErr := <-done:
				if processErr != nil {
					dm.updateDocumentStatus(docID, "failed", processErr.Error())
					log.Printf("Async processing failed for %s: %v", docID, processErr)
					errlog.Logf("[Async] processing failed for doc=%s file=%q: %v", docID, req.FileName, processErr)
				} else {
					dm.updateDocumentStatus(docID, "success", "")
					log.Printf("Async processing completed for %s", docID)
				}
			case <-ctx.Done():
				dm.updateDocumentStatus(docID, "failed", fmt.Sprintf("文档处理超时（%d分钟）", timeoutMin))
				log.Printf("Async processing timed out for %s (%d min)", docID, timeoutMin)
				errlog.Logf("[Async] processing timed out for doc=%s file=%q (%d min)", docID, req.FileName, timeoutMin)
			}
		}()
		return doc, nil
	}

	// Non-video, non-PDF files: process synchronously
	stats, processErr := dm.processFile(docID, req.FileName, req.FileData, fileType, req.ProductID)
	if processErr != nil {
		dm.updateDocumentStatus(docID, "failed", processErr.Error())
		doc.Status = "failed"
		doc.Error = processErr.Error()
		errlog.Logf("[Upload] file processing failed for doc=%s file=%q type=%s: %v", docID, req.FileName, fileType, processErr)
		return doc, nil
	}

	dm.updateDocumentStatus(docID, "success", "")
	doc.Status = "success"
	doc.Stats = stats
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
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return fmt.Errorf("too many redirects")
				}
				// Re-validate each redirect target against SSRF rules
				if err := validateExternalURL(req.URL.String()); err != nil {
					return fmt.Errorf("redirect blocked: %w", err)
				}
				return nil
			},
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

// SetLLMService sets the LLM service for OCR on scanned PDFs.
func (dm *DocumentManager) SetLLMService(ls LLMService) {
	dm.mu.Lock()
	defer dm.mu.Unlock()
	dm.llmService = ls
}

// ocrImageViaLLM uses the LLM vision API to extract text from an image.
// The image is resized before sending to reduce payload and improve throughput.
func (dm *DocumentManager) ocrImageViaLLM(imgData []byte) (string, error) {
	dm.mu.RLock()
	ls := dm.llmService
	dm.mu.RUnlock()
	if ls == nil {
		return "", fmt.Errorf("LLM service not configured")
	}

	resized := resizeImageForOCR(imgData)
	dataURL := imageToBase64DataURL(resized)
	prompt := "你是一个OCR文字识别助手。请仔细识别图片中的所有文字内容，按原始排版顺序输出纯文本。只输出识别到的文字，不要添加任何解释或描述。如果图片中没有文字，输出空字符串。"
	text, err := ls.GenerateWithImage(prompt, nil, "请识别图片中的所有文字", dataURL)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

// describeKeyframeViaLLM uses the LLM vision API to both extract text (OCR) and
// generate a scene description for a video keyframe image. The combined result
// provides richer searchable content than OCR alone.
// The image is resized before sending to reduce payload and improve throughput.
func (dm *DocumentManager) describeKeyframeViaLLM(imgData []byte) (string, error) {
	dm.mu.RLock()
	ls := dm.llmService
	dm.mu.RUnlock()
	if ls == nil {
		return "", fmt.Errorf("LLM service not configured")
	}

	resized := resizeImageForOCR(imgData)
	dataURL := imageToBase64DataURL(resized)
	prompt := "你是一个视频内容分析助手。请分析这张视频关键帧图片，完成以下两个任务：\n" +
		"1. 文字识别：识别图片中出现的所有文字内容（如标题、字幕、界面文字、标签等），按原始排版顺序输出。\n" +
		"2. 场景描述：用简洁的语言描述画面中的主要内容、场景、人物动作、展示的产品或界面等关键信息。\n\n" +
		"请按以下格式输出：\n" +
		"[文字内容]\n（识别到的文字，如果没有文字则写\"无\"）\n\n" +
		"[场景描述]\n（对画面内容的简要描述）"

	text, err := ls.GenerateWithImage(prompt, nil, "请识别图片中的文字并描述画面内容", dataURL)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(text), nil
}

// resizeImageForOCR resizes an image so its longest edge is at most maxEdge pixels.
// This reduces base64 payload size and improves LLM vision API throughput without
// sacrificing OCR quality (most vision models internally resize to ~1536px anyway).
// Returns the original data unchanged if the image is already within bounds or
// if decoding fails (graceful fallback).
const ocrImageMaxEdge = 1536

func resizeImageForOCR(imgData []byte) []byte {
	src, _, err := image.Decode(bytes.NewReader(imgData))
	if err != nil {
		return imgData // can't decode → send as-is
	}

	bounds := src.Bounds()
	w, h := bounds.Dx(), bounds.Dy()

	// Already within target size
	if w <= ocrImageMaxEdge && h <= ocrImageMaxEdge {
		return imgData
	}

	// Calculate new dimensions preserving aspect ratio
	var newW, newH int
	if w >= h {
		newW = ocrImageMaxEdge
		newH = h * ocrImageMaxEdge / w
	} else {
		newH = ocrImageMaxEdge
		newW = w * ocrImageMaxEdge / h
	}
	if newW < 1 {
		newW = 1
	}
	if newH < 1 {
		newH = 1
	}

	dst := image.NewRGBA(image.Rect(0, 0, newW, newH))
	draw.BiLinear.Scale(dst, dst.Bounds(), src, bounds, draw.Over, nil)

	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, dst, &jpeg.Options{Quality: 85}); err != nil {
		return imgData // encode failed → send original
	}
	return buf.Bytes()
}

// detectImageMIME returns the MIME type based on image magic bytes.
func detectImageMIME(data []byte) string {
	if len(data) >= 3 && data[0] == 0xFF && data[1] == 0xD8 && data[2] == 0xFF {
		return "image/jpeg"
	}
	if len(data) >= 4 && string(data[:4]) == "\x89PNG" {
		return "image/png"
	}
	if len(data) >= 4 && string(data[:4]) == "RIFF" {
		return "image/webp"
	}
	if len(data) >= 3 && string(data[:3]) == "GIF" {
		return "image/gif"
	}
	return "image/png" // default fallback
}

// imageToBase64DataURL converts raw image data to a base64 data URL.
func imageToBase64DataURL(imgData []byte) string {
	return fmt.Sprintf("data:%s;base64,%s", detectImageMIME(imgData), base64.StdEncoding.EncodeToString(imgData))
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
		if err := rows.Err(); err != nil {
			log.Printf("Warning: chunk embedding query iteration error: %v", err)
		}
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
	stats, err := dm.processURL(docID, req.URL, req.ProductID)
	if err != nil {
		dm.updateDocumentStatus(docID, "failed", err.Error())
		doc.Status = "failed"
		doc.Error = err.Error()
		errlog.Logf("[Upload] URL processing failed for doc=%s url=%q: %v", docID, req.URL, err)
		return doc, nil
	}

	dm.updateDocumentStatus(docID, "success", "")
	doc.Status = "success"
	doc.Stats = stats
	return doc, nil
}

// DeleteDocument removes a document's vectors from the vector store, its
// record from the documents table, and the original file from disk.
// Uses a transaction for atomicity of database operations.
func (dm *DocumentManager) DeleteDocument(docID string) error {
	// Validate docID to prevent path traversal in file deletion
	if docID == "" || strings.ContainsAny(docID, "/\\") || strings.Contains(docID, "..") {
		return fmt.Errorf("invalid document ID")
	}

	if err := dm.vectorStore.DeleteByDocID(docID); err != nil {
		return fmt.Errorf("failed to delete vectors: %w", err)
	}

	// Use a transaction for atomicity of the two DELETE operations
	tx, err := dm.db.Begin()
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback()

	// Delete associated video_segments records (cascade cleanup for video documents)
	if _, err := tx.Exec(`DELETE FROM video_segments WHERE document_id = ?`, docID); err != nil {
		return fmt.Errorf("failed to delete video segments: %w", err)
	}
	if _, err := tx.Exec(`DELETE FROM documents WHERE id = ?`, docID); err != nil {
		return fmt.Errorf("failed to delete document record: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit delete transaction: %w", err)
	}

	// Remove original file directory (after successful DB commit)
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
// For scanned PDFs (no text but images present), it uses LLM vision OCR to extract text.
func (dm *DocumentManager) processFile(docID, docName string, fileData []byte, fileType string, productID string) (*ImportStats, error) {
	result, err := dm.parser.Parse(fileData, fileType)
	if err != nil {
		errlog.Logf("[Parse] failed to parse doc=%s file=%q type=%s: %v", docID, docName, fileType, err)
		return nil, fmt.Errorf("parse error: %w", err)
	}
	if result.Text == "" && len(result.Images) == 0 {
		return nil, fmt.Errorf("文档内容为空")
	}

	// OCR fallback: for scanned PDFs (no text but images present), use LLM vision to extract text
	if result.Text == "" && len(result.Images) > 0 && fileType == "pdf" {
		dm.mu.RLock()
		hasLLM := dm.llmService != nil
		dm.mu.RUnlock()
		if hasLLM {
			log.Printf("扫描型PDF检测: doc=%s, 尝试OCR识别 %d 页图片", docID, len(result.Images))

			// Concurrent OCR with worker pool (up to 3 concurrent LLM calls)
			type ocrPageResult struct {
				index int
				text  string
			}
			const maxOCRWorkers = 3
			pageCh := make(chan int, len(result.Images))
			ocrResultCh := make(chan ocrPageResult, len(result.Images))

			workerCount := maxOCRWorkers
			if len(result.Images) < workerCount {
				workerCount = len(result.Images)
			}

			var ocrWg sync.WaitGroup
			for w := 0; w < workerCount; w++ {
				ocrWg.Add(1)
				go func() {
					defer ocrWg.Done()
					for i := range pageCh {
						img := result.Images[i]
						if len(img.Data) == 0 {
							continue
						}
						ocrText, ocrErr := dm.ocrImageViaLLM(img.Data)
						if ocrErr != nil {
							log.Printf("Warning: OCR第%d页失败: %v", i+1, ocrErr)
							errlog.Logf("[OCR] page %d failed for doc=%s: %v", i+1, docID, ocrErr)
							continue
						}
						if ocrText != "" {
							ocrResultCh <- ocrPageResult{index: i, text: ocrText}
						}
					}
				}()
			}

			for i := range result.Images {
				pageCh <- i
			}
			close(pageCh)

			go func() {
				ocrWg.Wait()
				close(ocrResultCh)
			}()

			// Collect results and sort by page order
			var pageResults []ocrPageResult
			for r := range ocrResultCh {
				pageResults = append(pageResults, r)
			}
			sort.Slice(pageResults, func(i, j int) bool {
				return pageResults[i].index < pageResults[j].index
			})

			var sb strings.Builder
			for _, pr := range pageResults {
				if sb.Len() > 0 {
					sb.WriteString("\n\n")
				}
				sb.WriteString(pr.text)
			}
			if sb.Len() > 0 {
				result.Text = sb.String()
				log.Printf("OCR识别完成: doc=%s, 提取 %d 字符 (%d/%d 页成功)", docID, len(result.Text), len(pageResults), len(result.Images))
			}
		}
	}

	stats := &ImportStats{
		TextChars: len([]rune(result.Text)),
	}

	// Document-level dedup: check if identical content already exists
	if result.Text != "" {
		hash := contentHash(result.Text)
		if existingID := dm.findDocumentByContentHash(hash); existingID != "" {
			return nil, fmt.Errorf("文档内容重复，与已有文档相同")
		}
		// Store the content hash for future dedup checks
		dm.db.Exec(`UPDATE documents SET content_hash = ? WHERE id = ?`, hash, docID)
	}

	// For PPT: store each slide as a text chunk with its rendered image.
	// This links slide text directly to the slide image so queries show the right slide.
	if fileType == "ppt" && len(result.Images) > 0 {
		// Phase 1: Save all slide images and collect texts
		type slideInfo struct {
			text     string
			imageURL string
			index    int
		}
		var slides []slideInfo
		for i, img := range result.Images {
			var savedLocalURL string
			if len(img.Data) > 0 {
				savedURL, saveErr := dm.saveExtractedImage(img.Data)
				if saveErr != nil {
					log.Printf("Warning: failed to save PPT slide image %d: %v", i, saveErr)
				} else {
					savedLocalURL = savedURL
				}
			}

			chunkText := img.SlideText
			if chunkText == "" {
				chunkText = img.Alt
			}
			if chunkText == "" {
				continue
			}
			slides = append(slides, slideInfo{text: chunkText, imageURL: savedLocalURL, index: i})
		}

		if len(slides) == 0 {
			return stats, nil
		}

		// Phase 2: Batch embed all slide texts (respecting API batch size limit)
		texts := make([]string, len(slides))
		for i, s := range slides {
			texts[i] = s.text
		}
		const batchSize = 64
		vectors := make([][]float64, len(texts))
		for start := 0; start < len(texts); start += batchSize {
			end := start + batchSize
			if end > len(texts) {
				end = len(texts)
			}
			batch, embErr := dm.embeddingService.EmbedBatch(texts[start:end])
			if embErr != nil {
				return nil, fmt.Errorf("PPT slide embedding error (batch %d-%d): %w", start, end, embErr)
			}
			copy(vectors[start:end], batch)
		}

		// Phase 3: Store each slide chunk with its image URL
		imageCount := 0
		for i, s := range slides {
			slideChunk := []vectorstore.VectorChunk{{
				ChunkText:    s.text,
				ChunkIndex:   s.index,
				DocumentID:   docID,
				DocumentName: docName,
				Vector:       vectors[i],
				ImageURL:     s.imageURL,
				ProductID:    productID,
			}}
			if err := dm.vectorStore.Store(docID, slideChunk); err != nil {
				log.Printf("Warning: failed to store PPT slide %d: %v", s.index+1, err)
			} else {
				imageCount++
			}
		}
		stats.ImageCount = imageCount
		return stats, nil
	}

	// Store text chunks (for non-PPT documents)
	if result.Text != "" {
		if err := dm.chunkEmbedStore(docID, docName, result.Text, productID); err != nil {
			return nil, err
		}
	}

	// Store image embeddings (for non-PPT documents)
	imageCount := 0
	for i, img := range result.Images {
		imgURL := img.URL

		// For embedded images (e.g. from PDF), save to disk for UI display
		var savedLocalURL string
		if imgURL == "" && len(img.Data) > 0 {
			savedURL, saveErr := dm.saveExtractedImage(img.Data)
			if saveErr != nil {
				log.Printf("Warning: failed to save extracted image %d: %v", i, saveErr)
				// Continue — we can still embed the image even if disk save fails
			} else {
				savedLocalURL = savedURL
			}
		}

		// For embedding API: use base64 data URL for embedded images (external API can't access local paths)
		embedURL := imgURL
		if embedURL == "" && len(img.Data) > 0 {
			embedURL = imageToBase64DataURL(img.Data)
		}
		if embedURL == "" {
			continue
		}

		vec, err := dm.embeddingService.EmbedImageURL(embedURL)
		if err != nil {
			log.Printf("Warning: failed to embed image %d (%s): %v", i, img.Alt, err)
			continue
		}

		// For storage, prefer external URL > local serving URL
		storeURL := imgURL
		if storeURL == "" {
			storeURL = savedLocalURL
		}

		imgChunk := []vectorstore.VectorChunk{{
			ChunkText:    fmt.Sprintf("[图片: %s]", img.Alt),
			ChunkIndex:   1000 + i,
			DocumentID:   docID,
			DocumentName: docName,
			Vector:       vec,
			ImageURL:     storeURL,
			ProductID:    productID,
		}}
		if err := dm.vectorStore.Store(docID, imgChunk); err != nil {
			log.Printf("Warning: failed to store image vector %d: %v", i, err)
		} else {
			imageCount++
		}
	}
	stats.ImageCount = imageCount

	return stats, nil
}
// findSavedFile returns the path to the first regular file in dir, or "" if none found.
func (dm *DocumentManager) findSavedFile(dir string) string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return ""
	}
	for _, e := range entries {
		if !e.IsDir() {
			return filepath.Join(dir, e.Name())
		}
	}
	return ""
}

// sanitizeFilename cleans a filename for safe use on all platforms.
func sanitizeFilename(name, fallbackID string) string {
	safe := filepath.Base(name)
	safe = strings.NewReplacer(":", "_", "*", "_", "?", "_", "\"", "_", "<", "_", ">", "_", "|", "_").Replace(safe)
	if safe == "" || safe == "." || safe == ".." {
		safe = fallbackID + ".video"
	}
	return safe
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
func (dm *DocumentManager) processURL(docID, url string, productID string) (*ImportStats, error) {
	if err := dm.validateURL(url); err != nil {
		return nil, err
	}

	resp, err := dm.httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("URL returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, 10<<20)) // 10MB limit
	if err != nil {
		return nil, fmt.Errorf("failed to read URL content: %w", err)
	}

	text := strings.TrimSpace(string(body))
	if text == "" {
		return nil, fmt.Errorf("URL内容为空")
	}

	// Detect HTML content and parse it with image extraction
	contentType := resp.Header.Get("Content-Type")
	isHTML := strings.Contains(contentType, "text/html") || looksLikeHTML(text)
	if isHTML {
		result, err := dm.parser.ParseWithBaseURL(body, "html", url)
		if err != nil {
			return nil, fmt.Errorf("HTML parse error: %w", err)
		}
		stats := &ImportStats{
			TextChars: len([]rune(result.Text)),
		}
		// Document-level dedup for HTML content
		if result.Text != "" {
			hash := contentHash(result.Text)
			if existingID := dm.findDocumentByContentHash(hash); existingID != "" {
				return nil, fmt.Errorf("文档内容重复，与已有文档相同")
			}
			dm.db.Exec(`UPDATE documents SET content_hash = ? WHERE id = ?`, hash, docID)
		}
		if result.Text != "" {
			if err := dm.chunkEmbedStore(docID, url, result.Text, productID); err != nil {
				return nil, err
			}
		}
		// Embed images found in the HTML
		imageCount := 0
		for i, img := range result.Images {
			if img.URL == "" {
				continue
			}
			vec, err := dm.embeddingService.EmbedImageURL(img.URL)
			if err != nil {
				log.Printf("Warning: failed to embed HTML image %d (%s): %v", i, img.Alt, err)
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
				log.Printf("Warning: failed to store HTML image vector %d: %v", i, err)
			} else {
				imageCount++
			}
		}
		stats.ImageCount = imageCount
		return stats, nil
	}

	// Document-level dedup for plain text URL content
	hash := contentHash(text)
	if existingID := dm.findDocumentByContentHash(hash); existingID != "" {
		return nil, fmt.Errorf("文档内容重复，与已有文档相同")
	}
	dm.db.Exec(`UPDATE documents SET content_hash = ? WHERE id = ?`, hash, docID)

	if err := dm.chunkEmbedStore(docID, url, text, productID); err != nil {
		return nil, err
	}
	return &ImportStats{TextChars: len([]rune(text))}, nil
}

// validateExternalURL checks that a URL is a valid external HTTP(S) URL
// to prevent SSRF attacks against internal services.
func validateExternalURL(rawURL string) error {
	if rawURL == "" {
		return fmt.Errorf("URL不能为空")
	}
	// Reject URLs with embedded credentials (user:pass@host)
	if strings.Contains(rawURL, "@") {
		return fmt.Errorf("URL中不允许包含用户凭据")
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
	// Strip port but preserve brackets for IPv6
	if strings.HasPrefix(host, "[") {
		// IPv6 address
		if idx := strings.Index(host, "]:"); idx >= 0 {
			host = host[:idx+1]
		}
	} else if idx := strings.LastIndex(host, ":"); idx >= 0 {
		host = host[:idx]
	}
	// Block empty host
	if host == "" {
		return fmt.Errorf("URL缺少主机名")
	}
	blockedHosts := []string{
		"localhost", "127.0.0.1", "0.0.0.0",
		"[::1]", "::1", "[::0]", "::0", "[::ffff:127.0.0.1]",
		"169.254.169.254", "metadata.google.internal",
		"metadata.internal", "instance-data",
		"kubernetes.default", "kubernetes.default.svc",
	}
	for _, blocked := range blockedHosts {
		if host == blocked {
			return fmt.Errorf("不允许访问内部地址")
		}
	}
	// Block .internal and .local TLDs (cloud metadata, mDNS)
	if strings.HasSuffix(host, ".internal") || strings.HasSuffix(host, ".local") {
		return fmt.Errorf("不允许访问内部地址")
	}
	// Block private IP ranges using proper IP parsing
	ip := net.ParseIP(strings.Trim(host, "[]"))
	if ip != nil {
		if ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified() {
			return fmt.Errorf("不允许访问内部网络地址")
		}
		// Block RFC 6598 CGN range (100.64.0.0/10)
		cgn := net.IPNet{IP: net.ParseIP("100.64.0.0"), Mask: net.CIDRMask(10, 32)}
		if cgn.Contains(ip) {
			return fmt.Errorf("不允许访问内部网络地址")
		}
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
		log.Printf("去重: %d/%d 个分块复用了已有向量，节省 %d 次API调用",
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
	if filename == "." || filename == ".." || filename == "" {
		return fmt.Errorf("invalid filename")
	}
	// Remove characters that are problematic on Windows and could cause issues
	filename = strings.Map(func(r rune) rune {
		if r < 0x20 || r == '<' || r == '>' || r == ':' || r == '"' || r == '|' || r == '?' || r == '*' || r == '\\' {
			return '_'
		}
		return r
	}, filename)

	dir := filepath.Join(".", "data", "uploads", docID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create upload dir: %w", err)
	}
	filePath := filepath.Join(dir, filename)

	// Final safety check: ensure resolved path is within the uploads directory
	absDir, _ := filepath.Abs(dir)
	absFile, _ := filepath.Abs(filePath)
	if !strings.HasPrefix(absFile, absDir) {
		return fmt.Errorf("invalid filename: path traversal detected")
	}

	return os.WriteFile(filePath, data, 0644)
}

// saveExtractedImage saves embedded image data (e.g. from PDF) to data/images/
// and returns the URL path for accessing it.
func (dm *DocumentManager) saveExtractedImage(data []byte) (string, error) {
	// Map MIME type to file extension
	mime := detectImageMIME(data)
	ext := ".png"
	switch mime {
	case "image/jpeg":
		ext = ".jpg"
	case "image/webp":
		ext = ".webp"
	case "image/gif":
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

// GetDocumentInfo returns metadata for a single document by ID.
func (dm *DocumentManager) GetDocumentInfo(docID string) (*DocumentInfo, error) {
	var d DocumentInfo
	var errStr sql.NullString
	var createdAt sql.NullTime
	err := dm.db.QueryRow(
		"SELECT id, name, type, status, error, created_at, COALESCE(product_id, '') FROM documents WHERE id = ?", docID,
	).Scan(&d.ID, &d.Name, &d.Type, &d.Status, &errStr, &createdAt, &d.ProductID)
	if err != nil {
		return nil, fmt.Errorf("document not found: %w", err)
	}
	if errStr.Valid {
		d.Error = errStr.String
	}
	if createdAt.Valid {
		d.CreatedAt = createdAt.Time
	}
	return &d, nil
}
// ReviewSegment represents a video/audio segment for review display.
type ReviewSegment struct {
	Type      string  `json:"type"`       // "transcript" or "keyframe"
	StartTime float64 `json:"start_time"` // seconds
	EndTime   float64 `json:"end_time"`   // seconds
	Content   string  `json:"content"`    // text or image path
	ImageURL  string  `json:"image_url,omitempty"`
}

// ReviewData holds all extracted data for a document review.
type ReviewData struct {
	DocID    string          `json:"doc_id"`
	DocName  string          `json:"doc_name"`
	DocType  string          `json:"doc_type"`
	Segments []ReviewSegment `json:"segments"`
}

// GetDocumentReview returns the extracted segments (transcript + keyframes) for review.
func (dm *DocumentManager) GetDocumentReview(docID string) (*ReviewData, error) {
	docInfo, err := dm.GetDocumentInfo(docID)
	if err != nil {
		return nil, err
	}

	result := &ReviewData{
		DocID:   docID,
		DocName: docInfo.Name,
		DocType: docInfo.Type,
	}

	// Query video_segments for transcript and keyframe records
	rows, err := dm.db.Query(
		`SELECT segment_type, start_time, end_time, content, chunk_id FROM video_segments WHERE document_id = ? ORDER BY start_time ASC`,
		docID,
	)
	if err != nil {
		return result, nil // return empty segments on query error
	}
	defer rows.Close()

	type segWithChunk struct {
		seg     ReviewSegment
		chunkID string
	}
	var segments []segWithChunk
	var keyframeChunkIDs []string

	for rows.Next() {
		var sc segWithChunk
		if err := rows.Scan(&sc.seg.Type, &sc.seg.StartTime, &sc.seg.EndTime, &sc.seg.Content, &sc.chunkID); err != nil {
			continue
		}
		if sc.seg.Type == "keyframe" {
			keyframeChunkIDs = append(keyframeChunkIDs, sc.chunkID)
		}
		segments = append(segments, sc)
	}

	// Batch-fetch image URLs for all keyframe chunks to avoid N+1 queries
	imageURLMap := make(map[string]string)
	if len(keyframeChunkIDs) > 0 {
		placeholders := make([]string, len(keyframeChunkIDs))
		args := make([]interface{}, len(keyframeChunkIDs))
		for i, id := range keyframeChunkIDs {
			placeholders[i] = "?"
			args[i] = id
		}
		imgRows, imgErr := dm.db.Query(
			`SELECT id, image_url FROM chunks WHERE id IN (`+strings.Join(placeholders, ",")+`) AND image_url != '' AND image_url IS NOT NULL`,
			args...,
		)
		if imgErr == nil {
			defer imgRows.Close()
			for imgRows.Next() {
				var cid, url string
				if imgRows.Scan(&cid, &url) == nil {
					imageURLMap[cid] = url
				}
			}
		}
	}

	for _, sc := range segments {
		seg := sc.seg
		if seg.Type == "keyframe" {
			if url, ok := imageURLMap[sc.chunkID]; ok {
				seg.ImageURL = url
			}
			// Clear the temp file path from content — not useful for display
			seg.Content = ""
		}
		result.Segments = append(result.Segments, seg)
	}

	// Also query OCR description chunks (chunk_index >= 20000)
	ocrRows, err := dm.db.Query(
		`SELECT chunk_text, chunk_index FROM chunks WHERE document_id = ? AND chunk_index >= 20000 ORDER BY chunk_index ASC`,
		docID,
	)
	if err == nil {
		defer ocrRows.Close()
		for ocrRows.Next() {
			var text string
			var idx int
			if err := ocrRows.Scan(&text, &idx); err != nil {
				continue
			}
			result.Segments = append(result.Segments, ReviewSegment{
				Type:    "ocr_description",
				Content: text,
			})
		}
	}

	return result, nil
}

// GetFilePath returns the path to the original uploaded file for a document.
// Returns empty string if the file doesn't exist.
func (dm *DocumentManager) GetFilePath(docID string) (string, string, error) {
	// Validate docID: must be hex characters only (generated by generateID)
	if docID == "" {
		return "", "", fmt.Errorf("invalid document ID")
	}
	for _, c := range docID {
		if !((c >= 'a' && c <= 'f') || (c >= '0' && c <= '9')) {
			return "", "", fmt.Errorf("invalid document ID")
		}
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

	// Only serve regular files, not directories or symlinks
	entry := entries[0]
	if entry.IsDir() {
		return "", name, fmt.Errorf("original file not found")
	}
	info, err := entry.Info()
	if err != nil {
		return "", name, fmt.Errorf("original file not found")
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", name, fmt.Errorf("original file not found")
	}

	filePath := filepath.Join(dir, entry.Name())
	return filePath, entry.Name(), nil
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
