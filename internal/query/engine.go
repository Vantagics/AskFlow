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
	"sort"
	"strings"
	"sync"
	"time"

	"askflow/internal/config"
	"askflow/internal/embedding"
	"askflow/internal/llm"
	"askflow/internal/vectorstore"
)

// QueryRequest represents a user's question submission.
type QueryRequest struct {
	Question  string `json:"question"`
	UserID    string `json:"user_id"`
	ProductID string `json:"product_id"`
	ImageData string `json:"image_data,omitempty"` // base64 data URL from clipboard paste
}


// QueryResponse represents the result of a RAG query.
type QueryResponse struct {
	Answer        string      `json:"answer"`
	Sources       []SourceRef `json:"sources"`
	IsPending     bool        `json:"is_pending"`
	AllowDownload bool        `json:"allow_download"`
	Message       string      `json:"message,omitempty"`
	DebugInfo     *DebugInfo  `json:"debug_info,omitempty"`
}

// DebugInfo holds diagnostic information for debugging the query pipeline.
type DebugInfo struct {
	Intent          string            `json:"intent"`
	VectorDim       int               `json:"vector_dim"`
	TopK            int               `json:"top_k"`
	Threshold       float64           `json:"threshold"`
	ResultCount     int               `json:"result_count"`
	RelaxedSearch   bool              `json:"relaxed_search"`
	RelaxedResults  []DebugSearchHit  `json:"relaxed_results,omitempty"`
	TopResults      []DebugSearchHit  `json:"top_results,omitempty"`
	LLMUnableAnswer bool              `json:"llm_unable_answer"`
	Steps           []string          `json:"steps"`
}

// SourceRef represents a reference to a source document chunk.
type SourceRef struct {
	DocumentID   string  `json:"document_id,omitempty"`
	DocumentName string  `json:"document_name"`
	DocumentType string  `json:"document_type,omitempty"`
	ChunkIndex   int     `json:"chunk_index"`
	Snippet      string  `json:"snippet"`
	ImageURL     string  `json:"image_url,omitempty"`
	StartTime    float64 `json:"start_time,omitempty"` // 视频起始时间（秒）
	EndTime      float64 `json:"end_time,omitempty"`   // 视频结束时间（秒）
}


// DebugSearchHit holds a single search result's diagnostic info.
type DebugSearchHit struct {
	DocName  string  `json:"doc_name"`
	Score    float64 `json:"score"`
	DimMatch bool    `json:"dim_match"`
}

// embeddingCacheEntry holds a cached embedding vector with expiry.
type embeddingCacheEntry struct {
	vector    []float64
	timestamp time.Time
}

// embeddingCache provides a ring-buffer LRU cache for embedding API results.
// Uses O(1) eviction instead of O(n) slice copy.
type embeddingCache struct {
	mu      sync.Mutex
	entries map[string]embeddingCacheEntry
	ring    []string // ring buffer for eviction order
	head    int
	count   int
	maxSize int
	ttl     time.Duration
}

func newEmbeddingCache(maxSize int, ttl time.Duration) *embeddingCache {
	return &embeddingCache{
		entries: make(map[string]embeddingCacheEntry, maxSize),
		ring:    make([]string, maxSize),
		maxSize: maxSize,
		ttl:     ttl,
	}
}

func (ec *embeddingCache) get(text string) ([]float64, bool) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	entry, ok := ec.entries[text]
	if !ok || time.Since(entry.timestamp) > ec.ttl {
		if ok {
			delete(ec.entries, text)
		}
		return nil, false
	}
	return entry.vector, true
}

func (ec *embeddingCache) put(text string, vector []float64) {
	ec.mu.Lock()
	defer ec.mu.Unlock()
	if _, ok := ec.entries[text]; !ok {
		if ec.count >= ec.maxSize {
			evictIdx := (ec.head - ec.count + ec.maxSize) % ec.maxSize
			delete(ec.entries, ec.ring[evictIdx])
		} else {
			ec.count++
		}
		ec.ring[ec.head] = text
		ec.head = (ec.head + 1) % ec.maxSize
	}
	ec.entries[text] = embeddingCacheEntry{vector: vector, timestamp: time.Now()}
}

