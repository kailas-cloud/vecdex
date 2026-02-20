package vecdex

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

func TestNew_NoAddress(t *testing.T) {
	_, err := New(context.Background())
	if err == nil {
		t.Fatal("expected error when no address provided")
	}
}

func TestNew_UnknownDriver(t *testing.T) {
	cfg := &clientConfig{driver: "unknown", addrs: []string{"localhost:1234"}}
	_, err := createStore(cfg)
	if err == nil {
		t.Fatal("expected error for unknown driver")
	}
}

func TestNoopEmbedder(t *testing.T) {
	noop := &noopEmbedder{}
	_, err := noop.Embed(context.Background(), "test")
	if err == nil {
		t.Fatal("expected error from noopEmbedder")
	}
}

func TestEmbedderAdapter(t *testing.T) {
	called := false
	mock := &mockEmbedder{
		fn: func(_ context.Context, text string) (EmbeddingResult, error) {
			called = true
			return EmbeddingResult{
				Embedding:    []float32{1, 2, 3},
				PromptTokens: 5,
				TotalTokens:  10,
			}, nil
		},
	}

	adapter := &embedderAdapter{inner: mock}
	result, err := adapter.Embed(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("inner embedder was not called")
	}
	if len(result.Embedding) != 3 {
		t.Errorf("embedding len = %d, want 3", len(result.Embedding))
	}
	if result.TotalTokens != 10 {
		t.Errorf("total tokens = %d, want 10", result.TotalTokens)
	}
}

func TestEmbedderAdapter_Error(t *testing.T) {
	mock := &mockEmbedder{
		fn: func(_ context.Context, _ string) (EmbeddingResult, error) {
			return EmbeddingResult{}, errors.New("provider down")
		},
	}

	adapter := &embedderAdapter{inner: mock}
	_, err := adapter.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error from adapter")
	}
}

func TestClientOptions(t *testing.T) {
	cfg := &clientConfig{}

	WithValkey("localhost:6379", "secret").apply(cfg)
	if cfg.driver != "valkey" {
		t.Errorf("driver = %q, want valkey", cfg.driver)
	}
	if cfg.addrs[0] != "localhost:6379" {
		t.Errorf("addr = %q, want localhost:6379", cfg.addrs[0])
	}
	if cfg.password != "secret" {
		t.Errorf("password = %q, want secret", cfg.password)
	}

	cfg2 := &clientConfig{}
	WithRedis("localhost:6380", "pass").apply(cfg2)
	if cfg2.driver != "redis" {
		t.Errorf("driver = %q, want redis", cfg2.driver)
	}

	cfg3 := &clientConfig{}
	WithVectorDimensions(768).apply(cfg3)
	if cfg3.vectorDimensions != 768 {
		t.Errorf("vectorDimensions = %d, want 768", cfg3.vectorDimensions)
	}

	WithHNSW(16, 200).apply(cfg3)
	if cfg3.hnswM != 16 || cfg3.hnswEFConstruct != 200 {
		t.Errorf("hnsw = (%d, %d), want (16, 200)", cfg3.hnswM, cfg3.hnswEFConstruct)
	}

	WithMaxBatchSize(5000).apply(cfg3)
	if cfg3.maxBatchSize != 5000 {
		t.Errorf("maxBatchSize = %d, want 5000", cfg3.maxBatchSize)
	}

	cfg4 := &clientConfig{}
	logger := slog.Default()
	WithLogger(logger).apply(cfg4)
	if cfg4.logger != logger {
		t.Error("expected logger to be set")
	}

	cfg5 := &clientConfig{}
	reg := prometheus.NewRegistry()
	WithPrometheus(reg).apply(cfg5)
	if cfg5.metricsReg != reg {
		t.Error("expected metricsReg to be set")
	}
}

func TestClient_Close_NilStore(t *testing.T) {
	// Close на клиенте с nil store не паникует.
	c := &Client{store: nil}
	c.Close() // не должен упасть
}

func TestWithEmbedder(t *testing.T) {
	mock := &mockEmbedder{
		fn: func(_ context.Context, _ string) (EmbeddingResult, error) {
			return EmbeddingResult{}, nil
		},
	}
	cfg := &clientConfig{}
	WithEmbedder(mock).apply(cfg)
	if cfg.embedder == nil {
		t.Error("expected non-nil embedder")
	}
}

func TestObserver_NilSafe(t *testing.T) {
	// nil observer should not panic.
	var obs *observer
	obs.observe("test", time.Now(), nil)
	obs.observe("test", time.Now(), errors.New("err"))
}

func TestObserver_WithPrometheus(t *testing.T) {
	reg := prometheus.NewRegistry()
	obs, err := newObserver(nil, reg)
	if err != nil {
		t.Fatalf("newObserver: %v", err)
	}

	obs.observe("document.get", time.Now().Add(-10*time.Millisecond), nil)
	obs.observe("document.get", time.Now(), errors.New("fail"))

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("gather: %v", err)
	}
	if len(families) == 0 {
		t.Fatal("expected metrics to be registered")
	}

	// Verify operations counter has both ok and error.
	found := false
	for _, f := range families {
		if f.GetName() == "vecdex_sdk_operations_total" {
			found = true
			if len(f.GetMetric()) != 2 {
				t.Errorf("expected 2 metric samples, got %d",
					len(f.GetMetric()))
			}
		}
	}
	if !found {
		t.Error("vecdex_sdk_operations_total not found")
	}
}

func TestObserver_WithLogger(t *testing.T) {
	// Проверяем что логгер не паникует при вызове.
	logger := slog.Default()
	obs, err := newObserver(logger, nil)
	if err != nil {
		t.Fatalf("newObserver: %v", err)
	}
	obs.observe("test.op", time.Now(), nil)
	obs.observe("test.op", time.Now(), errors.New("test error"))
}

func TestObserver_NoMetricsNoLogger(t *testing.T) {
	obs, err := newObserver(nil, nil)
	if err != nil {
		t.Fatalf("newObserver: %v", err)
	}
	// Не должно паниковать.
	obs.observe("noop", time.Now(), nil)
}

type mockEmbedder struct {
	fn func(ctx context.Context, text string) (EmbeddingResult, error)
}

func (m *mockEmbedder) Embed(ctx context.Context, text string) (EmbeddingResult, error) {
	return m.fn(ctx, text)
}
