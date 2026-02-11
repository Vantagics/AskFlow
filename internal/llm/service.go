// Package llm provides the LLM service client for generating answers
// via OpenAI-compatible Chat Completion API endpoints.
package llm

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// LLMService defines the interface for LLM text generation.
type LLMService interface {
	Generate(prompt string, context []string, question string) (string, error)
}

// APILLMService implements LLMService using an OpenAI-compatible Chat Completion API.
type APILLMService struct {
	Endpoint    string
	APIKey      string
	ModelName   string
	Temperature float64
	MaxTokens   int
	client      *http.Client
}

// NewAPILLMService creates a new APILLMService with the given configuration.
func NewAPILLMService(endpoint, apiKey, modelName string, temperature float64, maxTokens int) *APILLMService {
	return &APILLMService{
		Endpoint:    endpoint,
		APIKey:      apiKey,
		ModelName:   modelName,
		Temperature: temperature,
		MaxTokens:   maxTokens,
		client: &http.Client{
			Timeout: 60 * time.Second,
		},
	}
}

// chatRequest is the request body for the OpenAI-compatible chat completion API.
type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	MaxTokens   int           `json:"max_tokens"`
}

// chatMessage represents a single message in the chat completion request.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// chatResponse is the response body from the chat completion API.
type chatResponse struct {
	Choices []chatChoice `json:"choices"`
	Error   *apiError    `json:"error,omitempty"`
}

// chatChoice represents a single choice in the chat completion response.
type chatChoice struct {
	Message chatMessage `json:"message"`
}

// apiError represents an error returned by the API.
type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// BuildMessages constructs the chat messages from the prompt, context chunks, and question.
// It returns a system message and a user message.
func BuildMessages(prompt string, context []string, question string) []chatMessage {
	systemContent := prompt
	if systemContent == "" {
		systemContent = "你是一个专业的软件技术支持助手。请根据提供的参考资料回答用户的问题。" +
			"如果参考资料中没有相关信息，请如实告知用户。回答应简洁、准确、有条理。" +
			"\n\n重要规则：你必须使用与用户提问相同的语言来回答。如果用户用英文提问，你必须用英文回答；如果用户用中文提问，你必须用中文回答；其他语言同理。无论参考资料是什么语言，都要翻译成用户提问的语言来回答。"
	}

	var userParts []string
	if len(context) > 0 {
		userParts = append(userParts, "参考资料：")
		for i, chunk := range context {
			userParts = append(userParts, fmt.Sprintf("[%d] %s", i+1, chunk))
		}
		userParts = append(userParts, "")
	}
	userParts = append(userParts, "用户问题："+question)

	return []chatMessage{
		{Role: "system", Content: systemContent},
		{Role: "user", Content: strings.Join(userParts, "\n")},
	}
}

// Generate sends a prompt with context and question to the LLM and returns the generated answer.
// It retries once on failure. If both attempts fail, it returns a fallback error message.
func (s *APILLMService) Generate(prompt string, context []string, question string) (string, error) {
	messages := BuildMessages(prompt, context, question)

	// First attempt
	answer, err := s.callAPI(messages)
	if err == nil {
		return answer, nil
	}

	// Retry once
	answer, err = s.callAPI(messages)
	if err == nil {
		return answer, nil
	}

	return "服务暂时不可用，请稍后重试", nil
}

// callAPI sends the chat completion request to the API and returns the generated text.
func (s *APILLMService) callAPI(messages []chatMessage) (string, error) {
	reqBody := chatRequest{
		Model:       s.ModelName,
		Messages:    messages,
		Temperature: s.Temperature,
		MaxTokens:   s.MaxTokens,
	}
	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	url := strings.TrimRight(s.Endpoint, "/") + "/chat/completions"
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+s.APIKey)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("LLM API request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		var errResp chatResponse
		if json.Unmarshal(respBody, &errResp) == nil && errResp.Error != nil {
			return "", fmt.Errorf("LLM API error (HTTP %d): %s", resp.StatusCode, errResp.Error.Message)
		}
		return "", fmt.Errorf("LLM API error (HTTP %d): %s", resp.StatusCode, string(respBody))
	}

	var result chatResponse
	if err := json.Unmarshal(respBody, &result); err != nil {
		return "", fmt.Errorf("failed to decode response: %w", err)
	}
	if result.Error != nil {
		return "", fmt.Errorf("LLM API error: %s", result.Error.Message)
	}
	if len(result.Choices) == 0 {
		return "", fmt.Errorf("LLM API returned no choices")
	}

	return result.Choices[0].Message.Content, nil
}
