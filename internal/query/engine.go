// Package query implements the RAG query engine that coordinates
// embedding, vector search, and LLM response generation.
package query

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"helpdesk/internal/config"
	"helpdesk/internal/embedding"
	"helpdesk/internal/llm"
	"helpdesk/internal/vectorstore"
)

// QueryRequest represents a user's question submission.
type QueryRequest struct {
	Question  string `json:"question"`
	UserID    string `json:"user_id"`
	ImageData string `json:"image_data,omitempty"` // base64 data URL from clipboard paste
}

// QueryResponse represents the result of a RAG query.
type QueryResponse struct {
	Answer  string      `json:"answer"`
	Sources []SourceRef `json:"sources"`
	IsPending bool   `json:"is_pending"`
	Message   string `json:"message,omitempty"`
}

// SourceRef represents a reference to a source document chunk.
type SourceRef struct {
	DocumentName string `json:"document_name"`
	ChunkIndex   int    `json:"chunk_index"`
	Snippet      string `json:"snippet"`
	ImageURL     string `json:"image_url,omitempty"`
}

// QueryEngine orchestrates the RAG query flow: embed → search → LLM generate or pending.
type QueryEngine struct {
	embeddingService embedding.EmbeddingService
	vectorStore      vectorstore.VectorStore
	llmService       llm.LLMService
	db               *sql.DB
	config           *config.Config
}

// NewQueryEngine creates a new QueryEngine with the given dependencies.
func NewQueryEngine(
	embeddingService embedding.EmbeddingService,
	vectorStore vectorstore.VectorStore,
	llmService llm.LLMService,
	db *sql.DB,
	cfg *config.Config,
) *QueryEngine {
	return &QueryEngine{
		embeddingService: embeddingService,
		vectorStore:      vectorStore,
		llmService:       llmService,
		db:               db,
		config:           cfg,
	}
}

// UpdateServices replaces the embedding and LLM services (used after config change).
func (qe *QueryEngine) UpdateServices(es embedding.EmbeddingService, ls llm.LLMService, cfg *config.Config) {
	qe.embeddingService = es
	qe.llmService = ls
	qe.config = cfg
}

// IntentResult represents the result of intent classification.
type IntentResult struct {
	Intent string // "greeting", "product", or "irrelevant"
	Reason string
}

// classifyIntent uses the LLM to determine the user's intent.
func (qe *QueryEngine) classifyIntent(question string) (*IntentResult, error) {
	productIntro := ""
	if qe.config != nil {
		productIntro = qe.config.ProductIntro
	}

	systemPrompt := "你是一个意图分类器。根据用户输入判断意图类别。"
	if productIntro != "" {
		systemPrompt += "\n\n产品介绍：" + productIntro
	}
	systemPrompt += "\n\n请只回复一个JSON对象，格式：{\"intent\":\"类别\"}" +
		"\n\n意图类别：" +
		"\n- greeting: 仅限纯粹的打招呼和问候语（如：你好、hi、hello、在吗）" +
		"\n- product: 任何与产品相关的问题，包括但不限于：功能介绍、下载、安装、使用方法、技术问题、故障排查、价格、版本等" +
		"\n- irrelevant: 与产品完全无关的问题（如天气、笑话、新闻、个人情感等）" +
		"\n\n重要规则：如果用户在询问任何具体信息（即使很简短），都应归类为product而非greeting。" +
		"\n\n示例：" +
		"\n\"你好\" → {\"intent\":\"greeting\"}" +
		"\n\"hi\" → {\"intent\":\"greeting\"}" +
		"\n\"这是什么产品\" → {\"intent\":\"product\"}" +
		"\n\"下载地址\" → {\"intent\":\"product\"}" +
		"\n\"怎么安装\" → {\"intent\":\"product\"}" +
		"\n\"今天天气怎么样\" → {\"intent\":\"irrelevant\",\"reason\":\"天气查询与产品无关\"}"

	answer, err := qe.llmService.Generate(systemPrompt, nil, question)
	if err != nil {
		// If classification fails, default to allowing the query
		return &IntentResult{Intent: "product"}, nil
	}

	// Parse JSON response — extract first JSON object
	start := -1
	end := -1
	for i, c := range answer {
		if c == '{' && start == -1 {
			start = i
		}
		if c == '}' {
			end = i + 1
		}
	}
	if start >= 0 && end > start {
		var parsed struct {
			Intent string `json:"intent"`
			Reason string `json:"reason"`
		}
		if err := json.Unmarshal([]byte(answer[start:end]), &parsed); err == nil {
			return &IntentResult{Intent: parsed.Intent, Reason: parsed.Reason}, nil
		}
	}

	// Default to product if parsing fails
	return &IntentResult{Intent: "product"}, nil
}