// QueryEngine orchestrates the RAG query flow: embed → search → LLM generate or pending.
type QueryEngine struct {
	mu               sync.RWMutex
	embeddingService embedding.EmbeddingService
	vectorStore      vectorstore.VectorStore
	llmService       llm.LLMService
	db               *sql.DB
	config           *config.Config
	embedCache       *embeddingCache // caches embedding API results to avoid redundant calls
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
		embedCache:       newEmbeddingCache(512, 10*time.Minute),
	}
}

// cachedEmbed returns the embedding for text, using cache when available.
func (qe *QueryEngine) cachedEmbed(text string, es embedding.EmbeddingService) ([]float64, error) {
	if vec, ok := qe.embedCache.get(text); ok {
		return vec, nil
	}
	vec, err := es.Embed(text)
	if err != nil {
		return nil, err
	}
	qe.embedCache.put(text, vec)
	return vec, nil
}

// UpdateServices replaces the embedding and LLM services (used after config change).
func (qe *QueryEngine) UpdateServices(es embedding.EmbeddingService, ls llm.LLMService, cfg *config.Config) {
	qe.mu.Lock()
	defer qe.mu.Unlock()
	qe.embeddingService = es
	qe.llmService = ls
	qe.config = cfg
}

// getServices returns a snapshot of the current services under read lock.
func (qe *QueryEngine) getServices() (embedding.EmbeddingService, llm.LLMService, *config.Config) {
	qe.mu.RLock()
	defer qe.mu.RUnlock()
	return qe.embeddingService, qe.llmService, qe.config
}

