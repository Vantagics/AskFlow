// Package config provides configuration management with encrypted API key storage.
// It supports loading, saving, and hot-reloading of system configuration.
package config

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

// encryptionKeyEnvVar is the environment variable name for the AES encryption key.
const encryptionKeyEnvVar = "ASKFLOW_ENCRYPTION_KEY"

// encryptedPrefix marks a value as AES-encrypted in the config file.
const encryptedPrefix = "enc:"

// Config holds all system configuration.
type Config struct {
	Server       ServerConfig    `json:"server"`
	LLM          LLMConfig       `json:"llm"`
	Embedding    EmbeddingConfig `json:"embedding"`
	Vector       VectorConfig    `json:"vector"`
	OAuth        OAuthConfig     `json:"oauth"`
	Admin        AdminConfig     `json:"admin"`
	SMTP         SMTPConfig      `json:"smtp"`
	ProductIntro string          `json:"product_intro"`
	ProductName  string          `json:"product_name"`
	Video        VideoConfig     `json:"video"`
	AuthServer   string          `json:"auth_server"` // license verification server host, e.g. "license.vantagedata.chat"
}


// ServerConfig holds HTTP server configuration.
type ServerConfig struct {
	Bind    string `json:"bind"`    // bind address (e.g., "0.0.0.0", "::", "127.0.0.1")
	Port    int    `json:"port"`
	SSLCert string `json:"ssl_cert"` // path to SSL certificate file (PEM)
	SSLKey  string `json:"ssl_key"`  // path to SSL private key file (PEM)
}


// LLMConfig holds LLM service configuration.
type LLMConfig struct {
	Endpoint    string  `json:"endpoint"`
	APIKey      string  `json:"api_key"`
	ModelName   string  `json:"model_name"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"max_tokens"`
}

// EmbeddingConfig holds embedding service configuration.
type EmbeddingConfig struct {
	Endpoint      string `json:"endpoint"`
	APIKey        string `json:"api_key"`
	ModelName     string `json:"model_name"`
	UseMultimodal bool   `json:"use_multimodal"`
}

// VectorConfig holds vector store configuration.
type VectorConfig struct {
	DBPath          string  `json:"db_path"`
	ChunkSize       int     `json:"chunk_size"`
	Overlap         int     `json:"overlap"`
	TopK            int     `json:"top_k"`
	Threshold       float64 `json:"threshold"`
	ContentPriority string  `json:"content_priority"` // "image_text" (default) or "text_only"
	DebugMode       bool    `json:"debug_mode"`       // when true, query responses include search diagnostics
	TextMatchEnabled bool   `json:"text_match_enabled"` // enable 3-level text similarity processing to save API costs
}

// SMTPConfig holds SMTP email server configuration.
type SMTPConfig struct {
	Host       string `json:"host"`
	Port       int    `json:"port"`
	Username   string `json:"username"`
	Password   string `json:"password"`
	FromAddr   string `json:"from_addr"`
	FromName   string `json:"from_name"`
	UseTLS     bool   `json:"use_tls"`
	AuthMethod string `json:"auth_method"` // "PLAIN" (default), "LOGIN", or "NONE"
}

// OAuthProviderConfig holds configuration for a single OAuth provider.
type OAuthProviderConfig struct {
	ClientID     string   `json:"client_id"`
	ClientSecret string   `json:"client_secret"`
	AuthURL      string   `json:"auth_url"`
	TokenURL     string   `json:"token_url"`
	RedirectURL  string   `json:"redirect_url"`
	Scopes       []string `json:"scopes"`
}

// OAuthConfig holds OAuth configuration for all providers.
type OAuthConfig struct {
	Providers map[string]OAuthProviderConfig `json:"providers"`
}

