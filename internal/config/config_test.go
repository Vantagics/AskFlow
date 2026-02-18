package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"pgregory.net/rapid"
)

func testKey() []byte {
	return []byte("01234567890123456789012345678901") // 32 bytes
}

func tempConfigPath(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "config.json")
}

func newTestManager(t *testing.T) (*ConfigManager, string) {
	t.Helper()
	path := tempConfigPath(t)
	cm, err := NewConfigManagerWithKey(path, testKey())
	if err != nil {
		t.Fatalf("NewConfigManagerWithKey: %v", err)
	}
	return cm, path
}

func TestNewConfigManagerWithKey_InvalidKeyLength(t *testing.T) {
	_, err := NewConfigManagerWithKey("test.json", []byte("short"))
	if err == nil {
		t.Fatal("expected error for short key")
	}
}

func TestLoad_CreatesDefaultOnMissing(t *testing.T) {
	cm, path := newTestManager(t)
	if err := cm.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// File should be created
	if _, err := os.Stat(path); os.IsNotExist(err) {
		t.Fatal("config file was not created")
	}

	cfg := cm.Get()
	if cfg == nil {
		t.Fatal("Get returned nil")
	}

	// Verify defaults
	if cfg.Vector.ChunkSize != 512 {
		t.Errorf("ChunkSize = %d, want 512", cfg.Vector.ChunkSize)
	}
	if cfg.Vector.Overlap != 128 {
		t.Errorf("Overlap = %d, want 128", cfg.Vector.Overlap)
	}
	if cfg.Vector.TopK != 5 {
		t.Errorf("TopK = %d, want 5", cfg.Vector.TopK)
	}
	if cfg.Vector.Threshold != 0.5 {
		t.Errorf("Threshold = %f, want 0.5", cfg.Vector.Threshold)
	}
	if cfg.LLM.Temperature != 0.3 {
		t.Errorf("Temperature = %f, want 0.3", cfg.LLM.Temperature)
	}
	if cfg.LLM.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %d, want 2048", cfg.LLM.MaxTokens)
	}
	if cfg.Vector.DBPath != "./data/askflow.db" {
		t.Errorf("DBPath = %q, want ./data/askflow.db", cfg.Vector.DBPath)
	}
}

func TestSaveAndLoad_RoundTrip(t *testing.T) {
	cm, path := newTestManager(t)
	if err := cm.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Set some values
	cm.config.LLM.APIKey = "sk-test-secret-key-12345"
	cm.config.LLM.Endpoint = "https://api.example.com/v1"
	cm.config.Embedding.APIKey = "emb-secret-key-67890"
	cm.config.OAuth.Providers["google"] = OAuthProviderConfig{
		ClientID:     "google-client-id",
		ClientSecret: "google-secret",
		AuthURL:      "https://accounts.google.com/o/oauth2/auth",
		TokenURL:     "https://oauth2.googleapis.com/token",
		RedirectURL:  "http://localhost:8080/callback",
		Scopes:       []string{"openid", "email"},
	}

	if err := cm.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Load into a new manager
	cm2, err := NewConfigManagerWithKey(path, testKey())
	if err != nil {
		t.Fatalf("NewConfigManagerWithKey: %v", err)
	}
	if err := cm2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	cfg := cm2.Get()
	if cfg.LLM.APIKey != "sk-test-secret-key-12345" {
		t.Errorf("LLM.APIKey = %q, want sk-test-secret-key-12345", cfg.LLM.APIKey)
	}
	if cfg.LLM.Endpoint != "https://api.example.com/v1" {
		t.Errorf("LLM.Endpoint = %q", cfg.LLM.Endpoint)
	}
	if cfg.Embedding.APIKey != "emb-secret-key-67890" {
		t.Errorf("Embedding.APIKey = %q", cfg.Embedding.APIKey)
	}
	if cfg.OAuth.Providers["google"].ClientSecret != "google-secret" {
		t.Errorf("OAuth google ClientSecret = %q", cfg.OAuth.Providers["google"].ClientSecret)
	}
	if len(cfg.OAuth.Providers["google"].Scopes) != 2 {
		t.Errorf("OAuth google Scopes len = %d", len(cfg.OAuth.Providers["google"].Scopes))
	}
}

