// Package embedding provides the Embedding service client for converting text
// to vector representations via OpenAI-compatible API endpoints.
package embedding

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"askflow/internal/errlog"
)

// EmbeddingService defines the interface for text and image embedding operations.
type EmbeddingService interface {
	Embed(text string) ([]float64, error)
	EmbedBatch(texts []string) ([][]float64, error)
	EmbedImageURL(imageURL string) ([]float64, error)
}

// APIEmbeddingService implements EmbeddingService using an OpenAI-compatible API.
type APIEmbeddingService struct {
	Endpoint      string
	APIKey        string
	ModelName     string
	UseMultimodal bool
	client        *http.Client
	mmClient      *http.Client // longer timeout for multimodal (image) requests
}

// NewAPIEmbeddingService creates a new APIEmbeddingService with the given configuration.
func NewAPIEmbeddingService(endpoint, apiKey, modelName string, useMultimodal bool) *APIEmbeddingService {
	// Warn if API key is sent over non-HTTPS connection
	if apiKey != "" && !strings.HasPrefix(strings.ToLower(endpoint), "https://") {
		log.Printf("[WARNING] Embedding API key is being sent over non-HTTPS endpoint: %s", endpoint)
	}
	return &APIEmbeddingService{
		Endpoint:      endpoint,
		APIKey:        apiKey,
		ModelName:     modelName,
		UseMultimodal: useMultimodal,
		client: &http.Client{
			Timeout: 30 * time.Second,
		},
		mmClient: &http.Client{
			Timeout: 120 * time.Second,
		},
	}
}

// --- Standard (OpenAI-compatible) types ---

type embeddingRequest struct {
	Model string      `json:"model"`
	Input interface{} `json:"input"`
}

type embeddingResponse struct {
	Data  []embeddingData `json:"data"`
	Error *apiError       `json:"error,omitempty"`
}

type embeddingData struct {
	Embedding []float64 `json:"embedding"`
	Index     int       `json:"index"`
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// --- Multimodal types ---

type multimodalInputItem struct {
	Type     string                `json:"type"`
	Text     string                `json:"text,omitempty"`
	ImageURL *multimodalImageURL   `json:"image_url,omitempty"`
}

type multimodalImageURL struct {
	URL string `json:"url"`
}

type multimodalRequest struct {
	Model string                `json:"model"`
	Input []multimodalInputItem `json:"input"`
}

type multimodalResponse struct {
	Data  multimodalData `json:"data"`
	Error *apiError      `json:"error,omitempty"`
}

type multimodalData struct {
	Embedding []float64 `json:"embedding"`
}

// Embed converts a single text string into an embedding vector.
func (s *APIEmbeddingService) Embed(text string) ([]float64, error) {
	if s.Endpoint == "" {
		return nil, fmt.Errorf("embedding API endpoint not configured")
	}
	if s.UseMultimodal {
		return s.embedMultimodal(text)
	}
	results, err := s.callAPI(text)
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("embedding API returned no results")
	}
	return results[0].Embedding, nil
}

// EmbedBatch converts multiple text strings into embedding vectors.
func (s *APIEmbeddingService) EmbedBatch(texts []string) ([][]float64, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if s.Endpoint == "" {
		return nil, fmt.Errorf("embedding API endpoint not configured")
	}
	// Limit batch size to prevent excessive API payload
	const maxBatchSize = 256
	if len(texts) > maxBatchSize {
		return nil, fmt.Errorf("batch size %d exceeds maximum of %d", len(texts), maxBatchSize)
	}
	if s.UseMultimodal {
		return s.embedBatchMultimodal(texts)
	}
	results, err := s.callAPI(texts)
	if err != nil {
		return nil, err
	}
	if len(results) != len(texts) {
		return nil, fmt.Errorf("embedding API returned %d results, expected %d", len(results), len(texts))
	}
	embeddings := make([][]float64, len(texts))
	for _, d := range results {
		if d.Index < 0 || d.Index >= len(texts) {
			return nil, fmt.Errorf("embedding API returned invalid index %d", d.Index)
		}
		embeddings[d.Index] = d.Embedding
	}
	return embeddings, nil
}

// --- Standard API call ---