// Query executes the full RAG pipeline:
// 1. Embed the question
// 2. Search the vector store for relevant chunks
// 3. If results found, call LLM to generate an answer with source references
// 4. If no results, create a pending question and notify the user
func (qe *QueryEngine) Query(req QueryRequest) (*QueryResponse, error) {
	// Step 0: Intent classification
	intent, err := qe.classifyIntent(req.Question)
	if err == nil {
		switch intent.Intent {
		case "greeting":
			// Return product intro as greeting response, in the user's language
			intro := "您好！欢迎使用我们的产品。"
			if qe.config != nil && qe.config.ProductIntro != "" {
				intro = qe.config.ProductIntro
			}
			// Use LLM to translate the greeting to match the user's question language
			translated, tErr := qe.llmService.Generate(
				"你是一个翻译助手。将以下内容翻译为与用户提问相同的语言。如果用户用英文提问，翻译为英文；如果用户用中文提问，保持中文。只输出翻译结果，不要添加任何解释。",
				[]string{intro},
				req.Question,
			)
			if tErr == nil && translated != "" {
				intro = translated
			}
			return &QueryResponse{Answer: intro}, nil
		case "irrelevant":
			msg := "抱歉，这个问题与我们的产品无关。请问有什么产品方面的问题需要帮助吗？"
			if intent.Reason != "" {
				msg = "抱歉，" + intent.Reason + "。请问有什么产品方面的问题需要帮助吗？"
			}
			translated, tErr := qe.llmService.Generate(
				"你是一个翻译助手。将以下内容翻译为与用户提问相同的语言。如果用户用英文提问，翻译为英文；如果用户用中文提问，保持中文。只输出翻译结果，不要添加任何解释。",
				[]string{msg},
				req.Question,
			)
			if tErr == nil && translated != "" {
				msg = translated
			}
			return &QueryResponse{Answer: msg}, nil
		}
	}

	// Step 1: Embed the question
	queryVector, err := qe.embeddingService.Embed(req.Question)
	if err != nil {
		return nil, fmt.Errorf("failed to embed question: %w", err)
	}
	log.Printf("[Query] question=%q, vector_dim=%d", req.Question, len(queryVector))

	// Step 2: Search vector store
	topK := qe.config.Vector.TopK
	threshold := qe.config.Vector.Threshold
	results, err := qe.vectorStore.Search(queryVector, topK, threshold)
	if err != nil {
		return nil, fmt.Errorf("failed to search vector store: %w", err)
	}
	log.Printf("[Query] search topK=%d threshold=%.2f results=%d", topK, threshold, len(results))

	// Step 2.5: If image provided, also search with image embedding and merge results
	if req.ImageData != "" {
		imgVec, imgErr := qe.embeddingService.EmbedImageURL(req.ImageData)
		if imgErr != nil {
			log.Printf("[Query] image embedding failed: %v", imgErr)
		} else {
			log.Printf("[Query] image vector_dim=%d", len(imgVec))
			imgResults, imgSearchErr := qe.vectorStore.Search(imgVec, topK, threshold)
			if imgSearchErr == nil && len(imgResults) > 0 {
				log.Printf("[Query] image search results=%d", len(imgResults))
				results = mergeSearchResults(results, imgResults, topK)
			}
		}
	}

	// Step 3: If no results above threshold, try with lower threshold before giving up
	if len(results) == 0 {
		relaxedResults, _ := qe.vectorStore.Search(queryVector, 3, 0.0)
		log.Printf("[Query] relaxed search results=%d", len(relaxedResults))
		for i, r := range relaxedResults {
			log.Printf("[Query]   relaxed[%d] score=%.4f doc=%q dim_match=%v", i, r.Score, r.DocumentName, true)
		}
		if len(relaxedResults) > 0 && relaxedResults[0].Score >= 0.3 {
			results = relaxedResults[:1]
		}
	}

	// Step 4: If still no results, create pending question
	if len(results) == 0 {
		if err := qe.createPendingQuestion(req.Question, req.UserID); err != nil {
			return nil, fmt.Errorf("failed to create pending question: %w", err)
		}
		pendingMsg := "该问题已转交人工处理，请稍后查看回复"
		translated, tErr := qe.llmService.Generate(
			"你是一个翻译助手。将以下内容翻译为与用户提问相同的语言。如果用户用英文提问，翻译为英文；如果用户用中文提问，保持中文。只输出翻译结果，不要添加任何解释。",
			[]string{pendingMsg},
			req.Question,
		)
		if tErr == nil && translated != "" {
			pendingMsg = translated
		}
		return &QueryResponse{
			IsPending: true,
			Message:   pendingMsg,
		}, nil
	}

	// Step 5: Build context from search results and call LLM
	context := make([]string, len(results))
	for i, r := range results {
		context[i] = r.ChunkText
	}

	answer, err := qe.llmService.Generate("", context, req.Question)
	if err != nil {
		return nil, fmt.Errorf("failed to generate answer: %w", err)
	}

	// Step 6: Build source references
	sources := make([]SourceRef, len(results))
	for i, r := range results {
		snippet := r.ChunkText
		if len(snippet) > 100 {
			snippet = snippet[:100]
		}
		sources[i] = SourceRef{
			DocumentName: r.DocumentName,
			ChunkIndex:   r.ChunkIndex,
			Snippet:      snippet,
			ImageURL:     r.ImageURL,
		}
	}

	return &QueryResponse{
		Answer:  answer,
		Sources: sources,
	}, nil
}

