// Package pending manages questions that could not be automatically answered.
// It supports creating, listing, and answering pending questions.
package pending

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"fmt"
	"log"
	"sync"
	"time"

	"askflow/internal/chunker"
	"askflow/internal/embedding"
	"askflow/internal/llm"
	"askflow/internal/vectorstore"
)

// PendingQuestion represents a user question awaiting admin response.
type PendingQuestion struct {
	ID          string    `json:"id"`
	Question    string    `json:"question"`
	UserID      string    `json:"user_id"`
	UserName    string    `json:"user_name,omitempty"`
	Status      string    `json:"status"` // "pending", "answered"
	Answer      string    `json:"answer,omitempty"`
	ImageData   string    `json:"image_data,omitempty"` // base64 data URL of attached image
	ProductID   string    `json:"product_id"`
	ProductName string    `json:"product_name"`
	CreatedAt   time.Time `json:"created_at"`
}



// AdminAnswerRequest represents an admin's answer to a pending question.
type AdminAnswerRequest struct {
	QuestionID string   `json:"question_id"`
	Text       string   `json:"text,omitempty"`
	ImageData  []byte   `json:"image_data,omitempty"`
	URL        string   `json:"url,omitempty"`
	ImageURLs  []string `json:"image_urls,omitempty"`
	IsEdit     bool     `json:"is_edit,omitempty"`
}

// PendingQuestionManager handles the lifecycle of pending questions.
type PendingQuestionManager struct {
	mu               sync.RWMutex
	db               *sql.DB
	chunker          *chunker.TextChunker
	embeddingService embedding.EmbeddingService
	vectorStore      vectorstore.VectorStore
	llmService       llm.LLMService
}

// NewPendingQuestionManager creates a new PendingQuestionManager with the given dependencies.
func NewPendingQuestionManager(
	db *sql.DB,
	c *chunker.TextChunker,
	es embedding.EmbeddingService,
	vs vectorstore.VectorStore,
	ls llm.LLMService,
) *PendingQuestionManager {
	return &PendingQuestionManager{
		db:               db,
		chunker:          c,
		embeddingService: es,
		vectorStore:      vs,
		llmService:       ls,
	}
}

// UpdateServices replaces the embedding and LLM services (used after config change).
func (pm *PendingQuestionManager) UpdateServices(es embedding.EmbeddingService, ls llm.LLMService) {
	pm.mu.Lock()
	defer pm.mu.Unlock()
	pm.embeddingService = es
	pm.llmService = ls
}

// generateID creates a random hex string for use as a unique identifier.
func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// CreatePending inserts a new pending question record with status="pending".
func (pm *PendingQuestionManager) CreatePending(question string, userID string, imageData string, productID string) (*PendingQuestion, error) {
	// Validate input lengths
	if len(question) > 10000 {
		return nil, fmt.Errorf("question too long (max 10000 characters)")
	}
	if len(imageData) > 5*1024*1024 {
		return nil, fmt.Errorf("image data too large (max 5MB)")
	}

	id, err := generateID()
	if err != nil {
		return nil, err
	}

	now := time.Now().UTC()
	_, err = pm.db.Exec(
		`INSERT INTO pending_questions (id, question, user_id, status, image_data, product_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, question, userID, "pending", imageData, productID, now,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to insert pending question: %w", err)
	}

	return &PendingQuestion{
		ID:        id,
		Question:  question,
		UserID:    userID,
		Status:    "pending",
		ImageData: imageData,
		ProductID: productID,
		CreatedAt: now,
	}, nil
}

// DeletePending removes a pending question by ID.
// If the question was answered, it also cleans up the associated document and vector data.
func (pm *PendingQuestionManager) DeletePending(id string) error {
	var status string
	err := pm.db.QueryRow(`SELECT status FROM pending_questions WHERE id = ?`, id).Scan(&status)
	if err == sql.ErrNoRows {
		return fmt.Errorf("pending question not found")
	}
	if err != nil {
		return fmt.Errorf("failed to query pending question: %w", err)
	}

	// If answered, clean up the associated vector store data and document record
	if status == "answered" {
		docID := "pending-answer-" + id
		_ = pm.vectorStore.DeleteByDocID(docID)
		_, _ = pm.db.Exec(`DELETE FROM documents WHERE id = ?`, docID)
	}

	_, err = pm.db.Exec(`DELETE FROM pending_questions WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("failed to delete pending question: %w", err)
	}
	return nil
}