// VideoConfig holds video processing configuration.
type VideoConfig struct {
	FFmpegPath          string `json:"ffmpeg_path"`            // ffmpeg executable path, empty means video not supported
	RapidSpeechPath     string `json:"rapidspeech_path"`       // rs-asr-offline executable path, empty means skip transcription
	KeyframeInterval    int    `json:"keyframe_interval"`      // keyframe sampling interval in seconds, default 10
	RapidSpeechModel    string `json:"rapidspeech_model"`      // RapidSpeech model path (model.gguf file)
	MaxUploadSizeMB     int    `json:"max_upload_size_mb"`     // max video/document upload size in MB, default 500
	KeyframeOCREnabled  bool   `json:"keyframe_ocr_enabled"`   // enable LLM-based OCR on keyframes for text search
	KeyframeOCRMaxFrames int   `json:"keyframe_ocr_max_frames"` // max keyframes to OCR (0=unlimited), default 20
}

// AdminConfig holds admin authentication configuration.
type AdminConfig struct {
	Username   string `json:"username"`
	PasswordHash string `json:"password_hash"`
	LoginRoute string `json:"login_route"`
}

// ConfigManager manages loading, saving, and updating configuration.
type ConfigManager struct {
	configPath    string
	config        *Config
	mu            sync.RWMutex
	encryptionKey []byte // 32-byte AES-256 key
}

// NewConfigManager creates a new ConfigManager for the given config file path.
// The AES encryption key is read from the ASKFLOW_ENCRYPTION_KEY environment variable.
// If the env var is not set, a random 32-byte key is generated and set in the environment.
func NewConfigManager(configPath string) (*ConfigManager, error) {
	key, err := getOrCreateEncryptionKey()
	if err != nil {
		return nil, fmt.Errorf("encryption key error: %w", err)
	}
	return &ConfigManager{
		configPath:    configPath,
		encryptionKey: key,
	}, nil
}

// NewConfigManagerWithKey creates a ConfigManager with an explicit encryption key (for testing).
func NewConfigManagerWithKey(configPath string, key []byte) (*ConfigManager, error) {
	if len(key) != 32 {
		return nil, errors.New("encryption key must be 32 bytes for AES-256")
	}
	return &ConfigManager{
		configPath:    configPath,
		encryptionKey: key,
	}, nil
}


// DefaultConfig returns a Config populated with default values.
// API keys are intentionally left empty — the admin must configure them after installation.
func DefaultConfig() *Config {
	return &Config{
		Server: ServerConfig{
			Bind: "0.0.0.0",
			Port: 8080,
		},
		LLM: LLMConfig{
			Endpoint:    "",
			APIKey:      "",
			ModelName:   "",
			Temperature: 0.3,
			MaxTokens:   2048,
		},
		Embedding: EmbeddingConfig{
			Endpoint:      "",
			APIKey:        "",
			ModelName:     "",
			UseMultimodal: true,
		},
		Vector: VectorConfig{
			DBPath:           "askflow.db",
			ChunkSize:        512,
			Overlap:          128,
			TopK:             5,
			Threshold:        0.5,
			ContentPriority:  "image_text",
			TextMatchEnabled: true,
		},
		OAuth: OAuthConfig{
			Providers: make(map[string]OAuthProviderConfig),
		},
		Admin: AdminConfig{
			Username:     "",
			PasswordHash: "",
			LoginRoute:   "/admin",
		},
		SMTP: SMTPConfig{
			Host:   "",
			Port:   587,
			UseTLS: true,
		},
		Video: VideoConfig{
			KeyframeInterval:    10,
			MaxUploadSizeMB:     500,
			KeyframeOCREnabled:  true,
			KeyframeOCRMaxFrames: 20,
		},
	}
}

