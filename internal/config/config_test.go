package config

import "testing"

func TestValidate_InvalidBudgetAction(t *testing.T) {
	cfg := Config{
		HTTP: HTTPConfig{Port: 8080},
		Database: DatabaseConfig{
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

func TestValidate_ValidBudgetActions(t *testing.T) {
	validActions := []string{"", "warn", "reject"}

	for _, action := range validActions {
		t.Run("action="+action, func(t *testing.T) {
			cfg := Config{
				HTTP: HTTPConfig{Port: 8080},
				Database: DatabaseConfig{
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
		Database: DatabaseConfig{
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
		Database: DatabaseConfig{
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
	if cfg.Database.ReadinessTimeout != 10 {
		t.Errorf("expected ReadinessTimeout=10, got %d", cfg.Database.ReadinessTimeout)
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
	if cfg.Storage.KeyPrefix != "vecdex:" {
		t.Errorf("expected KeyPrefix='vecdex:', got %q", cfg.Storage.KeyPrefix)
	}
}

func TestApplyDefaults_NoOverride(t *testing.T) {
	cfg := Config{
		HTTP:     HTTPConfig{ReadTimeoutSec: 30, WriteTimeoutSec: 60, ShutdownSec: 5},
		Database: DatabaseConfig{ReadinessTimeout: 15},
		Index:    IndexConfig{HNSWM: 16, HNSWEFConstruct: 200, DefaultPageSize: 50, MaxPageSize: 500, MaxBatchSize: 50},
		Storage:  StorageConfig{KeyPrefix: "custom:"},
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
	if cfg.Storage.KeyPrefix != "custom:" {
		t.Errorf("expected KeyPrefix='custom:', got %q", cfg.Storage.KeyPrefix)
	}
}
