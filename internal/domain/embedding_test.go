package domain

import (
	"context"
	"errors"
	"testing"
)

type stubEmbedder struct {
	result EmbeddingResult
	err    error
	got    string
}

func (s *stubEmbedder) Embed(_ context.Context, text string) (EmbeddingResult, error) {
	s.got = text
	return s.result, s.err
}

func TestInstructionEmbedder_PrependsInstruction(t *testing.T) {
	inner := &stubEmbedder{result: EmbeddingResult{Embedding: []float32{0.1, 0.2, 0.3}}}
	emb := NewInstructionEmbedder(inner, "search_document: ")

	result, err := emb.Embed(context.Background(), "hello world")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.got != "search_document: hello world" {
		t.Errorf("expected prepended text, got %q", inner.got)
	}
	if len(result.Embedding) != 3 {
		t.Errorf("expected 3-element vector, got %d", len(result.Embedding))
	}
}

func TestInstructionEmbedder_ErrorPropagation(t *testing.T) {
	innerErr := errors.New("provider down")
	inner := &stubEmbedder{err: innerErr}
	emb := NewInstructionEmbedder(inner, "search_document: ")

	_, err := emb.Embed(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, innerErr) {
		t.Errorf("expected wrapped inner error, got %v", err)
	}
}

func TestInstructionEmbedder_EmptyInstruction(t *testing.T) {
	inner := &stubEmbedder{result: EmbeddingResult{Embedding: []float32{0.5}}}
	emb := NewInstructionEmbedder(inner, "")

	_, err := emb.Embed(context.Background(), "test")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if inner.got != "test" {
		t.Errorf("expected 'test', got %q", inner.got)
	}
}

// --- BatchEmbed tests ---

type stubBatchEmbedder struct {
	stubEmbedder
	batchResult BatchEmbeddingResult
	batchErr    error
	batchTexts  []string
}

func (s *stubBatchEmbedder) BatchEmbed(_ context.Context, texts []string) (BatchEmbeddingResult, error) {
	s.batchTexts = texts
	return s.batchResult, s.batchErr
}

func TestBatchFallback_Success(t *testing.T) {
	inner := &stubEmbedder{result: EmbeddingResult{
		Embedding:    []float32{0.1, 0.2},
		PromptTokens: 5,
		TotalTokens:  5,
	}}
	res, err := BatchFallback(context.Background(), inner, []string{"a", "b", "c"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Embeddings) != 3 {
		t.Fatalf("expected 3 embeddings, got %d", len(res.Embeddings))
	}
	if res.TotalTokens != 15 {
		t.Errorf("expected TotalTokens=15, got %d", res.TotalTokens)
	}
	if res.PromptTokens != 15 {
		t.Errorf("expected PromptTokens=15, got %d", res.PromptTokens)
	}
}

func TestBatchFallback_Error(t *testing.T) {
	innerErr := errors.New("fail")
	inner := &stubEmbedder{err: innerErr}
	_, err := BatchFallback(context.Background(), inner, []string{"a"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, innerErr) {
		t.Errorf("expected wrapped inner error, got %v", err)
	}
}

func TestBatchFallback_Empty(t *testing.T) {
	inner := &stubEmbedder{}
	res, err := BatchFallback(context.Background(), inner, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Embeddings) != 0 {
		t.Errorf("expected 0 embeddings, got %d", len(res.Embeddings))
	}
}

func TestInstructionEmbedder_BatchEmbed_WithBatchInner(t *testing.T) {
	inner := &stubBatchEmbedder{
		batchResult: BatchEmbeddingResult{
			Embeddings:   [][]float32{{0.1}, {0.2}},
			PromptTokens: 20,
			TotalTokens:  20,
		},
	}
	emb := NewInstructionEmbedder(inner, "search: ")

	res, err := emb.BatchEmbed(context.Background(), []string{"hello", "world"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Embeddings) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(res.Embeddings))
	}
	// Instruction должен быть добавлен к каждому тексту
	if inner.batchTexts[0] != "search: hello" || inner.batchTexts[1] != "search: world" {
		t.Errorf("expected prefixed texts, got %v", inner.batchTexts)
	}
}

func TestInstructionEmbedder_BatchEmbed_FallbackToSingle(t *testing.T) {
	// inner не реализует BatchEmbedder — fallback на поштучный Embed
	inner := &stubEmbedder{result: EmbeddingResult{
		Embedding:    []float32{0.5},
		PromptTokens: 3,
		TotalTokens:  3,
	}}
	emb := NewInstructionEmbedder(inner, "q: ")

	res, err := emb.BatchEmbed(context.Background(), []string{"a", "b"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(res.Embeddings) != 2 {
		t.Fatalf("expected 2 embeddings, got %d", len(res.Embeddings))
	}
	if res.TotalTokens != 6 {
		t.Errorf("expected TotalTokens=6, got %d", res.TotalTokens)
	}
}

func TestInstructionEmbedder_BatchEmbed_Error(t *testing.T) {
	innerErr := errors.New("batch fail")
	inner := &stubBatchEmbedder{batchErr: innerErr}
	emb := NewInstructionEmbedder(inner, "x: ")

	_, err := emb.BatchEmbed(context.Background(), []string{"a"})
	if err == nil {
		t.Fatal("expected error")
	}
	if !errors.Is(err, innerErr) {
		t.Errorf("expected wrapped error, got %v", err)
	}
}