func TestSave_APIKeysEncryptedOnDisk(t *testing.T) {
	cm, path := newTestManager(t)
	if err := cm.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	cm.config.LLM.APIKey = "my-secret-llm-key"
	cm.config.Embedding.APIKey = "my-secret-emb-key"
	cm.config.OAuth.Providers["google"] = OAuthProviderConfig{
		ClientSecret: "my-google-secret",
	}

	if err := cm.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	// Read raw file and verify plaintext keys are NOT present
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	raw := string(data)

	if strings.Contains(raw, "my-secret-llm-key") {
		t.Error("LLM API key found in plaintext on disk")
	}
	if strings.Contains(raw, "my-secret-emb-key") {
		t.Error("Embedding API key found in plaintext on disk")
	}
	if strings.Contains(raw, "my-google-secret") {
		t.Error("OAuth client secret found in plaintext on disk")
	}

	// Verify encrypted prefix is present
	if !strings.Contains(raw, encryptedPrefix) {
		t.Error("encrypted prefix not found in file")
	}
}

func TestUpdate_AppliesAndPersists(t *testing.T) {
	cm, path := newTestManager(t)
	if err := cm.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	updates := map[string]interface{}{
		"llm.endpoint":      "https://new-api.example.com",
		"llm.api_key":       "new-key",
		"llm.model_name":    "gpt-4o",
		"llm.temperature":   0.7,
		"llm.max_tokens":    4096,
		"vector.chunk_size":  1024,
		"vector.top_k":       10,
		"admin.password_hash": "bcrypt-hash-here",
	}
	if err := cm.Update(updates); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Verify in-memory
	cfg := cm.Get()
	if cfg.LLM.Endpoint != "https://new-api.example.com" {
		t.Errorf("LLM.Endpoint = %q", cfg.LLM.Endpoint)
	}
	if cfg.LLM.ModelName != "gpt-4o" {
		t.Errorf("LLM.ModelName = %q", cfg.LLM.ModelName)
	}
	if cfg.LLM.Temperature != 0.7 {
		t.Errorf("Temperature = %f", cfg.LLM.Temperature)
	}
	if cfg.LLM.MaxTokens != 4096 {
		t.Errorf("MaxTokens = %d", cfg.LLM.MaxTokens)
	}
	if cfg.Vector.ChunkSize != 1024 {
		t.Errorf("ChunkSize = %d", cfg.Vector.ChunkSize)
	}
	if cfg.Vector.TopK != 10 {
		t.Errorf("TopK = %d", cfg.Vector.TopK)
	}

	// Verify persisted
	cm2, err := NewConfigManagerWithKey(path, testKey())
	if err != nil {
		t.Fatalf("NewConfigManagerWithKey: %v", err)
	}
	if err := cm2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg2 := cm2.Get()
	if cfg2.LLM.Endpoint != "https://new-api.example.com" {
		t.Errorf("persisted LLM.Endpoint = %q", cfg2.LLM.Endpoint)
	}
	if cfg2.LLM.APIKey != "new-key" {
		t.Errorf("persisted LLM.APIKey = %q", cfg2.LLM.APIKey)
	}
}

func TestUpdate_UnknownKey(t *testing.T) {
	cm, _ := newTestManager(t)
	if err := cm.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	err := cm.Update(map[string]interface{}{"unknown.key": "value"})
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
}

func TestGet_ReturnsCopy(t *testing.T) {
	cm, _ := newTestManager(t)
	if err := cm.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	cfg1 := cm.Get()
	cfg1.LLM.Endpoint = "modified"

	cfg2 := cm.Get()
	if cfg2.LLM.Endpoint == "modified" {
		t.Error("Get did not return a copy — mutation leaked")
	}
}

func TestLoad_PlaintextAPIKey(t *testing.T) {
	// Simulate a manually edited config with plaintext API key
	path := tempConfigPath(t)
	raw := map[string]interface{}{
		"llm": map[string]interface{}{
			"api_key": "plaintext-key",
		},
	}
	data, _ := json.Marshal(raw)
	os.WriteFile(path, data, 0600)

	cm, err := NewConfigManagerWithKey(path, testKey())
	if err != nil {
		t.Fatalf("NewConfigManagerWithKey: %v", err)
	}
	if err := cm.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	cfg := cm.Get()
	if cfg.LLM.APIKey != "plaintext-key" {
		t.Errorf("APIKey = %q, want plaintext-key", cfg.LLM.APIKey)
	}
}