// ListPending returns pending questions filtered by status and/or productID,
// ordered by created_at DESC. When productID is non-empty, only questions matching
// that product or the public library (empty product_id) are returned.
// Product names are resolved via LEFT JOIN with the products table.
func (pm *PendingQuestionManager) ListPending(status string, productID string) ([]PendingQuestion, error) {
	// Validate status to prevent unexpected values
	if status != "" && status != "pending" && status != "answered" {
		return nil, fmt.Errorf("invalid status filter: %s", status)
	}

	var rows *sql.Rows
	var err error

	baseSelect := `SELECT pq.id, pq.question, pq.user_id, COALESCE(u.name, '') AS user_name, pq.status, pq.answer, pq.image_data, pq.product_id, COALESCE(p.name, '') AS product_name, pq.created_at
		FROM pending_questions pq
		LEFT JOIN products p ON pq.product_id = p.id
		LEFT JOIN users u ON pq.user_id = u.id`

	var conditions []string
	var args []interface{}

	if status != "" {
		conditions = append(conditions, "pq.status = ?")
		args = append(args, status)
	}
	if productID != "" {
		conditions = append(conditions, "(pq.product_id = ? OR pq.product_id = '')")
		args = append(args, productID)
	}

	query := baseSelect
	if len(conditions) > 0 {
		query += " WHERE " + conditions[0]
		for i := 1; i < len(conditions); i++ {
			query += " AND " + conditions[i]
		}
	}
	query += " ORDER BY pq.created_at DESC"

	rows, err = pm.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query pending questions: %w", err)
	}
	defer rows.Close()

	var questions []PendingQuestion
	for rows.Next() {
		var q PendingQuestion
		var answer sql.NullString
		var imageData sql.NullString
		var userName sql.NullString
		var productName sql.NullString
		var createdAt sql.NullTime
		if err := rows.Scan(&q.ID, &q.Question, &q.UserID, &userName, &q.Status, &answer, &imageData, &q.ProductID, &productName, &createdAt); err != nil {
			return nil, fmt.Errorf("failed to scan pending question row: %w", err)
		}
		if answer.Valid {
			q.Answer = answer.String
		}
		if imageData.Valid {
			q.ImageData = imageData.String
		}
		if userName.Valid && userName.String != "" {
			q.UserName = userName.String
		}
		if createdAt.Valid {
			q.CreatedAt = createdAt.Time
		}
		if q.ProductID == "" {
			q.ProductName = "公共库"
		} else if productName.Valid && productName.String != "" {
			q.ProductName = productName.String
		}
		questions = append(questions, q)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating pending question rows: %w", err)
	}
	return questions, nil
}

