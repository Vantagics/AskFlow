package llm

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// contentStr extracts the string content from a chatMessage.Content (which is interface{}).
func contentStr(c interface{}) string {
	if s, ok := c.(string); ok {
		return s
	}
	return ""
}

func TestBuildMessages_DefaultPrompt(t *testing.T) {
	msgs := BuildMessages("", []string{"chunk1", "chunk2"}, "什么是Go？")

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("expected system role, got %s", msgs[0].Role)
	}
	if contentStr(msgs[0].Content) == "" {
		t.Error("system message should not be empty when prompt is empty (default used)")
	}
	if msgs[1].Role != "user" {
		t.Errorf("expected user role, got %s", msgs[1].Role)
	}
	userContent := contentStr(msgs[1].Content)
	if !strings.Contains(userContent, "chunk1") {
		t.Error("user message should contain chunk1")
	}
	if !strings.Contains(userContent, "chunk2") {
		t.Error("user message should contain chunk2")
	}
	if !strings.Contains(userContent, "什么是Go？") {
		t.Error("user message should contain the question")
	}
}

func TestBuildMessages_CustomPrompt(t *testing.T) {
	customPrompt := "You are a helpful assistant."
	msgs := BuildMessages(customPrompt, []string{"ctx"}, "question?")

	if contentStr(msgs[0].Content) != customPrompt {
		t.Errorf("expected custom prompt %q, got %q", customPrompt, contentStr(msgs[0].Content))
	}
}

func TestBuildMessages_EmptyContext(t *testing.T) {
	msgs := BuildMessages("sys", nil, "q?")

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	userContent := contentStr(msgs[1].Content)
	if strings.Contains(userContent, "参考资料") {
		t.Error("user message should not contain reference header when context is empty")
	}
	if !strings.Contains(userContent, "q?") {
		t.Error("user message should contain the question")
	}
}

func TestBuildMessages_ContextNumbering(t *testing.T) {
	msgs := BuildMessages("sys", []string{"a", "b", "c"}, "q")
	content := contentStr(msgs[1].Content)
	if !strings.Contains(content, "[1] a") {
		t.Error("expected [1] a in user message")
	}
	if !strings.Contains(content, "[2] b") {
		t.Error("expected [2] b in user message")
	}
	if !strings.Contains(content, "[3] c") {
		t.Error("expected [3] c in user message")
	}
}