func TestEncryptDecrypt_EmptyString(t *testing.T) {
	cm, _ := newTestManager(t)
	encrypted := cm.encryptIfNeeded("")
	if encrypted != "" {
		t.Errorf("encryptIfNeeded empty = %q, want empty", encrypted)
	}
	decrypted, err := cm.decryptIfNeeded("")
	if err != nil {
		t.Fatalf("decryptIfNeeded: %v", err)
	}
	if decrypted != "" {
		t.Errorf("decryptIfNeeded empty = %q, want empty", decrypted)
	}
}

func TestVideoConfig_DefaultValues(t *testing.T) {
	cm, _ := newTestManager(t)
	if err := cm.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	cfg := cm.Get()
	if cfg.Video.KeyframeInterval != 10 {
		t.Errorf("Video.KeyframeInterval = %d, want 10", cfg.Video.KeyframeInterval)
	}
	if cfg.Video.FFmpegPath != "" {
		t.Errorf("Video.FFmpegPath = %q, want empty", cfg.Video.FFmpegPath)
	}
	if cfg.Video.RapidSpeechPath != "" {
		t.Errorf("Video.RapidSpeechPath = %q, want empty", cfg.Video.RapidSpeechPath)
	}
}

func TestVideoConfig_UpdateAndPersist(t *testing.T) {
	cm, path := newTestManager(t)
	if err := cm.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	// Create real temp files for rapidspeech validation
	tmpDir := t.TempDir()
	rapidspeechBin := filepath.Join(tmpDir, "rapidspeech")
	if err := os.WriteFile(rapidspeechBin, []byte("#!/bin/sh\n"), 0755); err != nil {
		t.Fatalf("create rapidspeech bin: %v", err)
	}
	modelFile := filepath.Join(tmpDir, "model.gguf")
	if err := os.WriteFile(modelFile, []byte("fake-model"), 0644); err != nil {
		t.Fatalf("create model file: %v", err)
	}

	updates := map[string]interface{}{
		"video.ffmpeg_path":        "/usr/bin/ffmpeg",
		"video.rapidspeech_path":   rapidspeechBin,
		"video.keyframe_interval":  5,
		"video.rapidspeech_model":  modelFile,
	}
	if err := cm.Update(updates); err != nil {
		t.Fatalf("Update: %v", err)
	}

	cfg := cm.Get()
	if cfg.Video.FFmpegPath != "/usr/bin/ffmpeg" {
		t.Errorf("Video.FFmpegPath = %q, want /usr/bin/ffmpeg", cfg.Video.FFmpegPath)
	}
	if cfg.Video.RapidSpeechPath != rapidspeechBin {
		t.Errorf("Video.RapidSpeechPath = %q, want %q", cfg.Video.RapidSpeechPath, rapidspeechBin)
	}
	if cfg.Video.KeyframeInterval != 5 {
		t.Errorf("Video.KeyframeInterval = %d, want 5", cfg.Video.KeyframeInterval)
	}
	if cfg.Video.RapidSpeechModel != modelFile {
		t.Errorf("Video.RapidSpeechModel = %q, want %q", cfg.Video.RapidSpeechModel, modelFile)
	}

	// Verify persisted
	cm2, err := NewConfigManagerWithKey(path, testKey())
	if err != nil {
		t.Fatalf("NewConfigManagerWithKey: %v", err)
	}
	if err := cm2.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}
	cfg2 := cm2.Get()
	if cfg2.Video.FFmpegPath != "/usr/bin/ffmpeg" {
		t.Errorf("persisted Video.FFmpegPath = %q", cfg2.Video.FFmpegPath)
	}
	if cfg2.Video.RapidSpeechPath != rapidspeechBin {
		t.Errorf("persisted Video.RapidSpeechPath = %q", cfg2.Video.RapidSpeechPath)
	}
	if cfg2.Video.KeyframeInterval != 5 {
		t.Errorf("persisted Video.KeyframeInterval = %d", cfg2.Video.KeyframeInterval)
	}
	if cfg2.Video.RapidSpeechModel != modelFile {
		t.Errorf("persisted Video.RapidSpeechModel = %q", cfg2.Video.RapidSpeechModel)
	}
}