// Load reads the config file from disk and decrypts API keys.
// If the file does not exist, it initializes with default values and saves.
func (cm *ConfigManager) Load() error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	data, err := os.ReadFile(cm.configPath)
	if err != nil {
		if os.IsNotExist(err) {
			cm.config = DefaultConfig()
			return cm.saveLocked()
		}
		return fmt.Errorf("read config file: %w", err)
	}

	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return fmt.Errorf("parse config file: %w", err)
	}

	// Decrypt API keys
	if cfg.LLM.APIKey, err = cm.decryptIfNeeded(cfg.LLM.APIKey); err != nil {
		return fmt.Errorf("decrypt LLM API key: %w", err)
	}
	if cfg.Embedding.APIKey, err = cm.decryptIfNeeded(cfg.Embedding.APIKey); err != nil {
		return fmt.Errorf("decrypt Embedding API key: %w", err)
	}
	for name, provider := range cfg.OAuth.Providers {
		if provider.ClientSecret, err = cm.decryptIfNeeded(provider.ClientSecret); err != nil {
			return fmt.Errorf("decrypt OAuth %s client secret: %w", name, err)
		}
		cfg.OAuth.Providers[name] = provider
	}
	if cfg.SMTP.Password, err = cm.decryptIfNeeded(cfg.SMTP.Password); err != nil {
		return fmt.Errorf("decrypt SMTP password: %w", err)
	}

	cm.applyDefaults(&cfg)
	cm.config = &cfg
	return nil
}

// Save writes the current config to disk with API keys encrypted.
func (cm *ConfigManager) Save() error {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	return cm.saveLocked()
}

// saveLocked writes config to disk. Caller must hold at least a read lock.
func (cm *ConfigManager) saveLocked() error {
	if cm.config == nil {
		return errors.New("no config loaded")
	}

	// Create a copy for serialization with encrypted keys
	out := *cm.config
	out.LLM.APIKey = cm.encryptIfNeeded(cm.config.LLM.APIKey)
	out.Embedding.APIKey = cm.encryptIfNeeded(cm.config.Embedding.APIKey)

	if cm.config.OAuth.Providers != nil {
		out.OAuth.Providers = make(map[string]OAuthProviderConfig, len(cm.config.OAuth.Providers))
		for name, provider := range cm.config.OAuth.Providers {
			p := provider
			p.ClientSecret = cm.encryptIfNeeded(provider.ClientSecret)
			out.OAuth.Providers[name] = p
		}
	}

	out.SMTP.Password = cm.encryptIfNeeded(cm.config.SMTP.Password)

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	if err := os.WriteFile(cm.configPath, data, 0600); err != nil {
		return fmt.Errorf("write config file: %w", err)
	}
	return nil
}

// Get returns a copy of the current configuration.
func (cm *ConfigManager) Get() *Config {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.config == nil {
		return nil
	}
	c := *cm.config
	// Deep copy OAuth providers map
	if cm.config.OAuth.Providers != nil {
		c.OAuth.Providers = make(map[string]OAuthProviderConfig, len(cm.config.OAuth.Providers))
		for k, v := range cm.config.OAuth.Providers {
			p := v
			if v.Scopes != nil {
				p.Scopes = make([]string, len(v.Scopes))
				copy(p.Scopes, v.Scopes)
			}
			c.OAuth.Providers[k] = p
		}
	}
	return &c
}

// IsReady returns true if both LLM and Embedding API keys are configured (non-empty).
func (cm *ConfigManager) IsReady() bool {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.config == nil {
		return false
	}
	return strings.TrimSpace(cm.config.LLM.APIKey) != "" &&
		strings.TrimSpace(cm.config.Embedding.APIKey) != ""
}

// Update applies partial updates to the configuration and saves to disk.
// Supported keys: "llm.endpoint", "llm.api_key", "llm.model_name", "llm.temperature",
// "llm.max_tokens", "embedding.endpoint", "embedding.api_key", "embedding.model_name",
// "vector.db_path", "vector.chunk_size", "vector.overlap", "vector.top_k", "vector.threshold",
// "admin.password_hash".
func (cm *ConfigManager) Update(updates map[string]interface{}) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.config == nil {
		cm.config = DefaultConfig()
	}

	// Limit number of keys per update to prevent abuse
	if len(updates) > 100 {
		return fmt.Errorf("too many config updates (max 100 keys per request)")
	}

	for key, val := range updates {
		if err := cm.applyUpdate(key, val); err != nil {
			return fmt.Errorf("update key %q: %w", key, err)
		}
	}

	return cm.saveLocked()
}