// TranslateText translates the given text to the target language using LLM.
func (qe *QueryEngine) TranslateText(text, targetLang string) (string, error) {
	if text == "" {
		return "", nil
	}
	_, ls, _ := qe.getServices()
	langName := targetLang
	switch targetLang {
	case "zh-CN":
		langName = "简体中文"
	case "en-US", "en":
		langName = "English"
	}
	prompt := fmt.Sprintf("你是一个翻译助手。将以下文本翻译为%s。只输出翻译结果，不要添加任何解释或引号。如果文本已经是目标语言，直接原样输出。", langName)
	translated, err := ls.Generate(prompt, []string{text}, text)
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
func (qe *QueryEngine) classifyIntent(question string, ls llm.LLMService, cfg *config.Config) (*IntentResult, error) {
	productIntro := ""
	if cfg != nil {
		productIntro = cfg.ProductIntro
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

	answer, err := ls.Generate(systemPrompt, nil, question)
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
	// Snapshot services under read lock for concurrency safety
	es, ls, cfg := qe.getServices()

	// Initialize debug info if debug mode is enabled
	debugMode := cfg != nil && cfg.Vector.DebugMode
	var dbg *DebugInfo
	if debugMode {
		dbg = &DebugInfo{
			TopK:      cfg.Vector.TopK,
			Threshold: cfg.Vector.Threshold,
		}
	}

	// Step 0: Intent classification (skip if image is attached — image may contain product info)
	// Also skip for knowledge_base products — they should answer all questions without filtering
	skipIntentClassification := req.ImageData != ""
	if !skipIntentClassification && req.ProductID != "" {
		var pType string
		err := qe.db.QueryRow("SELECT COALESCE(type, 'service') FROM products WHERE id = ?", req.ProductID).Scan(&pType)
		if err == nil && pType == "knowledge_base" {
			skipIntentClassification = true
			if debugMode {
				dbg.Intent = "product"
				dbg.Steps = append(dbg.Steps, "Step 0: product type=knowledge_base, skipping intent classification")
			}
		}
	}
	if !skipIntentClassification {
		intent, err := qe.classifyIntent(req.Question, ls, cfg)
		if err == nil {
			switch intent.Intent {
			case "greeting":
				if debugMode {
					dbg.Intent = "greeting"
					dbg.Steps = append(dbg.Steps, "Step 0: intent=greeting, returning product intro")
				}
				// Return product intro as greeting response, in the user's language
				intro := "您好！欢迎使用我们的产品。"
				if cfg != nil && cfg.ProductIntro != "" {
					intro = cfg.ProductIntro
				}
				// Use LLM to translate the greeting to match the user's question language
				translated, tErr := ls.Generate(
					"你是一个翻译助手。将以下内容翻译为与用户提问相同的语言。如果用户用英文提问，翻译为英文；如果用户用中文提问，保持中文。只输出翻译结果，不要添加任何解释。",
					[]string{intro},
					req.Question,
				)
				if tErr == nil && translated != "" {
					intro = translated
				}
				return &QueryResponse{Answer: intro, DebugInfo: dbg}, nil
			case "irrelevant":
				if debugMode {
					dbg.Intent = "irrelevant"
					dbg.Steps = append(dbg.Steps, "Step 0: intent=irrelevant, reason="+intent.Reason)
				}
				msg := "抱歉，这个问题与我们的产品无关。请问有什么产品方面的问题需要帮助吗？"
				if intent.Reason != "" {
					msg = "抱歉，" + intent.Reason + "。请问有什么产品方面的问题需要帮助吗？"
				}
				translated, tErr := ls.Generate(
					"你是一个翻译助手。将以下内容翻译为与用户提问相同的语言。如果用户用英文提问，翻译为英文；如果用户用中文提问，保持中文。只输出翻译结果，不要添加任何解释。",
					[]string{msg},
					req.Question,
				)
				if tErr == nil && translated != "" {
					msg = translated
				}
				return &QueryResponse{Answer: msg, DebugInfo: dbg}, nil
			}
		}
	}

	if debugMode {
		dbg.Intent = "product"
		dbg.Steps = append(dbg.Steps, "Step 0: intent=product, proceeding to RAG pipeline")
	}

	// ===== 3-Level Text Similarity Processing =====
	// Level 1: Text-based matching (free — no API calls)
	// Level 2: Vector search + cached answer reuse (embedding API only, no LLM)
	// Level 3: Full RAG pipeline (embedding + LLM)
	textMatchEnabled := cfg != nil && cfg.Vector.TextMatchEnabled

	if textMatchEnabled && req.ImageData == "" {
		if debugMode {
			dbg.Steps = append(dbg.Steps, "TextMatch: Level 1 — text-based matching (no API cost)")
		}

		// Level 1: Text-based search against chunk cache
		textResults, textErr := qe.vectorStore.TextSearch(req.Question, 3, 0.65, req.ProductID)
		if textErr == nil && len(textResults) > 0 && textResults[0].Score >= 0.75 {
			log.Printf("[Query] Level 1 text match hit: score=%.4f doc=%q", textResults[0].Score, textResults[0].DocumentName)
			if debugMode {
				dbg.Steps = append(dbg.Steps, fmt.Sprintf("TextMatch: Level 1 HIT score=%.4f doc=%q", textResults[0].Score, textResults[0].DocumentName))
			}

			// Check if this chunk belongs to a pending-answer doc (has cached LLM answer)
			cachedAnswer := qe.findCachedAnswer(textResults[0].DocumentID)
			if cachedAnswer != "" {
				log.Printf("[Query] Level 1 returning cached answer (zero API cost)")
				if debugMode {
					dbg.Steps = append(dbg.Steps, "TextMatch: Level 1 returning cached answer — zero API cost")
				}
				textResults = qe.enrichVideoTimeInfo(textResults)
				sources := qe.buildSourceRefs(textResults)
				return &QueryResponse{Answer: cachedAnswer, Sources: sources, DebugInfo: dbg}, nil
			}

			// Level 2: We have a good text match but no cached answer.
			// Use embedding to confirm, then try to reuse a cached answer from similar pending Q&A.
			if debugMode {
				dbg.Steps = append(dbg.Steps, "TextMatch: Level 2 — confirming with embedding (embedding API only)")
			}
			queryVector, embErr := qe.cachedEmbed(req.Question, es)
			if embErr == nil {
				vecResults, vecErr := qe.vectorStore.Search(queryVector, cfg.Vector.TopK, cfg.Vector.Threshold, req.ProductID)
				if vecErr == nil && len(vecResults) > 0 && vecResults[0].Score >= 0.75 {
					log.Printf("[Query] Level 2 vector confirmed: score=%.4f", vecResults[0].Score)
					if debugMode {
						dbg.VectorDim = len(queryVector)
						dbg.Steps = append(dbg.Steps, fmt.Sprintf("TextMatch: Level 2 vector confirmed score=%.4f", vecResults[0].Score))
					}

					// Try to find a cached LLM answer from the top vector result
					cachedAnswer = qe.findCachedAnswer(vecResults[0].DocumentID)
					if cachedAnswer != "" {
						log.Printf("[Query] Level 2 returning cached answer (embedding API only, no LLM)")
						if debugMode {
							dbg.Steps = append(dbg.Steps, "TextMatch: Level 2 returning cached answer — no LLM cost")
						}
						vecResults = qe.enrichVideoTimeInfo(vecResults)
						sources := qe.buildSourceRefs(vecResults)
						return &QueryResponse{Answer: cachedAnswer, Sources: sources, DebugInfo: dbg}, nil
					}
				}
			}
			if debugMode {
				dbg.Steps = append(dbg.Steps, "TextMatch: no cached answer found, falling through to Level 3 (full RAG)")
			}
		} else {
			if debugMode {
				score := 0.0
				if textErr == nil && len(textResults) > 0 {
					score = textResults[0].Score
				}
				dbg.Steps = append(dbg.Steps, fmt.Sprintf("TextMatch: Level 1 miss (best_score=%.4f), proceeding to Level 3", score))
			}
		}
	}

	// ===== Level 3: Full RAG Pipeline =====

	// Step 1: Embed the question
	queryVector, err := qe.cachedEmbed(req.Question, es)
	if err != nil {
		return nil, fmt.Errorf("failed to embed question: %w", err)
	}
	log.Printf("[Query] question_len=%d, vector_dim=%d", len(req.Question), len(queryVector))
	if debugMode {
		dbg.VectorDim = len(queryVector)
		dbg.Steps = append(dbg.Steps, fmt.Sprintf("Step 1: embedded question, vector_dim=%d", len(queryVector)))
	}

	// Step 2: Search vector store
	topK := cfg.Vector.TopK
	threshold := cfg.Vector.Threshold
	results, err := qe.vectorStore.Search(queryVector, topK, threshold, req.ProductID)
	if err != nil {
		return nil, fmt.Errorf("failed to search vector store: %w", err)
	}
	log.Printf("[Query] search topK=%d threshold=%.2f results=%d", topK, threshold, len(results))
	if debugMode {
		dbg.ResultCount = len(results)
		dbg.Steps = append(dbg.Steps, fmt.Sprintf("Step 2: search topK=%d threshold=%.2f results=%d", topK, threshold, len(results)))
		for i, r := range results {
			if i >= 5 {
				break
			}
			dbg.TopResults = append(dbg.TopResults, DebugSearchHit{DocName: r.DocumentName, Score: r.Score, DimMatch: true})
		}
	}

	// Step 2.5: If image provided, also search with image embedding and merge results
	var imgVec []float64
	if req.ImageData != "" {
		var imgErr error
		imgVec, imgErr = es.EmbedImageURL(req.ImageData)
		if imgErr != nil {
			log.Printf("[Query] image embedding failed: %v", imgErr)
		} else {
			log.Printf("[Query] image vector_dim=%d", len(imgVec))
			// Use a lower threshold for image search since cross-modal similarity scores are typically lower
			imgThreshold := threshold * 0.6
			if imgThreshold < 0.3 {
				imgThreshold = 0.3
			}
			imgResults, imgSearchErr := qe.vectorStore.Search(imgVec, topK, imgThreshold, req.ProductID)
			if imgSearchErr == nil && len(imgResults) > 0 {
				log.Printf("[Query] image search results=%d (threshold=%.2f)", len(imgResults), imgThreshold)
				results = mergeSearchResults(results, imgResults, topK)
			}
		}
	}

	// Step 3: If no results above threshold, try with lower threshold before giving up
	if len(results) == 0 {
		if debugMode {
			dbg.RelaxedSearch = true
			dbg.Steps = append(dbg.Steps, "Step 3: no results above threshold, trying relaxed search (threshold=0.0, accept>=0.3)")
		}
		relaxedResults, _ := qe.vectorStore.Search(queryVector, 3, 0.0, req.ProductID)
		log.Printf("[Query] relaxed search results=%d", len(relaxedResults))
		for i, r := range relaxedResults {
			log.Printf("[Query]   relaxed[%d] score=%.4f doc=%q dim_match=%v", i, r.Score, r.DocumentName, true)
			if debugMode {
				dbg.RelaxedResults = append(dbg.RelaxedResults, DebugSearchHit{DocName: r.DocumentName, Score: r.Score, DimMatch: true})
			}
		}
		if debugMode && len(relaxedResults) == 0 {
			dbg.Steps = append(dbg.Steps, "Step 3: relaxed search returned 0 results (vector store is empty)")
		}
		if len(relaxedResults) > 0 && relaxedResults[0].Score >= 0.3 {
			results = relaxedResults[:1]
			if debugMode {
				dbg.Steps = append(dbg.Steps, fmt.Sprintf("Step 3: accepted relaxed result score=%.4f", relaxedResults[0].Score))
			}
		}

		// Also try relaxed search with image vector
		if len(results) == 0 && len(imgVec) > 0 {
			imgRelaxed, _ := qe.vectorStore.Search(imgVec, 3, 0.0, req.ProductID)
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
	if len(results) > 1 && cfg != nil {
		priority := cfg.Vector.ContentPriority
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

	// Step 3.6: Enrich search results with video time information from video_segments table
	results = qe.enrichVideoTimeInfo(results)

	// Step 4: If still no results, create pending question
	if len(results) == 0 {
		if debugMode {
			dbg.Steps = append(dbg.Steps, "Step 4: no results after all searches, falling back to pending question")
		}
		// Check for existing similar pending question first
		if existing := qe.findSimilarPendingQuestion(req.Question, queryVector); existing != "" {
			if debugMode {
				dbg.Steps = append(dbg.Steps, "Step 4: found similar pending question, returning 'already processing'")
			}
			pendingMsg := "该问题已在处理中，请耐心等待回复"
			translated, tErr := ls.Generate(
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
				DebugInfo: dbg,
			}, nil
		}

		if err := qe.createPendingQuestion(req.Question, req.UserID, req.ImageData, req.ProductID); err != nil {
			return nil, fmt.Errorf("failed to create pending question: %w", err)
		}
		if debugMode {
			dbg.Steps = append(dbg.Steps, "Step 4: created new pending question, returning 'transferred to manual'")
		}
		pendingMsg := "该问题已转交人工处理，请稍后查看回复"
		translated, tErr := ls.Generate(
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
			DebugInfo: dbg,
		}, nil
	}

	if debugMode {
		dbg.Steps = append(dbg.Steps, fmt.Sprintf("Step 4: skipped (have %d results), proceeding to LLM", len(results)))
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

	// Use vision LLM when user attached an image
	var answer string
	if req.ImageData != "" {
		visionPrompt := systemPrompt
		if visionPrompt == "" {
			visionPrompt = "你是一个专业的软件技术支持助手。用户上传了一张图片并提出了问题。" +
				"请结合图片内容和提供的参考资料来回答用户的问题。" +
				"如果参考资料中没有相关信息，请根据图片内容尽可能回答。回答应简洁、准确、有条理。" +
				"\n\n重要规则：你必须使用与用户提问相同的语言来回答。"
		}
		answer, err = ls.GenerateWithImage(visionPrompt, context, req.Question, req.ImageData)
	} else {
		answer, err = ls.Generate(systemPrompt, context, req.Question)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to generate answer: %w", err)
	}

	// Step 5.5: Detect "unable to answer" responses and create pending question
	isPending := false
	if isUnableToAnswer(answer) {
		log.Printf("[Query] LLM answer indicates unable to answer, creating pending question")
		if debugMode {
			dbg.LLMUnableAnswer = true
			dbg.Steps = append(dbg.Steps, "Step 5.5: LLM indicated unable to answer, creating pending question")
		}
		if existing := qe.findSimilarPendingQuestion(req.Question, queryVector); existing != "" {
			isPending = true
		} else {
			_ = qe.createPendingQuestion(req.Question, req.UserID, req.ImageData, req.ProductID)
			isPending = true
		}
	} else if debugMode {
		dbg.Steps = append(dbg.Steps, "Step 5.5: LLM answered successfully")
	}

	// Step 6: Build source references
	sources := qe.buildSourceRefs(results)

	// Append document images that weren't already in search results
	for _, img := range docImages {
		sources = append(sources, img)
	}

	return &QueryResponse{
		Answer:    answer,
		Sources:   sources,
		IsPending: isPending,
		DebugInfo: dbg,
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

	// Batch query: single IN clause instead of N+1 queries
	ids := make([]string, 0, len(docIDs))
	for id := range docIDs {
		ids = append(ids, id)
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}

	query := `SELECT document_id, image_url, chunk_text FROM chunks WHERE document_id IN (` +
		strings.Join(placeholders, ",") + `) AND image_url != '' AND image_url IS NOT NULL`
	rows, err := qe.db.Query(query, args...)
	if err != nil {
		return nil
	}
	defer rows.Close()

	var images []SourceRef
	for rows.Next() {
		var docID, imgURL, chunkText string
		if err := rows.Scan(&docID, &imgURL, &chunkText); err != nil {
			continue
		}
		if imgURL == "" {
			continue
		}
		images = append(images, SourceRef{
			DocumentName: docIDs[docID],
			ChunkIndex:   -1,
			Snippet:      chunkText,
			ImageURL:     imgURL,
		})
	}

	return images
}

// findCachedAnswer looks up a cached LLM answer for a document ID.
// If the document is a pending-answer (docID starts with "pending-answer-"),
// it returns the stored llm_answer from the pending_questions table.
// This allows Level 1/2 to skip the LLM call entirely.
func (qe *QueryEngine) findCachedAnswer(documentID string) string {
	if !strings.HasPrefix(documentID, "pending-answer-") {
		return ""
	}
	questionID := strings.TrimPrefix(documentID, "pending-answer-")
	var llmAnswer sql.NullString
	err := qe.db.QueryRow(
		`SELECT llm_answer FROM pending_questions WHERE id = ? AND status = 'answered'`,
		questionID,
	).Scan(&llmAnswer)
	if err != nil || !llmAnswer.Valid || strings.TrimSpace(llmAnswer.String) == "" {
		return ""
	}
	return llmAnswer.String
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


// createPendingQuestion inserts a new pending question record into the database.
func (qe *QueryEngine) createPendingQuestion(question, userID, imageData, productID string) error {
	id, err := generateID()
	if err != nil {
		return err
	}
	_, err = qe.db.Exec(
		`INSERT INTO pending_questions (id, question, user_id, status, image_data, product_id, created_at) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		id, question, userID, "pending", imageData, productID, time.Now().UTC(),
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
	sort.Slice(merged, func(i, j int) bool {
		return merged[i].Score > merged[j].Score
	})

	if len(merged) > topK {
		merged = merged[:topK]
	}
	return merged
}
// enrichVideoTimeInfo queries the video_segments table to fill in StartTime and EndTime
// for search results that correspond to video content.
// Uses a single batch query instead of per-result queries for better performance.
func (qe *QueryEngine) enrichVideoTimeInfo(results []vectorstore.SearchResult) []vectorstore.SearchResult {
	if qe.db == nil || len(results) == 0 {
		return results
	}

	// Build batch query with all chunk IDs
	chunkIDs := make([]string, len(results))
	chunkIDToIdx := make(map[string][]int, len(results))
	for i, r := range results {
		id := fmt.Sprintf("%s-%d", r.DocumentID, r.ChunkIndex)
		chunkIDs[i] = id
		chunkIDToIdx[id] = append(chunkIDToIdx[id], i)
	}

	// Build IN clause: SELECT chunk_id, start_time, end_time FROM video_segments WHERE chunk_id IN (?,?,...)
	placeholders := make([]string, len(chunkIDs))
	args := make([]interface{}, len(chunkIDs))
	for i, id := range chunkIDs {
		placeholders[i] = "?"
		args[i] = id
	}

	query := `SELECT chunk_id, start_time, end_time FROM video_segments WHERE chunk_id IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := qe.db.Query(query, args...)
	if err != nil {
		return results
	}
	defer rows.Close()

	for rows.Next() {
		var chunkID string
		var startTime, endTime float64
		if err := rows.Scan(&chunkID, &startTime, &endTime); err != nil {
			continue
		}
		for _, idx := range chunkIDToIdx[chunkID] {
			results[idx].StartTime = startTime
			results[idx].EndTime = endTime
		}
	}
	return results
}



// lookupDocumentTypes queries the documents table to get the type for each unique document ID.
// Returns a map from document_id to document type (e.g., "video", "pdf", "word").
func (qe *QueryEngine) lookupDocumentTypes(docIDs []string) map[string]string {
	result := make(map[string]string)
	if qe.db == nil || len(docIDs) == 0 {
		return result
	}
	// Deduplicate
	unique := make(map[string]bool)
	var ids []string
	for _, id := range docIDs {
		if !unique[id] {
			unique[id] = true
			ids = append(ids, id)
		}
	}
	placeholders := make([]string, len(ids))
	args := make([]interface{}, len(ids))
	for i, id := range ids {
		placeholders[i] = "?"
		args[i] = id
	}
	q := `SELECT id, type FROM documents WHERE id IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := qe.db.Query(q, args...)
	if err != nil {
		return result
	}
	defer rows.Close()
	for rows.Next() {
		var id, docType string
		if err := rows.Scan(&id, &docType); err != nil {
			continue
		}
		result[id] = docType
	}
	return result
}

// buildSourceRefs converts search results into SourceRef slice, enriching with document type info.
func (qe *QueryEngine) buildSourceRefs(results []vectorstore.SearchResult) []SourceRef {
	// Collect document IDs
	docIDs := make([]string, 0, len(results))
	for _, r := range results {
		docIDs = append(docIDs, r.DocumentID)
	}
	docTypes := qe.lookupDocumentTypes(docIDs)

	sources := make([]SourceRef, len(results))
	for i, r := range results {
		snippet := r.ChunkText
		if len(snippet) > 100 {
			snippet = snippet[:100]
		}
		sources[i] = SourceRef{
			DocumentID:   r.DocumentID,
			DocumentName: r.DocumentName,
			DocumentType: docTypes[r.DocumentID],
			ChunkIndex:   r.ChunkIndex,
			Snippet:      snippet,
			ImageURL:     r.ImageURL,
			StartTime:    r.StartTime,
			EndTime:      r.EndTime,
		}
	}
	return sources
}