func TestVideoConfig_ApplyDefaultsOnLoad(t *testing.T) {
	// Write a config file with video section but missing defaults
	path := tempConfigPath(t)
	raw := map[string]interface{}{
		"video": map[string]interface{}{
			"ffmpeg_path": "/usr/bin/ffmpeg",
		},
	}
	data, _ := json.Marshal(raw)
	os.WriteFile(path, data, 0600)

	cm, err := NewConfigManagerWithKey(path, testKey())
	if err != nil {
		t.Fatalf("NewConfigManagerWithKey: %v", err)
	}
	if err := cm.Load(); err != nil {
		t.Fatalf("Load: %v", err)
	}

	cfg := cm.Get()
	if cfg.Video.FFmpegPath != "/usr/bin/ffmpeg" {
		t.Errorf("Video.FFmpegPath = %q, want /usr/bin/ffmpeg", cfg.Video.FFmpegPath)
	}
	if cfg.Video.KeyframeInterval != 10 {
		t.Errorf("Video.KeyframeInterval = %d, want 10 (default)", cfg.Video.KeyframeInterval)
	}
}

// TestProperty3_VideoConfigPersistenceRoundTrip 验证视频配置持久化往返一致性。
// 对于任意有效的 VideoConfig 值，通过 ConfigManager 更新后再读取，应返回与写入值等价的配置。
//
// **Feature: video-retrieval, Property 3: 视频配置持久化往返一致性**
// **Validates: Requirements 1.3**
func TestProperty3_VideoConfigPersistenceRoundTrip(t *testing.T) {
	rapid.Check(t, func(rt *rapid.T) {
		ffmpegPath := rapid.StringMatching(`[a-zA-Z0-9/._-]{0,50}`).Draw(rt, "ffmpeg_path")
		keyframeInterval := rapid.IntRange(1, 120).Draw(rt, "keyframe_interval")

		// Create real temp files for rapidspeech validation
		tmpDir := t.TempDir()
		rapidspeechBin := filepath.Join(tmpDir, "rapidspeech")
		if err := os.WriteFile(rapidspeechBin, []byte("#!/bin/sh\n"), 0755); err != nil {
			rt.Fatalf("create rapidspeech bin: %v", err)
		}
		modelFile := filepath.Join(tmpDir, "model.gguf")
		if err := os.WriteFile(modelFile, []byte("fake-model"), 0644); err != nil {
			rt.Fatalf("create model file: %v", err)
		}

		// Randomly choose between empty (skip validation) or real file paths
		useRapidspeech := rapid.Bool().Draw(rt, "use_rapidspeech")
		var rapidspeechPath, rapidspeechModel string
		if useRapidspeech {
			rapidspeechPath = rapidspeechBin
			rapidspeechModel = modelFile
		}

		path := filepath.Join(t.TempDir(), fmt.Sprintf("config-%d.json", time.Now().UnixNano()))
		cm, err := NewConfigManagerWithKey(path, testKey())
		if err != nil {
			rt.Fatalf("NewConfigManagerWithKey: %v", err)
		}
		if err := cm.Load(); err != nil {
			rt.Fatalf("Load: %v", err)
		}

		updates := map[string]interface{}{
			"video.ffmpeg_path":        ffmpegPath,
			"video.rapidspeech_path":   rapidspeechPath,
			"video.keyframe_interval":  keyframeInterval,
			"video.rapidspeech_model":  rapidspeechModel,
		}
		if err := cm.Update(updates); err != nil {
			rt.Fatalf("Update: %v", err)
		}

		// 重新加载配置
		cm2, err := NewConfigManagerWithKey(path, testKey())
		if err != nil {
			rt.Fatalf("NewConfigManagerWithKey: %v", err)
		}
		if err := cm2.Load(); err != nil {
			rt.Fatalf("Load: %v", err)
		}

		cfg := cm2.Get()
		if cfg.Video.FFmpegPath != ffmpegPath {
			rt.Errorf("FFmpegPath: got %q, want %q", cfg.Video.FFmpegPath, ffmpegPath)
		}
		if cfg.Video.RapidSpeechPath != rapidspeechPath {
			rt.Errorf("RapidSpeechPath: got %q, want %q", cfg.Video.RapidSpeechPath, rapidspeechPath)
		}
		if cfg.Video.KeyframeInterval != keyframeInterval {
			rt.Errorf("KeyframeInterval: got %d, want %d", cfg.Video.KeyframeInterval, keyframeInterval)
		}
		if cfg.Video.RapidSpeechModel != rapidspeechModel {
			rt.Errorf("RapidSpeechModel: got %q, want %q", cfg.Video.RapidSpeechModel, rapidspeechModel)
		}
	})
}
