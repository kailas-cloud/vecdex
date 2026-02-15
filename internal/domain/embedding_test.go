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
