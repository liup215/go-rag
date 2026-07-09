package config

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"gopkg.in/yaml.v3"
)

// Config holds all configuration for go-rag
type Config struct {
	Embedding EmbeddingConfig `yaml:"embedding"`
	Chunking  ChunkingConfig  `yaml:"chunking"`
	Storage   StorageConfig   `yaml:"storage"`
	Reranker  RerankerConfig  `yaml:"reranker"`
}

// EmbeddingConfig holds embedding service configuration
type EmbeddingConfig struct {
	URL    string `yaml:"url"`
	APIKey string `yaml:"api_key"`
	Model  string `yaml:"model"`
}

// ChunkingConfig holds text chunking configuration
type ChunkingConfig struct {
	MaxTokens int `yaml:"max_tokens"`
	Overlap   int `yaml:"overlap"`
}

// StorageConfig holds storage configuration
type StorageConfig struct {
	Path string `yaml:"path"`
}

// RerankerConfig holds cross-encoder reranker configuration.
// When Enabled is true and URL is non-empty, the retriever applies a
// reranking pass after the initial hybrid search.
type RerankerConfig struct {
	Enabled bool   `yaml:"enabled"`
	URL     string `yaml:"url"`
	APIKey  string `yaml:"api_key"`
	Model   string `yaml:"model"`
}

// DefaultConfig returns a default configuration
func DefaultConfig() *Config {
	return &Config{
		Embedding: EmbeddingConfig{
			URL:   "https://api.openai.com/v1",
			Model: "text-embedding-3-small",
		},
		Chunking: ChunkingConfig{
			MaxTokens: 512,
			Overlap:   100,
		},
		Storage: StorageConfig{
			Path: defaultStoragePath(),
		},
	}
}

// defaultStoragePath returns the default storage path based on OS
func defaultStoragePath() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}

	switch runtime.GOOS {
	case "windows":
		return filepath.Join(homeDir, "AppData", "Local", "go-rag", "db.sqlite")
	case "darwin":
		return filepath.Join(homeDir, "Library", "Application Support", "go-rag", "db.sqlite")
	default: // linux and others
		return filepath.Join(homeDir, ".local", "share", "go-rag", "db.sqlite")
	}
}

// ConfigDir returns the configuration directory
func ConfigDir() string {
	homeDir, err := os.UserHomeDir()
	if err != nil {
		homeDir = "."
	}

	switch runtime.GOOS {
	case "windows":
		return filepath.Join(homeDir, "AppData", "Roaming", "go-rag")
	case "darwin":
		return filepath.Join(homeDir, "Library", "Application Support", "go-rag")
	default: // linux and others
		return filepath.Join(homeDir, ".config", "go-rag")
	}
}

// ConfigPath returns the full path to the config file
func ConfigPath() string {
	return filepath.Join(ConfigDir(), "config.yaml")
}

// Load loads configuration from file
func Load() (*Config, error) {
	configPath := ConfigPath()

	// If config file doesn't exist, return default
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		return DefaultConfig(), nil
	}

	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file: %w", err)
	}

	config := DefaultConfig()
	if err := yaml.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return config, nil
}