func (cm *ConfigManager) applyUpdate(key string, val interface{}) error {
	switch key {
	// LLM fields
	case "llm.endpoint":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		cm.config.LLM.Endpoint = s
	case "llm.api_key":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		cm.config.LLM.APIKey = s
	case "llm.model_name":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		cm.config.LLM.ModelName = s
	case "llm.temperature":
		f, err := toFloat64(val)
		if err != nil {
			return err
		}
		if f < 0 || f > 2.0 {
			return errors.New("temperature must be between 0 and 2.0")
		}
		cm.config.LLM.Temperature = f
	case "llm.max_tokens":
		n, err := toInt(val)
		if err != nil {
			return err
		}
		if n < 1 || n > 128000 {
			return errors.New("max_tokens must be between 1 and 128000")
		}
		cm.config.LLM.MaxTokens = n

	// Embedding fields
	case "embedding.endpoint":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		cm.config.Embedding.Endpoint = s
	case "embedding.api_key":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		cm.config.Embedding.APIKey = s
	case "embedding.model_name":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		cm.config.Embedding.ModelName = s
	case "embedding.use_multimodal":
		b, ok := val.(bool)
		if !ok {
			return errors.New("expected boolean")
		}
		cm.config.Embedding.UseMultimodal = b

	// Vector fields
	case "vector.db_path":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		// Prevent path traversal
		if strings.Contains(s, "..") {
			return errors.New("db_path must not contain '..'")
		}
		cm.config.Vector.DBPath = s
	case "vector.chunk_size":
		n, err := toInt(val)
		if err != nil {
			return err
		}
		if n < 64 || n > 8192 {
			return errors.New("chunk_size must be between 64 and 8192")
		}
		cm.config.Vector.ChunkSize = n
	case "vector.overlap":
		n, err := toInt(val)
		if err != nil {
			return err
		}
		if n < 0 || n > 4096 {
			return errors.New("overlap must be between 0 and 4096")
		}
		cm.config.Vector.Overlap = n
	case "vector.top_k":
		n, err := toInt(val)
		if err != nil {
			return err
		}
		if n < 1 || n > 100 {
			return errors.New("top_k must be between 1 and 100")
		}
		cm.config.Vector.TopK = n
	case "vector.threshold":
		f, err := toFloat64(val)
		if err != nil {
			return err
		}
		if f < 0 || f > 1.0 {
			return errors.New("threshold must be between 0 and 1.0")
		}
		cm.config.Vector.Threshold = f
	case "vector.content_priority":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		if s != "image_text" && s != "text_only" {
			return errors.New("content_priority must be 'image_text' or 'text_only'")
		}
		cm.config.Vector.ContentPriority = s
	case "vector.debug_mode":
		b, ok := val.(bool)
		if !ok {
			return errors.New("expected boolean")
		}
		cm.config.Vector.DebugMode = b
	case "vector.text_match_enabled":
		b, ok := val.(bool)
		if !ok {
			return errors.New("expected boolean")
		}
		cm.config.Vector.TextMatchEnabled = b

	// Admin fields
	case "admin.username":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		cm.config.Admin.Username = s
	case "admin.login_route":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		if s == "" || s[0] != '/' {
			return errors.New("login_route must start with /")
		}
		cm.config.Admin.LoginRoute = s
	case "admin.password_hash":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		cm.config.Admin.PasswordHash = s
	case "admin.password":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		hash, err := bcrypt.GenerateFromPassword([]byte(s), bcrypt.DefaultCost)
		if err != nil {
			return fmt.Errorf("hash password: %w", err)
		}
		cm.config.Admin.PasswordHash = string(hash)

	// SMTP fields
	case "smtp.host":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		cm.config.SMTP.Host = s
	case "smtp.port":
		n, err := toInt(val)
		if err != nil {
			return err
		}
		if n < 1 || n > 65535 {
			return errors.New("SMTP port must be between 1 and 65535")
		}
		cm.config.SMTP.Port = n
	case "smtp.username":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		cm.config.SMTP.Username = s
	case "smtp.password":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		cm.config.SMTP.Password = s
	case "smtp.from_addr":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		cm.config.SMTP.FromAddr = s
	case "smtp.from_name":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		cm.config.SMTP.FromName = s
	case "smtp.use_tls":
		b, ok := val.(bool)
		if !ok {
			return errors.New("expected boolean")
		}
		cm.config.SMTP.UseTLS = b
	case "smtp.auth_method":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		cm.config.SMTP.AuthMethod = s

	case "product_intro":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		if len(s) > 10000 {
			return errors.New("product_intro too long (max 10000 characters)")
		}
		cm.config.ProductIntro = s

	case "product_name":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		if len(s) > 200 {
			return errors.New("product_name too long (max 200 characters)")
		}
		cm.config.ProductName = s

	case "auth_server":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		if len(s) > 200 {
			return errors.New("auth_server too long (max 200 characters)")
		}
		cm.config.AuthServer = s

	// Video fields
	case "video.ffmpeg_path":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		// Validate path doesn't contain shell metacharacters
		if strings.ContainsAny(s, "|;&$`") {
			return errors.New("ffmpeg path contains invalid characters")
		}
		cm.config.Video.FFmpegPath = s
	case "video.rapidspeech_path":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		// Validate path doesn't contain shell metacharacters
		if strings.ContainsAny(s, "|;&$`") {
			return errors.New("rapidspeech path contains invalid characters")
		}
		// Allow clearing the path
		if s != "" {
			// Check file exists
			info, err := os.Stat(s)
			if err != nil {
				return fmt.Errorf("rapidspeech 可执行文件不存在: %s", s)
			}
			if info.IsDir() {
				return fmt.Errorf("rapidspeech 路径指向目录而非文件: %s", s)
			}
			// Try to set execute permission and verify
			if err := ensureExecutable(s); err != nil {
				return fmt.Errorf("rapidspeech 可执行文件权限设置失败: %w", err)
			}
		}
		cm.config.Video.RapidSpeechPath = s
	case "video.keyframe_interval":
		n, err := toInt(val)
		if err != nil {
			return err
		}
		if n < 1 || n > 300 {
			return errors.New("keyframe_interval must be between 1 and 300 seconds")
		}
		cm.config.Video.KeyframeInterval = n
	case "video.rapidspeech_model":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		// Validate path doesn't contain shell metacharacters
		if strings.ContainsAny(s, "|;&$`") {
			return errors.New("rapidspeech model path contains invalid characters")
		}
		// Allow clearing the path
		if s != "" {
			// Check model file exists
			info, err := os.Stat(s)
			if err != nil {
				return fmt.Errorf("rapidspeech 模型文件不存在: %s", s)
			}
			if info.IsDir() {
				return fmt.Errorf("rapidspeech 模型路径指向目录而非文件: %s", s)
			}
			// Verify it looks like a valid model file
			if !strings.HasSuffix(strings.ToLower(s), ".gguf") && !strings.HasSuffix(strings.ToLower(s), ".bin") {
				return fmt.Errorf("rapidspeech 模型文件应为 .gguf 或 .bin 格式")
			}
		}
		cm.config.Video.RapidSpeechModel = s
	case "video.max_upload_size_mb":
		n, err := toInt(val)
		if err != nil {
			return err
		}
		if n < 1 {
			return errors.New("max_upload_size_mb must be at least 1")
		}
		cm.config.Video.MaxUploadSizeMB = n
	case "video.keyframe_ocr_enabled":
		b, ok := val.(bool)
		if !ok {
			return errors.New("expected boolean")
		}
		cm.config.Video.KeyframeOCREnabled = b
	case "video.keyframe_ocr_max_frames":
		n, err := toInt(val)
		if err != nil {
			return err
		}
		if n < 0 || n > 200 {
			return errors.New("keyframe_ocr_max_frames must be between 0 and 200")
		}
		cm.config.Video.KeyframeOCRMaxFrames = n

	// Server fields
	case "server.bind":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		cm.config.Server.Bind = s
	case "server.port":
		n, err := toInt(val)
		if err != nil {
			return err
		}
		if n < 1 || n > 65535 {
			return errors.New("port must be between 1 and 65535")
		}
		cm.config.Server.Port = n
	case "server.ssl_cert":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		if strings.Contains(s, "..") {
			return errors.New("ssl_cert path must not contain '..'")
		}
		cm.config.Server.SSLCert = s
	case "server.ssl_key":
		s, ok := val.(string)
		if !ok {
			return errors.New("expected string")
		}
		if strings.Contains(s, "..") {
			return errors.New("ssl_key path must not contain '..'")
		}
		cm.config.Server.SSLKey = s

	default:
		// Handle OAuth provider config: oauth.providers.<name>.<field>
		if strings.HasPrefix(key, "oauth.providers.") {
			return cm.applyOAuthUpdate(key, val)
		}
		return fmt.Errorf("unknown config key: %s", key)
	}
	return nil
}

