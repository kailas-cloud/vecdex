// Package ingestion prepares complete documents for a model without truncating text.
package ingestion

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
	"unicode"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
)

// End-to-end deadlines cover all preparation, retries and storage work.
const (
	DocumentTimeout = 120 * time.Second
	BatchTimeout    = 300 * time.Second
)

// Embedder is the model operation used for each complete input or context-sized part.
type Embedder interface {
	Embed(context.Context, string) (domain.EmbeddingResult, error)
}

// Fragment retains the exact source span and its document embedding.
type Fragment = domdoc.Fragment

// Prepared contains a full set of fragments and aggregate usage, even on failure.
// Fragments are returned only when the entire document was prepared successfully.
type Prepared struct {
	Fragments                 []Fragment
	Vector                    []float32
	PromptTokens, TotalTokens int
	UsageUnknown              bool
}

// Prepare first sends the complete text. Only explicit context-limit rejections
// cause splitting; auth, throttling, cancellation and unrelated validation errors propagate.
func Prepare(ctx context.Context, embedder Embedder, text string, dimensions int) (*Prepared, error) {
	prepared := &Prepared{}
	if text == "" {
		return prepared, fmt.Errorf("document content is required: %w", domain.ErrInvalidSchema)
	}
	fragments, err := prepareParts(ctx, embedder, text, dimensions, prepared)
	if err != nil {
		return prepared, err
	}
	prepared.Fragments = fragments
	prepared.Vector = representativeVector(fragments)
	return prepared, nil
}

func prepareParts(
	ctx context.Context,
	embedder Embedder,
	text string,
	dimensions int,
	usage *Prepared,
) ([]Fragment, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("prepare document: %w", err)
	}
	result, err := embedder.Embed(ctx, text)
	usage.PromptTokens += result.PromptTokens
	usage.TotalTokens += result.TotalTokens
	usage.UsageUnknown = usage.UsageUnknown || result.UsageUnknown
	if err == nil {
		if err := domain.ValidateVector(result.Embedding, dimensions); err != nil {
			return nil, fmt.Errorf("prepare vector: %w", err)
		}
		return []Fragment{{Content: text, Vector: result.Embedding}}, nil
	}
	if !errors.Is(err, domain.ErrEmbeddingInputTooLong) {
		return nil, fmt.Errorf("prepare embedding: %w", err)
	}
	left, right, ok := splitText(text)
	if !ok {
		return nil, fmt.Errorf("model rejected indivisible text: %w", err)
	}
	before, err := prepareParts(ctx, embedder, left, dimensions, usage)
	if err != nil {
		return nil, err
	}
	after, err := prepareParts(ctx, embedder, right, dimensions, usage)
	if err != nil {
		return nil, err
	}
	return append(before, after...), nil
}

// splitText preserves every rune, including whitespace, and always makes progress.
func splitText(text string) (left, right string, ok bool) {
	runes := []rune(text)
	if len(runes) < 2 {
		return "", "", false
	}
	middle := len(runes) / 2
	for priority := 0; priority < 3; priority++ {
		for distance := 0; distance <= len(runes)/4; distance++ {
			for _, cut := range []int{middle - distance, middle + distance} {
				if preferredBoundary(runes, cut, priority) {
					return string(runes[:cut]), string(runes[cut:]), true
				}
			}
		}
	}
	return string(runes[:middle]), string(runes[middle:]), true
}

func preferredBoundary(runes []rune, cut, priority int) bool {
	if cut <= 0 || cut >= len(runes) {
		return false
	}
	switch priority {
	case 0:
		return cut > 1 && runes[cut-1] == '\n' && runes[cut-2] == '\n'
	case 1:
		return cut > 1 && unicode.IsSpace(runes[cut-1]) && sentenceEnd(runes[cut-2])
	default:
		return unicode.IsSpace(runes[cut-1])
	}
}

func sentenceEnd(r rune) bool {
	switch r {
	case '.', '!', '?', '。', '！', '？':
		return true
	default:
		return false
	}
}

func representativeVector(parts []Fragment) []float32 {
	if len(parts) == 1 {
		return parts[0].Vector
	}
	sum := make([]float64, len(parts[0].Vector))
	for _, part := range parts {
		weight := float64(len([]rune(part.Content)))
		for i, value := range part.Vector {
			sum[i] += float64(value) * weight
		}
	}
	var norm float64
	for _, value := range sum {
		norm += value * value
	}
	norm = math.Sqrt(norm)
	if norm == 0 {
		return parts[0].Vector
	}
	vector := make([]float32, len(sum))
	for i, value := range sum {
		vector[i] = float32(value / norm)
	}
	return vector
}
