package ingestion

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/kailas-cloud/vecdex/internal/domain"
)

type boundedEmbedder struct {
	maxRunes int
	calls    int
	failText string
}

func (e *boundedEmbedder) Embed(_ context.Context, text string) (domain.EmbeddingResult, error) {
	e.calls++
	if utf8.RuneCountInString(text) > e.maxRunes {
		return domain.EmbeddingResult{}, domain.ErrEmbeddingInputTooLong
	}
	if e.failText != "" && strings.Contains(text, e.failText) {
		return domain.EmbeddingResult{}, domain.ErrEmbeddingProviderError
	}
	return domain.EmbeddingResult{Embedding: []float32{1, 0}, PromptTokens: 1, TotalTokens: 1}, nil
}
func TestFittingDocumentUsesOneEmbedding(t *testing.T) {
	e := &boundedEmbedder{maxRunes: 128000}
	text := strings.Repeat("документ ", 1000)
	result, err := Prepare(context.Background(), e, text, 2)
	if err != nil || e.calls != 1 || len(result.Fragments) != 1 || result.Fragments[0].Content != text {
		t.Fatalf("calls=%d err=%v", e.calls, err)
	}
}
func TestOversizedUnicodeHasNoTextLoss(t *testing.T) {
	e := &boundedEmbedder{maxRunes: 16}
	text := strings.Repeat("Русский 中文 👩‍💻\n\n", 30)
	result, err := Prepare(context.Background(), e, text, 2)
	if err != nil {
		t.Fatal(err)
	}
	var joined strings.Builder
	for _, part := range result.Fragments {
		joined.WriteString(part.Content)
		if !utf8.ValidString(part.Content) || utf8.RuneCountInString(part.Content) > 16 {
			t.Fatal("invalid fragment")
		}
	}
	if joined.String() != text {
		t.Fatal("source text changed")
	}
	if result.TotalTokens != len(result.Fragments) {
		t.Fatal("usage mismatch")
	}
}
func TestFailedPreparationKeepsUsageAndHidesPartialFragments(t *testing.T) {
	e := &boundedEmbedder{maxRunes: 4, failText: "FAIL"}
	result, err := Prepare(context.Background(), e, "goodFAIL", 2)
	if err == nil || result.TotalTokens != 1 || len(result.Fragments) != 0 {
		t.Fatalf("result %+v error %v", result, err)
	}
}
func TestUnrelatedFailureDoesNotSplit(t *testing.T) {
	e := &boundedEmbedder{maxRunes: 100, failText: "FAIL"}
	_, err := Prepare(context.Background(), e, "FAIL", 2)
	if !errors.Is(err, domain.ErrEmbeddingProviderError) || e.calls != 1 {
		t.Fatalf("calls=%d error %v", e.calls, err)
	}
}