func (s *APIEmbeddingService) callAPI(input interface{}) ([]embeddingData, error) {
	reqBody := embeddingRequest{
		Model: s.ModelName,
		Input: input,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	apiURL := strings.TrimRight(s.Endpoint, "/") + "/embeddings"

	const maxRetries = 3
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt) * 5 * time.Second
			log.Printf("[Embed] text embedding retry %d/%d after %v", attempt+1, maxRetries, backoff)
			time.Sleep(backoff)
		}

		req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if s.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+s.APIKey)
		}

		resp, err := s.client.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("embedding API request failed: %w", err)
			continue
		}

		respBody, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20)) // 50MB max response
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read response body: %w", err)
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("embedding API error (HTTP %d): %s", resp.StatusCode, string(respBody))
			continue
		}

		if resp.StatusCode != http.StatusOK {
			var errResp embeddingResponse
			if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != nil {
				errlog.Logf("[Embed] text embedding API error (HTTP %d): %s", resp.StatusCode, errResp.Error.Message)
				return nil, fmt.Errorf("embedding API error (HTTP %d): %s", resp.StatusCode, errResp.Error.Message)
			}
			errlog.Logf("[Embed] text embedding API error (HTTP %d): %s", resp.StatusCode, string(respBody))
			return nil, fmt.Errorf("embedding API error (HTTP %d): %s", resp.StatusCode, string(respBody))
		}

		var result embeddingResponse
		if err := json.Unmarshal(respBody, &result); err != nil {
			return nil, fmt.Errorf("failed to decode response: %w", err)
		}
		if result.Error != nil {
			return nil, fmt.Errorf("embedding API error: %s", result.Error.Message)
		}

		return result.Data, nil
	}

	errlog.Logf("[Embed] text embedding API failed after %d retries: %v", maxRetries, lastErr)
	return nil, lastErr
}

// --- Multimodal API calls ---

func (s *APIEmbeddingService) embedMultimodal(text string) ([]float64, error) {
	input := []multimodalInputItem{{Type: "text", Text: text}}
	vec, err := s.callMultimodalAPI(input)
	if err != nil {
		return nil, err
	}
	if len(vec) == 0 {
		return nil, fmt.Errorf("multimodal embedding API returned empty vector")
	}
	return vec, nil
}

func (s *APIEmbeddingService) embedBatchMultimodal(texts []string) ([][]float64, error) {
	embeddings := make([][]float64, len(texts))
	for i, text := range texts {
		vec, err := s.embedMultimodal(text)
		if err != nil {
			return nil, fmt.Errorf("embed text[%d]: %w", i, err)
		}
		embeddings[i] = vec
	}
	return embeddings, nil
}

// EmbedImageURL embeds an image via its URL using the multimodal API.
func (s *APIEmbeddingService) EmbedImageURL(imageURL string) ([]float64, error) {
	if s.Endpoint == "" {
		return nil, fmt.Errorf("embedding API endpoint not configured")
	}
	if !s.UseMultimodal {
		return nil, fmt.Errorf("image embedding requires multimodal mode")
	}
	input := []multimodalInputItem{{
		Type:     "image_url",
		ImageURL: &multimodalImageURL{URL: imageURL},
	}}
	vec, err := s.callMultimodalAPI(input)
	if err != nil {
		return nil, err
	}
	if len(vec) == 0 {
		return nil, fmt.Errorf("multimodal embedding API returned empty vector for image")
	}
	return vec, nil
}

func (s *APIEmbeddingService) callMultimodalAPI(input []multimodalInputItem) ([]float64, error) {
	reqBody := multimodalRequest{
		Model: s.ModelName,
		Input: input,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	apiURL := strings.TrimRight(s.Endpoint, "/") + "/embeddings/multimodal"

	const maxRetries = 3
	var lastErr error
	for attempt := 0; attempt < maxRetries; attempt++ {
		if attempt > 0 {
			backoff := time.Duration(attempt) * 5 * time.Second
			log.Printf("[Embed] multimodal retry %d/%d after %v", attempt+1, maxRetries, backoff)
			time.Sleep(backoff)
		}

		req, err := http.NewRequest(http.MethodPost, apiURL, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if s.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+s.APIKey)
		}

		resp, err := s.mmClient.Do(req)
		if err != nil {
			lastErr = fmt.Errorf("multimodal embedding API request failed: %w", err)
			continue
		}

		respBody, err := io.ReadAll(io.LimitReader(resp.Body, 50<<20)) // 50MB max response
		resp.Body.Close()
		if err != nil {
			lastErr = fmt.Errorf("failed to read response body: %w", err)
			continue
		}

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("embedding API error (HTTP %d): %s", resp.StatusCode, string(respBody))
			continue
		}

		if resp.StatusCode != http.StatusOK {
			errlog.Logf("[Embed] multimodal API error (HTTP %d): %s", resp.StatusCode, string(respBody))
			return nil, fmt.Errorf("embedding API error (HTTP %d): %s", resp.StatusCode, string(respBody))
		}

		var result multimodalResponse
		if err := json.Unmarshal(respBody, &result); err != nil {
			return nil, fmt.Errorf("failed to decode response: %w", err)
		}
		if result.Error != nil {
			return nil, fmt.Errorf("multimodal embedding API error: %s", result.Error.Message)
		}

		return result.Data.Embedding, nil
	}

	errlog.Logf("[Embed] multimodal API failed after %d retries: %v", maxRetries, lastErr)
	return nil, lastErr
}