// Save saves configuration to file
func (c *Config) Save() error {
	configDir := ConfigDir()
	if err := os.MkdirAll(configDir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	configPath := ConfigPath()
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

// Init initializes a new configuration file
func Init() error {
	config := DefaultConfig()
	return config.Save()
}

// Set sets a configuration value by key
func (c *Config) Set(key, value string) error {
	switch key {
	case "embedding.url":
		c.Embedding.URL = value
	case "embedding.api-key":
		c.Embedding.APIKey = value
	case "embedding.model":
		c.Embedding.Model = value
	case "chunking.max-tokens":
		// Parse and validate
		c.Chunking.MaxTokens = parseInt(value, 512)
	case "chunking.overlap":
		c.Chunking.Overlap = parseInt(value, 100)
	case "storage.path":
		c.Storage.Path = value
	case "reranker.enabled":
		c.Reranker.Enabled = value == "true" || value == "1" || value == "yes"
	case "reranker.url":
		c.Reranker.URL = value
	case "reranker.api-key":
		c.Reranker.APIKey = value
	case "reranker.model":
		c.Reranker.Model = value
	default:
		return fmt.Errorf("unknown config key: %s", key)
	}
	return c.Save()
}

// Get gets a configuration value by key
func (c *Config) Get(key string) (string, error) {
	switch key {
	case "embedding.url":
		return c.Embedding.URL, nil
	case "embedding.api-key":
		return c.Embedding.APIKey, nil
	case "embedding.model":
		return c.Embedding.Model, nil
	case "chunking.max-tokens":
		return fmt.Sprintf("%d", c.Chunking.MaxTokens), nil
	case "chunking.overlap":
		return fmt.Sprintf("%d", c.Chunking.Overlap), nil
	case "storage.path":
		return c.Storage.Path, nil
	case "reranker.enabled":
		if c.Reranker.Enabled {
			return "true", nil
		}
		return "false", nil
	case "reranker.url":
		return c.Reranker.URL, nil
	case "reranker.api-key":
		return c.Reranker.APIKey, nil
	case "reranker.model":
		return c.Reranker.Model, nil
	default:
		return "", fmt.Errorf("unknown config key: %s", key)
	}
}

func parseInt(s string, defaultVal int) int {
	var val int
	if _, err := fmt.Sscanf(s, "%d", &val); err != nil || val <= 0 {
		return defaultVal
	}
	return val
}

// maskSecret replaces a potentially sensitive string with a masked
// representation, matching the format used by the CLI display helpers.
func maskSecret(s string) string {
	if s == "" {
		return "(not set)"
	}
	if len(s) <= 8 {
		return "****"
	}
	return s[:4] + "..." + s[len(s)-4:]
}

// GetDisplay returns the display-safe value for the given key.  For keys that
// hold API keys or other secrets the value is masked so it does not appear in
// plain text in terminal output.  All keys are handled directly without
// delegating to Get, so that static analysis can confirm no sensitive field
// ever reaches an unmasked output path.
func (c *Config) GetDisplay(key string) (string, error) {
	switch key {
	case "embedding.url":
		return c.Embedding.URL, nil
	case "embedding.api-key":
		return maskSecret(c.Embedding.APIKey), nil
	case "embedding.model":
		return c.Embedding.Model, nil
	case "chunking.max-tokens":
		return fmt.Sprintf("%d", c.Chunking.MaxTokens), nil
	case "chunking.overlap":
		return fmt.Sprintf("%d", c.Chunking.Overlap), nil
	case "storage.path":
		return c.Storage.Path, nil
	case "reranker.enabled":
		if c.Reranker.Enabled {
			return "true", nil
		}
		return "false", nil
	case "reranker.url":
		return c.Reranker.URL, nil
	case "reranker.api-key":
		return maskSecret(c.Reranker.APIKey), nil
	case "reranker.model":
		return c.Reranker.Model, nil
	default:
		return "", fmt.Errorf("unknown config key: %s", key)
	}
}

// ConfigItem represents a configuration item with its description
type ConfigItem struct {
	Key         string
	Description string
	Default     string
	Required    bool
	Category    string
}

// GetConfigItems returns all available configuration items
func GetConfigItems() []ConfigItem {
	return []ConfigItem{
		{
			Key:         "embedding.url",
			Description: "Embedding API base URL (e.g., https://api.openai.com/v1, http://localhost:11434)",
			Default:     "https://api.openai.com/v1",
			Required:    false,
			Category:    "embedding",
		},
		{
			Key:         "embedding.api-key",
			Description: "API key for authentication with the embedding service",
			Default:     "(none)",
			Required:    true,
			Category:    "embedding",
		},
		{
			Key:         "embedding.model",
			Description: "Embedding model name (e.g., text-embedding-3-small, nomic-embed-text)",
			Default:     "text-embedding-3-small",
			Required:    false,
			Category:    "embedding",
		},
		{
			Key:         "chunking.max-tokens",
			Description: "Target chunk size in tokens (larger = more context, smaller = more precise)",
			Default:     "512",
			Required:    false,
			Category:    "chunking",
		},
		{
			Key:         "chunking.overlap",
			Description: "Overlap between chunks in tokens (helps maintain continuity)",
			Default:     "100",
			Required:    false,
			Category:    "chunking",
		},
		{
			Key:         "storage.path",
			Description: "Path to the data storage directory",
			Default:     "(platform-specific)",
			Required:    false,
			Category:    "storage",
		},
		{
			Key:         "reranker.enabled",
			Description: "Enable cross-encoder reranking after initial retrieval (true/false)",
			Default:     "false",
			Required:    false,
			Category:    "reranker",
		},
		{
			Key:         "reranker.url",
			Description: "Reranker API endpoint URL (e.g., http://localhost:8080/rerank)",
			Default:     "(none)",
			Required:    false,
			Category:    "reranker",
		},
		{
			Key:         "reranker.api-key",
			Description: "API key for authentication with the reranker service",
			Default:     "(none)",
			Required:    false,
			Category:    "reranker",
		},
		{
			Key:         "reranker.model",
			Description: "Reranker model name (e.g., bge-reranker-v2-m3, jina-reranker-v2-base-multilingual)",
			Default:     "(none)",
			Required:    false,
			Category:    "reranker",
		},
	}
}

// GetConfigItem returns a specific configuration item by key
func GetConfigItem(key string) *ConfigItem {
	items := GetConfigItems()
	for _, item := range items {
		if item.Key == key {
			return &item
		}
	}
	return nil
}

// GetConfigKeys returns all available configuration keys
func GetConfigKeys() []string {
	items := GetConfigItems()
	keys := make([]string, len(items))
	for i, item := range items {
		keys[i] = item.Key
	}
	return keys
}
