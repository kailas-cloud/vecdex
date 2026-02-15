package config

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"

	"gopkg.in/yaml.v3"
)

// Config holds the vecdex API configuration.
type Config struct {
	HTTP      HTTPConfig      `yaml:"http"`
	Database  DatabaseConfig  `yaml:"database"`
	Embedding EmbeddingConfig `yaml:"embedding"`
	Auth      AuthConfig      `yaml:"auth"`
	Index     IndexConfig     `yaml:"index"`
	Storage   StorageConfig   `yaml:"storage"`
	Logging   LoggingConfig   `yaml:"logging"`
}

// LoggingConfig holds logging settings.
type LoggingConfig struct {
	Level string `yaml:"level"` // debug, info, warn, error (default: determined by env)
}

// AuthConfig holds API authentication settings.
type AuthConfig struct {
	APIKeys []string `yaml:"api_keys"`
}

// HTTPConfig holds HTTP server settings.
type HTTPConfig struct {
	Port            int `yaml:"port"`
	ReadTimeoutSec  int `yaml:"read_timeout_sec"`
	WriteTimeoutSec int `yaml:"write_timeout_sec"`
	ShutdownSec     int `yaml:"shutdown_timeout_sec"`
}

// DatabaseConfig holds database connection settings.
type DatabaseConfig struct {
	Driver           string   `yaml:"driver"` // valkey, redis (default: valkey)
	Addrs            []string `yaml:"addrs"`
	Password         string   `yaml:"password"`
	ReadinessTimeout int      `yaml:"readiness_timeout_sec"`
}

// IndexConfig holds HNSW index and pagination settings.
type IndexConfig struct {
	HNSWM           int `yaml:"hnsw_m"`
	HNSWEFConstruct int `yaml:"hnsw_ef_construction"`
	DefaultPageSize int `yaml:"default_page_size"`
	MaxPageSize     int `yaml:"max_page_size"`
	MaxBatchSize    int `yaml:"max_batch_size"`
}

// StorageConfig holds storage settings.
type StorageConfig struct {
	KeyPrefix string `yaml:"key_prefix"`
}

// EmbeddingConfig holds embedding settings.
type EmbeddingConfig struct {
	Providers   map[string]ProviderConfig   `yaml:"providers"`
	Vectorizers map[string]VectorizerConfig `yaml:"vectorizers"`
}

// BudgetConfig holds token budget settings.
type BudgetConfig struct {
	DailyTokenLimit      int64   `yaml:"daily_token_limit"`       // 0 = unlimited
	MonthlyTokenLimit    int64   `yaml:"monthly_token_limit"`     // 0 = unlimited
	CostPerMillionTokens float64 `yaml:"cost_per_million_tokens"` // для дашборда
	Action               string  `yaml:"action"`                  // "reject" | "warn" (default)
}

// ProviderConfig holds embedding provider settings.
type ProviderConfig struct {
	APIKey  string       `yaml:"api_key"`
	BaseURL string       `yaml:"base_url"`
	Budget  BudgetConfig `yaml:"budget"`
}

// VectorizerConfig holds vectorizer settings.
type VectorizerConfig struct {
	Provider            string `yaml:"provider"`
	Model               string `yaml:"model"`
	Dimensions          int    `yaml:"dimensions"`
	DocumentInstruction string `yaml:"document_instruction"`
	QueryInstruction    string `yaml:"query_instruction"`
}

// Load reads configuration from a YAML file by environment name (local, dev, prod).
func Load(env string) (Config, error) {
	configPath := findConfigPath(env)

	data, err := os.ReadFile(filepath.Clean(configPath))
	if err != nil {
		return Config{}, fmt.Errorf("failed to read config %s: %w", configPath, err)
	}

	// Substitute env variables of the form ${VAR}
	data = expandEnvVars(data)

	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("failed to parse config: %w", err)
	}

	cfg.ApplyDefaults()

	if err := cfg.Validate(); err != nil {
		return Config{}, fmt.Errorf("invalid config: %w", err)
	}

	return cfg, nil
}

// MustLoad loads configuration or panics.
func MustLoad(env string) Config {
	cfg, err := Load(env)
	if err != nil {
		panic(err)
	}
	return cfg
}

// GetEnv returns the current environment from the ENV variable, defaulting to "local".
func GetEnv() string {
	if env := os.Getenv("ENV"); env != "" {
		return env
	}
	return "local"
}

// ApplyDefaults fills empty fields with default values.
func (c *Config) ApplyDefaults() {
	if c.HTTP.ReadTimeoutSec <= 0 {
		c.HTTP.ReadTimeoutSec = 10
	}
	if c.HTTP.WriteTimeoutSec <= 0 {
		c.HTTP.WriteTimeoutSec = 10
	}
	if c.HTTP.ShutdownSec <= 0 {
		c.HTTP.ShutdownSec = 10
	}
	if c.Database.Driver == "" {
		c.Database.Driver = "valkey"
	}
	if c.Database.ReadinessTimeout <= 0 {
		c.Database.ReadinessTimeout = 10
	}
	if c.Index.HNSWM <= 0 {
		c.Index.HNSWM = 32
	}
	if c.Index.HNSWEFConstruct <= 0 {
		c.Index.HNSWEFConstruct = 400
	}
	if c.Index.DefaultPageSize <= 0 {
		c.Index.DefaultPageSize = 20
	}
	if c.Index.MaxPageSize <= 0 {
		c.Index.MaxPageSize = 100
	}
	if c.Index.MaxBatchSize <= 0 {
		c.Index.MaxBatchSize = 100
	}
	if c.Storage.KeyPrefix == "" {
		c.Storage.KeyPrefix = "vecdex:"
	}
}

// Validate checks the configuration for correctness.
func (c *Config) Validate() error {
	if c.HTTP.Port <= 0 || c.HTTP.Port > 65535 {
		return fmt.Errorf("http.port must be between 1 and 65535, got %d", c.HTTP.Port)
	}
	if len(c.Database.Addrs) == 0 {
		return fmt.Errorf("database.addrs is required")
	}
	for name, p := range c.Embedding.Providers {
		switch p.Budget.Action {
		case "", "warn", "reject":
			// ok
		default:
			return fmt.Errorf(
				"embedding.providers.%s.budget.action must be \"warn\" or \"reject\", got %q",
				name, p.Budget.Action,
			)
		}
	}
	return nil
}

// findConfigPath locates the config file.
func findConfigPath(env string) string {
	filename := fmt.Sprintf("%s.yaml", env)

	// 1. Check ./config/
	if path := filepath.Join("config", filename); fileExists(path) {
		return path
	}

	// 2. Check relative to the source file
	_, b, _, _ := runtime.Caller(0)
	projectRoot := filepath.Dir(filepath.Dir(filepath.Dir(b))) // internal/config -> project root
	if path := filepath.Join(projectRoot, "config", filename); fileExists(path) {
		return path
	}

	// 3. Fallback to ./config/
	return filepath.Join("config", filename)
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// expandEnvVars replaces ${VAR} and ${VAR:-default} with environment variable values.
var envVarRegex = regexp.MustCompile(`\$\{([^}]+)\}`)

func expandEnvVars(data []byte) []byte {
	return envVarRegex.ReplaceAllFunc(data, func(match []byte) []byte {
		expr := string(match[2 : len(match)-1]) // strip ${ and }
		varName, defaultVal, hasDefault := strings.Cut(expr, ":-")
		val := os.Getenv(varName)
		if val == "" && hasDefault {
			val = defaultVal
		}
		return []byte(val)
	})
}