// applyOAuthUpdate handles OAuth provider config keys like "oauth.providers.google.client_id".
func (cm *ConfigManager) applyOAuthUpdate(key string, val interface{}) error {
	parts := strings.SplitN(key, ".", 4)
	if len(parts) != 4 {
		return fmt.Errorf("invalid OAuth config key: %s", key)
	}
	providerName := parts[2]
	field := parts[3]

	if cm.config.OAuth.Providers == nil {
		cm.config.OAuth.Providers = make(map[string]OAuthProviderConfig)
	}
	p := cm.config.OAuth.Providers[providerName]

	s, ok := val.(string)
	if !ok {
		if field == "scopes" {
			if arr, ok := val.([]interface{}); ok {
				scopes := make([]string, 0, len(arr))
				for _, v := range arr {
					if sv, ok := v.(string); ok {
						scopes = append(scopes, sv)
					}
				}
				p.Scopes = scopes
				cm.config.OAuth.Providers[providerName] = p
				return nil
			}
		}
		return errors.New("expected string")
	}

	switch field {
	case "client_id":
		p.ClientID = s
	case "client_secret":
		p.ClientSecret = s
	case "auth_url":
		// Validate URL format
		if s != "" && !strings.HasPrefix(s, "https://") {
			return errors.New("auth_url must use HTTPS")
		}
		p.AuthURL = s
	case "token_url":
		if s != "" && !strings.HasPrefix(s, "https://") {
			return errors.New("token_url must use HTTPS")
		}
		p.TokenURL = s
	case "redirect_url":
		p.RedirectURL = s
	case "scopes":
		p.Scopes = strings.Split(s, ",")
	default:
		return fmt.Errorf("unknown OAuth provider field: %s", field)
	}

	cm.config.OAuth.Providers[providerName] = p
	return nil
}

