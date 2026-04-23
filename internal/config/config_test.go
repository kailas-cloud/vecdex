package config

import "testing"

func TestValidate_InvalidBudgetAction(t *testing.T) {
	cfg := Config{
		HTTP: HTTPConfig{Port: 8080},
		Valkey: ValkeyConfig{
			Addrs: []string{"localhost:6379"},
		},
		Embedding: EmbeddingConfig{
			Providers: map[string]ProviderConfig{
				"nebius": {
					APIKey:  "test-key",
					BaseURL: "https://api.example.com/v1/",
					Budget: BudgetConfig{
						DailyTokenLimit: 1000000,
						Action:          "invalid_action",
					},
				},
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid budget action")
	}

	expected := `embedding.providers.nebius.budget.action must be "warn" or "reject", got "invalid_action"`
	if err.Error() != expected {
		t.Errorf("unexpected error message:\ngot:  %q\nwant: %q", err.Error(), expected)
	}
}

func TestValidate_InvalidEmbeddingBackend(t *testing.T) {
	cfg := Config{
		HTTP: HTTPConfig{Port: 8080},
		Valkey: ValkeyConfig{
			Addrs: []string{"localhost:6379"},
		},
		Embedding: EmbeddingConfig{
			Providers: map[string]ProviderConfig{
				"local": {
					Backend: "invalid",
				},
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid backend")
	}

	expected := `embedding.providers.local.backend must be "openai" or "onnx", got "invalid"`
	if err.Error() != expected {
		t.Fatalf("unexpected error:\ngot:  %q\nwant: %q", err.Error(), expected)
	}
}

func TestValidate_ONNXRequiresModelDir(t *testing.T) {
	cfg := Config{
		HTTP: HTTPConfig{Port: 8080},
		Valkey: ValkeyConfig{
			Addrs: []string{"localhost:6379"},
		},
		Embedding: EmbeddingConfig{
			Providers: map[string]ProviderConfig{
				"local": {
					Backend:   "onnx",
					MaxLength: 256,
				},
			},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for missing model_dir")
	}
}

func TestApplyDefaults_ONNXProvider(t *testing.T) {
	cfg := Config{
		Embedding: EmbeddingConfig{
			Providers: map[string]ProviderConfig{
				"local": {
					Backend: "onnx",
				},
				"remote": {},
			},
		},
	}

	cfg.ApplyDefaults()

	local := cfg.Embedding.Providers["local"]
	if local.MaxLength != 256 {
		t.Fatalf("expected onnx max_length=256, got %d", local.MaxLength)
	}
	if local.ExecutionProvider != "cpu" {
		t.Fatalf("expected onnx execution_provider=cpu, got %q", local.ExecutionProvider)
	}

	remote := cfg.Embedding.Providers["remote"]
	if remote.Backend != "openai" {
		t.Fatalf("expected default backend=openai, got %q", remote.Backend)
	}
}

func TestValidate_ValidBudgetActions(t *testing.T) {
	validActions := []string{"", "warn", "reject"}

	for _, action := range validActions {
		t.Run("action="+action, func(t *testing.T) {
			cfg := Config{
				HTTP: HTTPConfig{Port: 8080},
				Valkey: ValkeyConfig{
					Addrs: []string{"localhost:6379"},
				},
				Embedding: EmbeddingConfig{
					Providers: map[string]ProviderConfig{
						"nebius": {
							APIKey: "test-key",
							Budget: BudgetConfig{
								Action: action,
							},
						},
					},
				},
			}

			err := cfg.Validate()
			if err != nil {
				t.Fatalf("unexpected error for valid action %q: %v", action, err)
			}
		})
	}
}

func TestValidate_InvalidPort(t *testing.T) {
	cfg := Config{
		HTTP: HTTPConfig{Port: 0},
		Valkey: ValkeyConfig{
			Addrs: []string{"localhost:6379"},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for invalid port")
	}
}

func TestValidate_MissingValkeyAddrs(t *testing.T) {
	cfg := Config{
		HTTP: HTTPConfig{Port: 8080},
		Valkey: ValkeyConfig{
			Addrs: []string{},
		},
	}

	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected error for missing valkey addrs")
	}
}

func TestApplyDefaults(t *testing.T) {
	cfg := Config{}
	cfg.ApplyDefaults()

	if cfg.HTTP.ReadTimeoutSec != 10 {
		t.Errorf("expected ReadTimeoutSec=10, got %d", cfg.HTTP.ReadTimeoutSec)
	}
	if cfg.HTTP.WriteTimeoutSec != 10 {
		t.Errorf("expected WriteTimeoutSec=10, got %d", cfg.HTTP.WriteTimeoutSec)
	}
	if cfg.HTTP.ShutdownSec != 10 {
		t.Errorf("expected ShutdownSec=10, got %d", cfg.HTTP.ShutdownSec)
	}
	if cfg.Valkey.ReadinessTimeout != 10 {
		t.Errorf("expected ReadinessTimeout=10, got %d", cfg.Valkey.ReadinessTimeout)
	}
	if cfg.Index.HNSWM != 32 {
		t.Errorf("expected HNSWM=32, got %d", cfg.Index.HNSWM)
	}
	if cfg.Index.HNSWEFConstruct != 400 {
		t.Errorf("expected HNSWEFConstruct=400, got %d", cfg.Index.HNSWEFConstruct)
	}
	if cfg.Index.DefaultPageSize != 20 {
		t.Errorf("expected DefaultPageSize=20, got %d", cfg.Index.DefaultPageSize)
	}
	if cfg.Index.MaxPageSize != 100 {
		t.Errorf("expected MaxPageSize=100, got %d", cfg.Index.MaxPageSize)
	}
	if cfg.Index.MaxBatchSize != 100 {
		t.Errorf("expected MaxBatchSize=100, got %d", cfg.Index.MaxBatchSize)
	}
	if cfg.Search.SemanticCandidateFloor != 100 {
		t.Errorf("expected SemanticCandidateFloor=100, got %d", cfg.Search.SemanticCandidateFloor)
	}
	if cfg.Search.SemanticCandidateMultiplier != 2.0 {
		t.Errorf("expected SemanticCandidateMultiplier=2.0, got %v", cfg.Search.SemanticCandidateMultiplier)
	}
	if cfg.Search.BM25CandidateFloor != 100 {
		t.Errorf("expected BM25CandidateFloor=100, got %d", cfg.Search.BM25CandidateFloor)
	}
	if cfg.Search.BM25CandidateMultiplier != 2.0 {
		t.Errorf("expected BM25CandidateMultiplier=2.0, got %v", cfg.Search.BM25CandidateMultiplier)
	}
	if cfg.Storage.KeyPrefix != "vecdex:" {
		t.Errorf("expected KeyPrefix='vecdex:', got %q", cfg.Storage.KeyPrefix)
	}
}

func TestApplyDefaults_NoOverride(t *testing.T) {
	cfg := Config{
		HTTP:   HTTPConfig{ReadTimeoutSec: 30, WriteTimeoutSec: 60, ShutdownSec: 5},
		Valkey: ValkeyConfig{ReadinessTimeout: 15},
		Index:  IndexConfig{HNSWM: 16, HNSWEFConstruct: 200, DefaultPageSize: 50, MaxPageSize: 500, MaxBatchSize: 50},
		Search: SearchConfig{
			SemanticCandidateFloor:      60,
			SemanticCandidateMultiplier: 3,
			BM25CandidateFloor:          70,
			BM25CandidateMultiplier:     4,
		},
		Storage: StorageConfig{KeyPrefix: "custom:"},
	}
	cfg.ApplyDefaults()

	if cfg.HTTP.ReadTimeoutSec != 30 {
		t.Errorf("expected ReadTimeoutSec=30, got %d", cfg.HTTP.ReadTimeoutSec)
	}
	if cfg.HTTP.WriteTimeoutSec != 60 {
		t.Errorf("expected WriteTimeoutSec=60, got %d", cfg.HTTP.WriteTimeoutSec)
	}
	if cfg.Index.HNSWM != 16 {
		t.Errorf("expected HNSWM=16, got %d", cfg.Index.HNSWM)
	}
	if cfg.Search.SemanticCandidateFloor != 60 {
		t.Errorf("expected SemanticCandidateFloor=60, got %d", cfg.Search.SemanticCandidateFloor)
	}
	if cfg.Search.SemanticCandidateMultiplier != 3 {
		t.Errorf("expected SemanticCandidateMultiplier=3, got %v", cfg.Search.SemanticCandidateMultiplier)
	}
	if cfg.Search.BM25CandidateFloor != 70 {
		t.Errorf("expected BM25CandidateFloor=70, got %d", cfg.Search.BM25CandidateFloor)
	}
	if cfg.Search.BM25CandidateMultiplier != 4 {
		t.Errorf("expected BM25CandidateMultiplier=4, got %v", cfg.Search.BM25CandidateMultiplier)
	}
	if cfg.Storage.KeyPrefix != "custom:" {
		t.Errorf("expected KeyPrefix='custom:', got %q", cfg.Storage.KeyPrefix)
	}
}

func TestValidate_InvalidSearchConfig(t *testing.T) {
	t.Run("semantic floor", func(t *testing.T) {
		cfg := Config{
			HTTP:   HTTPConfig{Port: 8080},
			Valkey: ValkeyConfig{Addrs: []string{"localhost:6379"}},
			Search: SearchConfig{
				SemanticCandidateFloor:      -1,
				SemanticCandidateMultiplier: 2,
				BM25CandidateFloor:          100,
				BM25CandidateMultiplier:     2,
			},
		}
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected error for invalid semantic candidate floor")
		}
	})

	t.Run("bm25 multiplier", func(t *testing.T) {
		cfg := Config{
			HTTP:   HTTPConfig{Port: 8080},
			Valkey: ValkeyConfig{Addrs: []string{"localhost:6379"}},
			Search: SearchConfig{
				SemanticCandidateFloor:      100,
				SemanticCandidateMultiplier: 2,
				BM25CandidateFloor:          100,
				BM25CandidateMultiplier:     0.5,
			},
		}
		if err := cfg.Validate(); err == nil {
			t.Fatal("expected error for invalid bm25 candidate multiplier")
		}
	})
}