// AnswerQuestion processes an admin's answer to a pending question:
// 1. Retrieves the question from DB
// 2. Stores the answer text in the pending_questions record
// 3. Chunks the answer text → embeds → stores in vector store (knowledge base)
// 4. Calls LLM to generate a summary answer based on the admin's answer
// 5. Updates the record with llm_answer, status="answered", answered_at=now
func (pm *PendingQuestionManager) AnswerQuestion(req AdminAnswerRequest) error {
	// Validate inputs
	if req.QuestionID == "" {
		return fmt.Errorf("question_id is required")
	}
	if len(req.Text) > 100000 {
		return fmt.Errorf("answer text too long (max 100000 characters)")
	}
	if len(req.ImageURLs) > 50 {
		return fmt.Errorf("too many image URLs (max 50)")
	}

	// Step 1: Get the question from DB
	var question string
	var status string
	var productID string
	err := pm.db.QueryRow(
		`SELECT question, status, product_id FROM pending_questions WHERE id = ?`, req.QuestionID,
	).Scan(&question, &status, &productID)
	if err == sql.ErrNoRows {
		return fmt.Errorf("pending question not found")
	}
	if err != nil {
		return fmt.Errorf("failed to query pending question: %w", err)
	}
	if status == "answered" && !req.IsEdit {
		return fmt.Errorf("question already answered")
	}

	// If editing, clean up old vector store data first
	if status == "answered" && req.IsEdit {
		docID := "pending-answer-" + req.QuestionID
		if err := pm.vectorStore.DeleteByDocID(docID); err != nil {
			log.Printf("Warning: failed to delete old vector data for %s: %v", docID, err)
		}
		if _, err := pm.db.Exec(`DELETE FROM documents WHERE id = ?`, docID); err != nil {
			log.Printf("Warning: failed to delete old document record for %s: %v", docID, err)
		}
	}

	// Step 2: Store the answer text in the record
	answerText := req.Text
	_, err = pm.db.Exec(
		`UPDATE pending_questions SET answer = ? WHERE id = ?`,
		answerText, req.QuestionID,
	)
	if err != nil {
		return fmt.Errorf("failed to update answer text: %w", err)
	}

	// Step 3: Chunk the Q&A content → embed → store in vector store
	docID := "pending-answer-" + req.QuestionID
	docName := "管理员回答: " + truncate(question, 50)
	docCreated := false

	if answerText != "" {
		// Combine question and answer for better semantic matching
		qaText := "问题：" + question + "\n回答：" + answerText

		chunks := pm.chunker.Split(qaText, docID)
		if len(chunks) > 0 {
			texts := make([]string, len(chunks))
			for i, c := range chunks {
				texts[i] = c.Text
			}

			embeddings, err := pm.embeddingService.EmbedBatch(texts)
			if err != nil {
				return fmt.Errorf("failed to embed answer chunks: %w", err)
			}

			// Insert a document record so the chunks FK constraint is satisfied
			_, err = pm.db.Exec(
				`INSERT OR REPLACE INTO documents (id, name, type, status, product_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
				docID, docName, "answer", "success", productID, time.Now().UTC(),
			)
			if err != nil {
				return fmt.Errorf("failed to insert document record for answer: %w", err)
			}
			docCreated = true

			vectorChunks := make([]vectorstore.VectorChunk, len(chunks))
			for i, c := range chunks {
				vectorChunks[i] = vectorstore.VectorChunk{
					ChunkText:    c.Text,
					ChunkIndex:   c.Index,
					DocumentID:   docID,
					DocumentName: docName,
					Vector:       embeddings[i],
					ProductID:    productID,
				}
			}

			if err := pm.vectorStore.Store(docID, vectorChunks); err != nil {
				return fmt.Errorf("failed to store answer in vector store: %w", err)
			}
		}
	}

	// Step 3.5: Store image references as searchable vector chunks
	if len(req.ImageURLs) > 0 {
		if !docCreated {
			_, err = pm.db.Exec(
				`INSERT OR REPLACE INTO documents (id, name, type, status, product_id, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
				docID, docName, "answer", "success", productID, time.Now().UTC(),
			)
			if err != nil {
				return fmt.Errorf("failed to insert document record for answer images: %w", err)
			}
		}

		imgText := fmt.Sprintf("[图片回答: %s] %s", truncate(question, 50), answerText)
		// Embed the text once and reuse the vector for all images (same text → same embedding)
		imgVec, embErr := pm.embeddingService.Embed(imgText)
		if embErr != nil {
			log.Printf("Warning: failed to embed answer image text: %v", embErr)
		} else {
			for i, imgURL := range req.ImageURLs {
				if imgURL == "" {
					continue
				}
				// Copy the vector to avoid shared slice mutation
				vecCopy := make([]float64, len(imgVec))
				copy(vecCopy, imgVec)
				imgChunk := []vectorstore.VectorChunk{{
					ChunkText:    fmt.Sprintf("[图片回答: %s]", truncate(question, 50)),
					ChunkIndex:   1000 + i,
					DocumentID:   docID,
					DocumentName: docName,
					Vector:       vecCopy,
					ImageURL:     imgURL,
					ProductID:    productID,
				}}
				if storeErr := pm.vectorStore.Store(docID, imgChunk); storeErr != nil {
					log.Printf("Warning: failed to store answer image chunk %d: %v", i, storeErr)
				}
			}
		}
	}

	// Step 4: Call LLM to generate a summary answer
	llmAnswer, err := pm.llmService.Generate(
		"请根据管理员提供的回答内容，生成一个简洁、清晰的总结性回答。",
		[]string{answerText},
		question,
	)
	if err != nil {
		return fmt.Errorf("failed to generate LLM answer: %w", err)
	}

	// Step 5: Update record with llm_answer, status="answered", answered_at=now
	now := time.Now().UTC()
	_, err = pm.db.Exec(
		`UPDATE pending_questions SET llm_answer = ?, status = ?, answered_at = ? WHERE id = ?`,
		llmAnswer, "answered", now, req.QuestionID,
	)
	if err != nil {
		return fmt.Errorf("failed to update pending question status: %w", err)
	}

	return nil
}

// truncate shortens a string to maxLen characters, appending "..." if truncated.
func truncate(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