// DeleteOAuthProvider removes an OAuth provider from the config and saves.
func (cm *ConfigManager) DeleteOAuthProvider(provider string) error {
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.config == nil || cm.config.OAuth.Providers == nil {
		return nil
	}
	delete(cm.config.OAuth.Providers, provider)
	return cm.saveLocked()
}

// applyDefaults fills in zero-value fields with defaults.
func (cm *ConfigManager) applyDefaults(cfg *Config) {
	defaults := DefaultConfig()
	if cfg.Server.Bind == "" {
		cfg.Server.Bind = defaults.Server.Bind
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = defaults.Server.Port
	}
	if cfg.LLM.Endpoint == "" {
		cfg.LLM.Endpoint = defaults.LLM.Endpoint
	}
	if cfg.LLM.ModelName == "" {
		cfg.LLM.ModelName = defaults.LLM.ModelName
	}
	// Temperature 0 is valid (deterministic output), only default if negative (impossible from JSON).
	// For fresh configs, DefaultConfig() already sets 0.3.
	if cfg.LLM.Temperature < 0 {
		cfg.LLM.Temperature = defaults.LLM.Temperature
	}
	if cfg.LLM.MaxTokens == 0 {
		cfg.LLM.MaxTokens = defaults.LLM.MaxTokens
	}
	if cfg.Embedding.Endpoint == "" {
		cfg.Embedding.Endpoint = defaults.Embedding.Endpoint
	}
	if cfg.Embedding.ModelName == "" {
		cfg.Embedding.ModelName = defaults.Embedding.ModelName
	}
	if cfg.Vector.DBPath == "" {
		cfg.Vector.DBPath = defaults.Vector.DBPath
	}
	if cfg.Vector.ChunkSize == 0 {
		cfg.Vector.ChunkSize = defaults.Vector.ChunkSize
	}
	if cfg.Vector.Overlap == 0 {
		cfg.Vector.Overlap = defaults.Vector.Overlap
	}
	if cfg.Vector.TopK == 0 {
		cfg.Vector.TopK = defaults.Vector.TopK
	}
	if cfg.Vector.Threshold == 0 {
		cfg.Vector.Threshold = defaults.Vector.Threshold
	}
	if cfg.Vector.ContentPriority == "" {
		cfg.Vector.ContentPriority = defaults.Vector.ContentPriority
	}
	if cfg.OAuth.Providers == nil {
		cfg.OAuth.Providers = make(map[string]OAuthProviderConfig)
	}
	if cfg.Admin.LoginRoute == "" {
		cfg.Admin.LoginRoute = defaults.Admin.LoginRoute
	}
	if cfg.SMTP.Port == 0 {
		cfg.SMTP.Port = defaults.SMTP.Port
	}
	if cfg.Video.KeyframeInterval == 0 {
		cfg.Video.KeyframeInterval = defaults.Video.KeyframeInterval
	}
	if cfg.Video.MaxUploadSizeMB == 0 {
		cfg.Video.MaxUploadSizeMB = defaults.Video.MaxUploadSizeMB
	}
}