func TestGenerate_Success(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if !strings.HasSuffix(r.URL.Path, "/chat/completions") {
			t.Errorf("expected /chat/completions path, got %s", r.URL.Path)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Bearer test-key, got %s", r.Header.Get("Authorization"))
		}
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("expected application/json content type, got %s", r.Header.Get("Content-Type"))
		}

		var req chatRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("failed to decode request: %v", err)
		}
		if req.Model != "test-model" {
			t.Errorf("expected model test-model, got %s", req.Model)
		}
		if len(req.Messages) != 2 {
			t.Errorf("expected 2 messages, got %d", len(req.Messages))
		}

		resp := chatResponse{
			Choices: []chatChoice{
				{Message: chatChoiceMessage{Role: "assistant", Content: "Go是一种编程语言。"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	svc := NewAPILLMService(server.URL, "test-key", "test-model", 0.3, 2048)
	answer, err := svc.Generate("sys prompt", []string{"Go is a language"}, "什么是Go？")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if answer != "Go是一种编程语言。" {
		t.Errorf("unexpected answer: %s", answer)
	}
}

func TestGenerate_RetryOnFirstFailure(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		if callCount == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			w.Write([]byte(`{"error":{"message":"server error"}}`))
			return
		}
		resp := chatResponse{
			Choices: []chatChoice{
				{Message: chatChoiceMessage{Role: "assistant", Content: "retry success"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	svc := NewAPILLMService(server.URL, "key", "model", 0.3, 2048)
	answer, err := svc.Generate("", []string{}, "q")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if answer != "retry success" {
		t.Errorf("expected retry success, got %s", answer)
	}
	if callCount != 2 {
		t.Errorf("expected 2 API calls, got %d", callCount)
	}
}

func TestGenerate_BothAttemptsFail_ReturnsFallback(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(`{"error":{"message":"server error"}}`))
	}))
	defer server.Close()

	svc := NewAPILLMService(server.URL, "key", "model", 0.3, 2048)
	answer, err := svc.Generate("", []string{}, "q")
	// Both attempts fail → returns fallback message AND error
	if err == nil {
		t.Fatal("expected error when both attempts fail")
	}
	if answer != "服务暂时不可用，请稍后重试" {
		t.Errorf("expected fallback message, got %s", answer)
	}
	if callCount != 2 {
		t.Errorf("expected 2 API calls (initial + retry), got %d", callCount)
	}
}

func TestGenerate_EmptyChoices(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := chatResponse{Choices: []chatChoice{}}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	svc := NewAPILLMService(server.URL, "key", "model", 0.3, 2048)
	answer, err := svc.Generate("", []string{}, "q")
	if err == nil {
		t.Fatal("expected error when both attempts return empty choices")
	}
	if answer != "服务暂时不可用，请稍后重试" {
		t.Errorf("expected fallback message, got %s", answer)
	}
	if callCount != 2 {
		t.Errorf("expected 2 API calls, got %d", callCount)
	}
}

func TestGenerate_APIErrorInBody(t *testing.T) {
	callCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		callCount++
		resp := chatResponse{
			Error: &apiError{Message: "rate limited", Type: "rate_limit"},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	svc := NewAPILLMService(server.URL, "key", "model", 0.3, 2048)
	answer, err := svc.Generate("", []string{}, "q")
	if err == nil {
		t.Fatal("expected error when API returns error in body")
	}
	if answer != "服务暂时不可用，请稍后重试" {
		t.Errorf("expected fallback message, got %s", answer)
	}
	if callCount != 2 {
		t.Errorf("expected 2 API calls, got %d", callCount)
	}
}

func TestGenerate_NoAPIKey(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Error("expected no Authorization header when API key is empty")
		}
		resp := chatResponse{
			Choices: []chatChoice{
				{Message: chatChoiceMessage{Role: "assistant", Content: "ok"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	svc := NewAPILLMService(server.URL, "", "model", 0.3, 2048)
	answer, err := svc.Generate("", nil, "q")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if answer != "ok" {
		t.Errorf("expected ok, got %s", answer)
	}
}

func TestGenerate_EndpointTrailingSlash(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/chat/completions" {
			t.Errorf("expected /chat/completions, got %s", r.URL.Path)
		}
		resp := chatResponse{
			Choices: []chatChoice{
				{Message: chatChoiceMessage{Role: "assistant", Content: "ok"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	svc := NewAPILLMService(server.URL+"/", "key", "model", 0.3, 2048)
	answer, err := svc.Generate("", nil, "q")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if answer != "ok" {
		t.Errorf("expected ok, got %s", answer)
	}
}

func TestGenerate_PromptConstruction(t *testing.T) {
	var capturedReq chatRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&capturedReq)
		resp := chatResponse{
			Choices: []chatChoice{
				{Message: chatChoiceMessage{Role: "assistant", Content: "answer"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	svc := NewAPILLMService(server.URL, "key", "test-model", 0.7, 1024)
	svc.Generate("custom system prompt", []string{"chunk A", "chunk B"}, "my question")

	if capturedReq.Model != "test-model" {
		t.Errorf("expected model test-model, got %s", capturedReq.Model)
	}
	if capturedReq.Temperature != 0.7 {
		t.Errorf("expected temperature 0.7, got %f", capturedReq.Temperature)
	}
	if capturedReq.MaxTokens != 1024 {
		t.Errorf("expected max_tokens 1024, got %d", capturedReq.MaxTokens)
	}
	if len(capturedReq.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(capturedReq.Messages))
	}
	if capturedReq.Messages[0].Role != "system" {
		t.Errorf("expected system role, got %s", capturedReq.Messages[0].Role)
	}
	sysContent := contentStr(capturedReq.Messages[0].Content)
	if sysContent != "custom system prompt" {
		t.Errorf("expected custom system prompt, got %s", sysContent)
	}
	userContent := contentStr(capturedReq.Messages[1].Content)
	if !strings.Contains(userContent, "chunk A") {
		t.Error("user message should contain chunk A")
	}
	if !strings.Contains(userContent, "chunk B") {
		t.Error("user message should contain chunk B")
	}
	if !strings.Contains(userContent, "my question") {
		t.Error("user message should contain the question")
	}
}

func TestBuildMessagesWithImage(t *testing.T) {
	msgs := BuildMessagesWithImage("sys", []string{"ctx1"}, "what is this?", "data:image/png;base64,abc123")

	if len(msgs) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(msgs))
	}
	if msgs[0].Role != "system" {
		t.Errorf("expected system role, got %s", msgs[0].Role)
	}
	if msgs[1].Role != "user" {
		t.Errorf("expected user role, got %s", msgs[1].Role)
	}

	// User content should be an array of vision content parts
	parts, ok := msgs[1].Content.([]visionContentPart)
	if !ok {
		t.Fatalf("expected user content to be []visionContentPart, got %T", msgs[1].Content)
	}
	if len(parts) != 2 {
		t.Fatalf("expected 2 content parts, got %d", len(parts))
	}
	if parts[0].Type != "image_url" || parts[0].ImageURL == nil || parts[0].ImageURL.URL != "data:image/png;base64,abc123" {
		t.Error("first part should be image_url with the data URL")
	}
	if parts[1].Type != "text" || !strings.Contains(parts[1].Text, "what is this?") {
		t.Error("second part should be text containing the question")
	}
	if !strings.Contains(parts[1].Text, "ctx1") {
		t.Error("text part should contain context")
	}
}

func TestGenerateWithImage_FallsBackWhenEmpty(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := chatResponse{
			Choices: []chatChoice{
				{Message: chatChoiceMessage{Role: "assistant", Content: "text only"}},
			},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	svc := NewAPILLMService(server.URL, "key", "model", 0.3, 2048)
	answer, err := svc.GenerateWithImage("", nil, "q", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if answer != "text only" {
		t.Errorf("expected 'text only', got %s", answer)
	}
}
