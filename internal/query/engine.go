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
	"math"
	"strings"
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

// TranslateText translates the given text to the target language using LLM.
func (qe *QueryEngine) TranslateText(text, targetLang string) (string, error) {
	if text == "" {
		return "", nil
	}
	langName := targetLang
	switch targetLang {
	case "zh-CN":
		langName = "简体中文"
	case "en-US", "en":
		langName = "English"
	}
	prompt := fmt.Sprintf("你是一个翻译助手。将以下文本翻译为%s。只输出翻译结果，不要添加任何解释或引号。如果文本已经是目标语言，直接原样输出。", langName)
	translated, err := qe.llmService.Generate(prompt, []string{text}, text)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(translated), nil
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
	// Step 0: Intent classification (skip if image is attached — image may contain product info)
	if req.ImageData == "" {
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
	var imgVec []float64
	if req.ImageData != "" {
		var imgErr error
		imgVec, imgErr = qe.embeddingService.EmbedImageURL(req.ImageData)
		if imgErr != nil {
			log.Printf("[Query] image embedding failed: %v", imgErr)
		} else {
			log.Printf("[Query] image vector_dim=%d", len(imgVec))
			// Use a lower threshold for image search since cross-modal similarity scores are typically lower
			imgThreshold := threshold * 0.6
			if imgThreshold < 0.3 {
				imgThreshold = 0.3
			}
			imgResults, imgSearchErr := qe.vectorStore.Search(imgVec, topK, imgThreshold)
			if imgSearchErr == nil && len(imgResults) > 0 {
				log.Printf("[Query] image search results=%d (threshold=%.2f)", len(imgResults), imgThreshold)
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

		// Also try relaxed search with image vector
		if len(results) == 0 && len(imgVec) > 0 {
			imgRelaxed, _ := qe.vectorStore.Search(imgVec, 3, 0.0)
			log.Printf("[Query] relaxed image search results=%d", len(imgRelaxed))
			for i, r := range imgRelaxed {
				log.Printf("[Query]   img_relaxed[%d] score=%.4f doc=%q", i, r.Score, r.DocumentName)
			}
			if len(imgRelaxed) > 0 && imgRelaxed[0].Score >= 0.2 {
				results = mergeSearchResults(results, imgRelaxed[:1], topK)
			}
		}
	}

	// Step 3.5: Reorder results based on content priority setting
	if len(results) > 1 && qe.config != nil {
		priority := qe.config.Vector.ContentPriority
		if priority == "image_text" {
			// Boost results that have images to the top (stable sort preserving score order within group)
			reordered := make([]vectorstore.SearchResult, 0, len(results))
			var textOnly []vectorstore.SearchResult
			for _, r := range results {
				if r.ImageURL != "" {
					reordered = append(reordered, r)
				} else {
					textOnly = append(textOnly, r)
				}
			}
			results = append(reordered, textOnly...)
			log.Printf("[Query] content_priority=image_text, image_results=%d, text_results=%d", len(reordered), len(textOnly))
		} else if priority == "text_only" {
			// Boost pure text results to the top
			reordered := make([]vectorstore.SearchResult, 0, len(results))
			var withImage []vectorstore.SearchResult
			for _, r := range results {
				if r.ImageURL == "" {
					reordered = append(reordered, r)
				} else {
					withImage = append(withImage, r)
				}
			}
			results = append(reordered, withImage...)
			log.Printf("[Query] content_priority=text_only, text_results=%d, image_results=%d", len(reordered), len(withImage))
		}
	}

	// Step 4: If still no results, create pending question
	if len(results) == 0 {
		// Check for existing similar pending question first
		if existing := qe.findSimilarPendingQuestion(req.Question, queryVector); existing != "" {
			pendingMsg := "该问题已在处理中，请耐心等待回复"
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

		if err := qe.createPendingQuestion(req.Question, req.UserID, req.ImageData); err != nil {
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

	// Step 4.5: Enrich search results with images from the same documents
	// If search results don't include image chunks, look up image URLs
	// from the same documents in the database.
	docImages := qe.findDocumentImages(results)

	// Step 5: Build context from search results and call LLM
	context := make([]string, len(results))
	hasImages := len(docImages) > 0
	for i, r := range results {
		if r.ImageURL != "" {
			context[i] = r.ChunkText + " (图片已附带，将自动展示给用户)"
			hasImages = true
		} else {
			context[i] = r.ChunkText
		}
	}

	systemPrompt := ""
	if hasImages {
		systemPrompt = "你是一个专业的软件技术支持助手。请根据提供的参考资料回答用户的问题。" +
			"如果参考资料中没有相关信息，请如实告知用户。回答应简洁、准确、有条理。" +
			"\n\n重要规则：你必须使用与用户提问相同的语言来回答。如果用户用英文提问，你必须用英文回答；如果用户用中文提问，你必须用中文回答；其他语言同理。无论参考资料是什么语言，都要翻译成用户提问的语言来回答。" +
			"\n\n关于图片：参考资料中标记为[图片已附带]的内容，对应的图片会自动展示在你的回答下方。请在回答中自然地引导用户查看图片（例如：如下图所示、请参考下方图片），不要说无法提供图片或无法展示图片。"
	}

	answer, err := qe.llmService.Generate(systemPrompt, context, req.Question)
	if err != nil {
		return nil, fmt.Errorf("failed to generate answer: %w", err)
	}

	// Step 5.5: Detect "unable to answer" responses and create pending question
	isPending := false
	if isUnableToAnswer(answer) {
		log.Printf("[Query] LLM answer indicates unable to answer, creating pending question")
		if existing := qe.findSimilarPendingQuestion(req.Question, queryVector); existing != "" {
			isPending = true
		} else {
			_ = qe.createPendingQuestion(req.Question, req.UserID, req.ImageData)
			isPending = true
		}
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

	// Append document images that weren't already in search results
	for _, img := range docImages {
		sources = append(sources, img)
	}

	return &QueryResponse{
		Answer:    answer,
		Sources:   sources,
		IsPending: isPending,
	}, nil
}

// findDocumentImages queries the database for image chunks from the same documents
// as the search results. Returns image SourceRefs that aren't already in the results.
func (qe *QueryEngine) findDocumentImages(results []vectorstore.SearchResult) []SourceRef {
	// Check if results already have images
	for _, r := range results {
		if r.ImageURL != "" {
			return nil // already have images, no need to enrich
		}
	}

	// Collect unique document IDs
	docIDs := make(map[string]string) // docID -> docName
	for _, r := range results {
		if r.DocumentID != "" {
			docIDs[r.DocumentID] = r.DocumentName
		}
	}
	if len(docIDs) == 0 {
		return nil
	}

	var images []SourceRef
	for docID, docName := range docIDs {
		rows, err := qe.db.Query(
			`SELECT image_url, chunk_text FROM chunks WHERE document_id = ? AND image_url != '' AND image_url IS NOT NULL`,
			docID,
		)
		if err != nil {
			continue
		}
		for rows.Next() {
			var imgURL, chunkText string
			if err := rows.Scan(&imgURL, &chunkText); err != nil {
				continue
			}
			if imgURL == "" {
				continue
			}
			images = append(images, SourceRef{
				DocumentName: docName,
				ChunkIndex:   -1,
				Snippet:      chunkText,
				ImageURL:     imgURL,
			})
		}
		rows.Close()
	}

	return images
}

// findSimilarPendingQuestion checks if there's already a pending question similar
// to the given question. Uses local text similarity (Jaccard on character bigrams)
// to avoid unnecessary embedding API calls.
// Returns the existing question text if found, empty string otherwise.
func (qe *QueryEngine) findSimilarPendingQuestion(question string, queryVector []float64) string {
	rows, err := qe.db.Query(
		`SELECT question FROM pending_questions WHERE status = 'pending' ORDER BY created_at DESC LIMIT 50`,
	)
	if err != nil {
		return ""
	}
	defer rows.Close()

	var pendingQuestions []string
	for rows.Next() {
		var q string
		if err := rows.Scan(&q); err != nil {
			continue
		}
		pendingQuestions = append(pendingQuestions, q)
	}
	if len(pendingQuestions) == 0 {
		return ""
	}

	// Use local text similarity instead of embedding API to save API calls
	for _, pq := range pendingQuestions {
		if textSimilarity(question, pq) >= 0.7 {
			return pq
		}
	}
	return ""
}

// textSimilarity computes Jaccard similarity on character bigrams between two strings.
// This is a fast, local approximation that avoids embedding API calls.
func textSimilarity(a, b string) float64 {
	if a == b {
		return 1.0
	}
	bigramsA := charBigrams(strings.ToLower(a))
	bigramsB := charBigrams(strings.ToLower(b))
	if len(bigramsA) == 0 || len(bigramsB) == 0 {
		return 0
	}
	intersection := 0
	for bg := range bigramsA {
		if bigramsB[bg] {
			intersection++
		}
	}
	union := len(bigramsA) + len(bigramsB) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

// charBigrams extracts character bigrams from a string.
func charBigrams(s string) map[string]bool {
	runes := []rune(s)
	result := make(map[string]bool)
	for i := 0; i < len(runes)-1; i++ {
		result[string(runes[i:i+2])] = true
	}
	return result
}

// cosineSimilarity computes the cosine similarity between two vectors.
func cosineSimilarity(a, b []float64) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, normA, normB float64
	for i := range a {
		dot += a[i] * b[i]
		normA += a[i] * a[i]
		normB += b[i] * b[i]
	}
	if normA == 0 || normB == 0 {
		return 0
	}
	return dot / (math.Sqrt(normA) * math.Sqrt(normB))
}

// createPendingQuestion inserts a new pending question record into the database.
func (qe *QueryEngine) createPendingQuestion(question, userID, imageData string) error {
	id, err := generateID()
	if err != nil {
		return err
	}
	_, err = qe.db.Exec(
		`INSERT INTO pending_questions (id, question, user_id, status, image_data, created_at) VALUES (?, ?, ?, ?, ?, ?)`,
		id, question, userID, "pending", imageData, time.Now().UTC(),
	)
	return err
}

// isUnableToAnswer detects if the LLM response indicates it could not find
// the answer in the reference materials, in both Chinese and English.
func isUnableToAnswer(answer string) bool {
	lower := strings.ToLower(answer)
	patterns := []string{
		// Chinese patterns
		"未提及", "未找到", "没有相关信息", "没有提及", "未涉及",
		"没有涉及", "无法从参考资料", "参考资料中没有",
		"没有找到相关", "未包含", "没有包含",
		"无相关信息", "暂无相关", "未能找到",
		// English patterns
		"not mentioned", "no relevant information",
		"not found in the reference", "no information available",
		"does not contain", "do not have information",
		"not covered in the reference", "unable to find",
		"not available in the provided",
	}
	for _, p := range patterns {
		if strings.Contains(lower, strings.ToLower(p)) {
			return true
		}
	}
	return false
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