// --- AES-GCM encryption helpers ---

// encrypt encrypts plaintext using AES-256-GCM.
func (cm *ConfigManager) encrypt(plaintext string) (string, error) {
	if plaintext == "" {
		return "", nil
	}
	block, err := aes.NewCipher(cm.encryptionKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ciphertext := gcm.Seal(nonce, nonce, []byte(plaintext), nil)
	return hex.EncodeToString(ciphertext), nil
}

// decrypt decrypts AES-256-GCM encrypted hex string.
func (cm *ConfigManager) decrypt(ciphertextHex string) (string, error) {
	if ciphertextHex == "" {
		return "", nil
	}
	ciphertext, err := hex.DecodeString(ciphertextHex)
	if err != nil {
		return "", fmt.Errorf("hex decode: %w", err)
	}
	block, err := aes.NewCipher(cm.encryptionKey)
	if err != nil {
		return "", err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonceSize := gcm.NonceSize()
	if len(ciphertext) < nonceSize {
		return "", errors.New("ciphertext too short")
	}
	nonce, ciphertext := ciphertext[:nonceSize], ciphertext[nonceSize:]
	plaintext, err := gcm.Open(nil, nonce, ciphertext, nil)
	if err != nil {
		return "", err
	}
	return string(plaintext), nil
}

// encryptIfNeeded encrypts a value and adds the "enc:" prefix.
// Empty strings are returned as-is.
func (cm *ConfigManager) encryptIfNeeded(value string) string {
	if value == "" {
		return ""
	}
	encrypted, err := cm.encrypt(value)
	if err != nil {
		// Fallback: return as-is (should not happen with valid key)
		return value
	}
	return encryptedPrefix + encrypted
}

// decryptIfNeeded decrypts a value if it has the "enc:" prefix.
func (cm *ConfigManager) decryptIfNeeded(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	if len(value) > len(encryptedPrefix) && value[:len(encryptedPrefix)] == encryptedPrefix {
		return cm.decrypt(value[len(encryptedPrefix):])
	}
	// Not encrypted (e.g., manually edited config) — return as-is
	return value, nil
}

// --- Encryption key management ---

func getOrCreateEncryptionKey() ([]byte, error) {
	// 1. Check environment variable first (preferred for production)
	keyHex := os.Getenv(encryptionKeyEnvVar)
	if keyHex != "" {
		key, err := hex.DecodeString(keyHex)
		if err != nil {
			return nil, fmt.Errorf("invalid encryption key hex: %w", err)
		}
		if len(key) != 32 {
			return nil, fmt.Errorf("encryption key must be 32 bytes, got %d", len(key))
		}
		return key, nil
	}

	// 2. Try to read from persistent key file
	keyFile := "./data/encryption.key"
	if data, err := os.ReadFile(keyFile); err == nil {
		keyHex = strings.TrimSpace(string(data))
		if key, err := hex.DecodeString(keyHex); err == nil && len(key) == 32 {
			// Ensure file permissions are restrictive
			os.Chmod(keyFile, 0600)
			return key, nil
		}
		// Key file exists but is invalid — log warning and regenerate
		fmt.Println("Warning: encryption.key file is invalid, regenerating")
	}

	// 3. Generate a new random key and persist it
	key := make([]byte, 32)
	if _, err := io.ReadFull(rand.Reader, key); err != nil {
		return nil, fmt.Errorf("generate encryption key: %w", err)
	}
	keyHex = hex.EncodeToString(key)
	os.MkdirAll("./data", 0700)
	if err := os.WriteFile(keyFile, []byte(keyHex+"\n"), 0600); err != nil {
		return nil, fmt.Errorf("save encryption key: %w", err)
	}
	return key, nil
}

// --- Type conversion helpers ---

// ensureExecutable checks that the given path is an executable file.
// On Unix systems, it also attempts to set the execute permission if missing.
func ensureExecutable(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("文件不存在: %w", err)
	}
	if info.IsDir() {
		return fmt.Errorf("路径指向目录而非文件: %s", path)
	}

	// On Windows, file permissions work differently — existence is sufficient.
	// On Unix, check and set execute permission.
	if runtime.GOOS != "windows" {
		mode := info.Mode()
		if mode&0111 == 0 {
			// No execute bits set, try to add owner execute
			newMode := mode | 0755
			if err := os.Chmod(path, newMode); err != nil {
				return fmt.Errorf("无法设置执行权限: %w", err)
			}
		}

		// Re-stat to verify
		info, err = os.Stat(path)
		if err != nil {
			return fmt.Errorf("权限设置后无法访问文件: %w", err)
		}
		if info.Mode()&0111 == 0 {
			return fmt.Errorf("文件没有执行权限: %s", path)
		}
	}
	return nil
}

func toFloat64(val interface{}) (float64, error) {
	switch v := val.(type) {
	case float64:
		return v, nil
	case float32:
		return float64(v), nil
	case int:
		return float64(v), nil
	case int64:
		return float64(v), nil
	case json.Number:
		return v.Float64()
	default:
		return 0, fmt.Errorf("expected numeric value, got %T", val)
	}
}

func toInt(val interface{}) (int, error) {
	switch v := val.(type) {
	case int:
		return v, nil
	case int64:
		return int(v), nil
	case float64:
		return int(v), nil
	case float32:
		return int(v), nil
	case json.Number:
		n, err := v.Int64()
		if err != nil {
			return 0, err
		}
		return int(n), nil
	default:
		return 0, fmt.Errorf("expected numeric value, got %T", val)
	}
}
