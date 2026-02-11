// Package document provides the Document Manager for handling document upload,
// processing, deletion, and listing in the helpdesk system.
package document

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"helpdesk/internal/chunker"
	"helpdesk/internal/embedding"
	"helpdesk/internal/parser"
	"helpdesk/internal/vectorstore"
)

// supportedFileTypes lists the file types accepted for upload.
var supportedFileTypes = map[string]bool{
	"pdf":      true,
	"word":     true,
	"excel":    true,
	"ppt":      true,
	"markdown": true,
	"html":     true,
}

// DocumentManager orchestrates document upload, processing, and lifecycle management.
type DocumentManager struct {
	parser           *parser.DocumentParser
	chunker          *chunker.TextChunker
	embeddingService embedding.EmbeddingService
	vectorStore      vectorstore.VectorStore
	db               *sql.DB
	httpClient       *http.Client
}

// DocumentInfo holds metadata about a document stored in the system.
type DocumentInfo struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Type      string    `json:"type"`
	Status    string    `json:"status"` // "processing", "success", "failed"
	Error     string    `json:"error,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// UploadFileRequest represents a file upload request.
type UploadFileRequest struct {
	FileName string `json:"file_name"`
	FileData []byte `json:"file_data"`
	FileType string `json:"file_type"`
}

// UploadURLRequest represents a URL upload request.
type UploadURLRequest struct {
	URL string `json:"url"`
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
	}
}

// UpdateEmbeddingService replaces the embedding service (used after config change).
func (dm *DocumentManager) UpdateEmbeddingService(es embedding.EmbeddingService) {
	dm.embeddingService = es
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
func (dm *DocumentManager) getExistingChunkEmbeddings(texts []string) map[string][]float64 {
	result := make(map[string][]float64)
	if len(texts) == 0 {
		return result
	}
	for _, text := range texts {
		var embeddingBytes []byte
		err := dm.db.QueryRow(
			`SELECT embedding FROM chunks WHERE chunk_text = ? LIMIT 1`, text,
		).Scan(&embeddingBytes)
		if err == nil && len(embeddingBytes) > 0 {
			vec := vectorstore.DeserializeVector(embeddingBytes)
			if len(vec) > 0 {
				result[text] = vec
			}
		}
	}
	return result
}

// UploadFile validates the file type, parses the file, chunks the text,
// generates embeddings, and stores everything. The document record tracks
// processing status in the documents table.
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
	}

	if err := dm.insertDocument(doc); err != nil {
		return nil, fmt.Errorf("failed to insert document record: %w", err)
	}

	// Save original file to disk
	if err := dm.saveOriginalFile(docID, req.FileName, req.FileData); err != nil {
		// Non-fatal: log but continue processing
		fmt.Printf("Warning: failed to save original file: %v\n", err)
	}

	// Parse → Chunk → Embed → Store
	if err := dm.processFile(docID, req.FileName, req.FileData, fileType); err != nil {
		dm.updateDocumentStatus(docID, "failed", err.Error())
		doc.Status = "failed"
		doc.Error = err.Error()
		return doc, nil
	}

	dm.updateDocumentStatus(docID, "success", "")
	doc.Status = "success"
	return doc, nil
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
	}

	if err := dm.insertDocument(doc); err != nil {
		return nil, fmt.Errorf("failed to insert document record: %w", err)
	}

	// Fetch → Chunk → Embed → Store
	if err := dm.processURL(docID, req.URL); err != nil {
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
	if err := dm.vectorStore.DeleteByDocID(docID); err != nil {
		return fmt.Errorf("failed to delete vectors: %w", err)
	}
	_, err := dm.db.Exec(`DELETE FROM documents WHERE id = ?`, docID)
	if err != nil {
		return fmt.Errorf("failed to delete document record: %w", err)
	}
	// Remove original file directory
	dir := filepath.Join(".", "data", "uploads", docID)
	os.RemoveAll(dir)
	return nil
}

// ListDocuments returns all documents ordered by creation time descending.
func (dm *DocumentManager) ListDocuments() ([]DocumentInfo, error) {
	rows, err := dm.db.Query(`SELECT id, name, type, status, error, created_at FROM documents ORDER BY created_at DESC`)
	if err != nil {
		return nil, fmt.Errorf("failed to query documents: %w", err)
	}
	defer rows.Close()

	var docs []DocumentInfo
	for rows.Next() {
		var d DocumentInfo
		var errStr sql.NullString
		var createdAt sql.NullTime
		if err := rows.Scan(&d.ID, &d.Name, &d.Type, &d.Status, &errStr, &createdAt); err != nil {
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
func (dm *DocumentManager) processFile(docID, docName string, fileData []byte, fileType string) error {
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
		if err := dm.chunkEmbedStore(docID, docName, result.Text); err != nil {
			return err
		}
	}

	// Store image embeddings
	for i, img := range result.Images {
		if img.URL == "" {
			continue
		}
		vec, err := dm.embeddingService.EmbedImageURL(img.URL)
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
			ImageURL:     img.URL,
		}}
		if err := dm.vectorStore.Store(docID, imgChunk); err != nil {
			fmt.Printf("Warning: failed to store image vector %d: %v\n", i, err)
		}
	}

	return nil
}

// processURL fetches URL content and processes it as plain text.
func (dm *DocumentManager) processURL(docID, url string) error {
	resp, err := dm.httpClient.Get(url)
	if err != nil {
		return fmt.Errorf("failed to fetch URL: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("URL returned HTTP %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
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
			if err := dm.chunkEmbedStore(docID, url, result.Text); err != nil {
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

	return dm.chunkEmbedStore(docID, url, text)
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
func (dm *DocumentManager) chunkEmbedStore(docID, docName, text string) error {
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
		`INSERT INTO documents (id, name, type, status, error, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		doc.ID, doc.Name, doc.Type, doc.Status, doc.Error, doc.CreatedAt,
	)
	return err
}

// updateDocumentStatus updates the status and error fields of a document.
func (dm *DocumentManager) updateDocumentStatus(docID, status, errMsg string) {
	dm.db.Exec(`UPDATE documents SET status = ?, error = ? WHERE id = ?`, status, errMsg, docID)
}

// saveOriginalFile saves the uploaded file to data/uploads/{docID}/{filename}.
func (dm *DocumentManager) saveOriginalFile(docID, filename string, data []byte) error {
	dir := filepath.Join(".", "data", "uploads", docID)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create upload dir: %w", err)
	}
	filePath := filepath.Join(dir, filename)
	return os.WriteFile(filePath, data, 0644)
}

// GetFilePath returns the path to the original uploaded file for a document.
// Returns empty string if the file doesn't exist.
func (dm *DocumentManager) GetFilePath(docID string) (string, string, error) {
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
func (dm *DocumentManager) ChunkEmbedStore(docID, docName, text string) error {
	return dm.chunkEmbedStore(docID, docName, text)
}

// GetEmbeddingService returns the current embedding service.
func (dm *DocumentManager) GetEmbeddingService() embedding.EmbeddingService {
	return dm.embeddingService
}

// StoreChunks stores pre-built vector chunks into the vector store.
func (dm *DocumentManager) StoreChunks(docID string, chunks []vectorstore.VectorChunk) error {
	return dm.vectorStore.Store(docID, chunks)
}
