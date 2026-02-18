package vecdex

import (
	"context"
	"errors"
	"testing"
)

func TestNew_NoAddress(t *testing.T) {
	_, err := New()
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

	WithValkey("localhost:6379", "secret")(cfg)
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
	WithRedis("localhost:6380", "pass")(cfg2)
	if cfg2.driver != "redis" {
		t.Errorf("driver = %q, want redis", cfg2.driver)
	}

	cfg3 := &clientConfig{}
	WithVectorDimensions(768)(cfg3)
	if cfg3.vectorDimensions != 768 {
		t.Errorf("vectorDimensions = %d, want 768", cfg3.vectorDimensions)
	}

	WithHNSW(16, 200)(cfg3)
	if cfg3.hnswM != 16 || cfg3.hnswEFConstruct != 200 {
		t.Errorf("hnsw = (%d, %d), want (16, 200)", cfg3.hnswM, cfg3.hnswEFConstruct)
	}

	WithMaxBatchSize(5000)(cfg3)
	if cfg3.maxBatchSize != 5000 {
		t.Errorf("maxBatchSize = %d, want 5000", cfg3.maxBatchSize)
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
	WithEmbedder(mock)(cfg)
	if cfg.embedder == nil {
		t.Error("expected non-nil embedder")
	}
}

type mockEmbedder struct {
	fn func(ctx context.Context, text string) (EmbeddingResult, error)
}

func (m *mockEmbedder) Embed(ctx context.Context, text string) (EmbeddingResult, error) {
	return m.fn(ctx, text)
}