// createPendingQuestion inserts a new pending question record into the database.
func (qe *QueryEngine) createPendingQuestion(question, userID string) error {
	id, err := generateID()
	if err != nil {
		return err
	}
	_, err = qe.db.Exec(
		`INSERT INTO pending_questions (id, question, user_id, status, created_at) VALUES (?, ?, ?, ?, ?)`,
		id, question, userID, "pending", time.Now().UTC(),
	)
	return err
}

// generateID creates a random hex string for use as a unique identifier.
func generateID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("failed to generate ID: %w", err)
	}
	return hex.EncodeToString(b), nil
}

// mergeSearchResults merges two search result sets, deduplicating by (documentID, chunkIndex),
// keeping the higher score, and returning the top-K results sorted by score descending.
func mergeSearchResults(a, b []vectorstore.SearchResult, topK int) []vectorstore.SearchResult {
	type key struct {
		docID      string
		chunkIndex int
	}
	seen := make(map[key]int) // key → index in merged
	merged := make([]vectorstore.SearchResult, 0, len(a)+len(b))

	for _, r := range a {
		k := key{r.DocumentID, r.ChunkIndex}
		seen[k] = len(merged)
		merged = append(merged, r)
	}
	for _, r := range b {
		k := key{r.DocumentID, r.ChunkIndex}
		if idx, ok := seen[k]; ok {
			if r.Score > merged[idx].Score {
				merged[idx] = r
			}
		} else {
			seen[k] = len(merged)
			merged = append(merged, r)
		}
	}

	// Sort by score descending
	for i := 0; i < len(merged)-1; i++ {
		for j := i + 1; j < len(merged); j++ {
			if merged[j].Score > merged[i].Score {
				merged[i], merged[j] = merged[j], merged[i]
			}
		}
	}

	if len(merged) > topK {
		merged = merged[:topK]
	}
	return merged
}
