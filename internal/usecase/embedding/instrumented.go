package embedding

import (
	"context"
	"fmt"
	"time"

	"go.uber.org/zap"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/metrics"
)

// DefaultMaxAPIBatchSize — максимальный размер батча для одного API-запроса.
const DefaultMaxAPIBatchSize = 256

// BudgetChecker is the local interface for budget enforcement.
type BudgetChecker interface {
	Check(ctx context.Context) error
	Record(tokens int64)
	RemainingDaily() int64
	RemainingMonthly() int64
}

// InstrumentedEmbedder wraps Embedder with budget enforcement and logging.
// Transport metrics (requests, duration, tokens) are recorded in transport/openai.
// This layer owns budget tracking and budget-related metrics only.
type InstrumentedEmbedder struct {
	inner    domain.Embedder
	provider string
	model    string
	budget   BudgetChecker
	logger   *zap.Logger
}

// NewInstrumentedEmbedder wraps an embedder with budget and observability.
func NewInstrumentedEmbedder(
	inner domain.Embedder, provider, model string,
	budget BudgetChecker, logger *zap.Logger,
) *InstrumentedEmbedder {
	return &InstrumentedEmbedder{
		inner:    inner,
		provider: provider,
		model:    model,
		budget:   budget,
		logger:   logger,
	}
}

// Embed checks budget, delegates to the inner embedder, and records usage.
func (p *InstrumentedEmbedder) Embed(
	ctx context.Context, text string,
) (domain.EmbeddingResult, error) {
	// Check budget before making the request
	if p.budget != nil {
		if err := p.budget.Check(ctx); err != nil {
			p.logger.Error("Budget exceeded",
				zap.String("provider", p.provider),
				zap.String("model", p.model),
				zap.Error(err),
			)
			return domain.EmbeddingResult{}, fmt.Errorf("budget check: %w", err)
		}
	}

	start := time.Now()

	result, err := p.inner.Embed(ctx, text)

	duration := time.Since(start)
	p.recordBatchBudget(result.TotalTokens)

	if err != nil {
		p.logger.Error("Embedding request failed",
			zap.String("provider", p.provider),
			zap.String("model", p.model),
			zap.Duration("duration", duration),
			zap.Error(err),
		)
		return result, fmt.Errorf("embed: %w", err)
	}

	p.logger.Debug("Embedding request completed",
		zap.String("provider", p.provider),
		zap.String("model", p.model),
		zap.Duration("duration", duration),
		zap.Int("dimensions", len(result.Embedding)),
		zap.Int("prompt_tokens", result.PromptTokens),
		zap.Int("total_tokens", result.TotalTokens),
	)

	return result, nil
}

// BatchEmbed проверяет бюджет, разбивает на sub-batches, делегирует inner.
func (p *InstrumentedEmbedder) BatchEmbed(
	ctx context.Context, texts []string,
) (domain.BatchEmbeddingResult, error) {
	if len(texts) == 0 {
		return domain.BatchEmbeddingResult{}, nil
	}

	if p.budget != nil {
		if err := p.budget.Check(ctx); err != nil {
			p.logger.Error("Budget exceeded (batch)",
				zap.String("provider", p.provider),
				zap.String("model", p.model),
				zap.Int("batch_size", len(texts)),
				zap.Error(err),
			)
			return domain.BatchEmbeddingResult{}, fmt.Errorf("budget check: %w", err)
		}
	}

	start := time.Now()

	result, err := p.embedChunked(ctx, texts)
	if err != nil {
		return result, err
	}

	duration := time.Since(start)

	p.logger.Debug("Batch embedding completed",
		zap.String("provider", p.provider),
		zap.String("model", p.model),
		zap.Duration("duration", duration),
		zap.Int("batch_size", len(texts)),
		zap.Int("prompt_tokens", result.PromptTokens),
		zap.Int("total_tokens", result.TotalTokens),
	)

	return result, nil
}

// embedChunked разбивает тексты на чанки по DefaultMaxAPIBatchSize с re-check бюджета.
func (p *InstrumentedEmbedder) embedChunked(
	ctx context.Context, texts []string,
) (domain.BatchEmbeddingResult, error) {
	var allEmbeddings [][]float32
	var totalPrompt, totalTokens int
	var unknown bool

	for offset := 0; offset < len(texts); offset += DefaultMaxAPIBatchSize {
		if p.budget != nil && offset > 0 {
			if err := p.budget.Check(ctx); err != nil {
				return domain.BatchEmbeddingResult{
					Embeddings:   allEmbeddings,
					PromptTokens: totalPrompt,
					TotalTokens:  totalTokens,
					UsageUnknown: unknown,
				}, fmt.Errorf(
					"budget check (chunk %d): %w",
					offset,
					err,
				)
			}
		}

		end := offset + DefaultMaxAPIBatchSize
		if end > len(texts) {
			end = len(texts)
		}
		chunk := texts[offset:end]

		chunkResult, err := p.embedInner(ctx, chunk)
		p.recordBatchBudget(chunkResult.TotalTokens)
		totalPrompt += chunkResult.PromptTokens
		totalTokens += chunkResult.TotalTokens
		unknown = unknown || chunkResult.UsageUnknown
		allEmbeddings = append(allEmbeddings, chunkResult.Embeddings...)
		if err != nil {
			p.logger.Error("Batch embedding request failed",
				zap.String("provider", p.provider),
				zap.String("model", p.model),
				zap.Int("chunk_offset", offset),
				zap.Int("chunk_size", len(chunk)),
				zap.Error(err),
			)
			return domain.BatchEmbeddingResult{
				Embeddings:   allEmbeddings,
				PromptTokens: totalPrompt,
				TotalTokens:  totalTokens,
				UsageUnknown: unknown,
			}, fmt.Errorf(
				"batch embed: %w",
				err,
			)
		}

	}

	return domain.BatchEmbeddingResult{
		UsageUnknown: unknown,
		Embeddings:   allEmbeddings,
		PromptTokens: totalPrompt,
		TotalTokens:  totalTokens,
	}, nil
}

func (p *InstrumentedEmbedder) embedInner(
	ctx context.Context, texts []string,
) (domain.BatchEmbeddingResult, error) {
	if be, ok := p.inner.(domain.BatchEmbedder); ok {
		res, err := be.BatchEmbed(ctx, texts)
		if err != nil {
			return res, fmt.Errorf("inner batch embed: %w", err)
		}
		return res, nil
	}
	res, err := domain.BatchFallback(ctx, p.inner, texts)
	if err != nil {
		return res, fmt.Errorf("inner batch fallback: %w", err)
	}
	return res, nil
}

func (p *InstrumentedEmbedder) recordBatchBudget(totalTokens int) {
	if p.budget != nil && totalTokens > 0 {
		p.budget.Record(int64(totalTokens))
		remaining := metrics.EmbeddingBudgetTokensRemaining
		remaining.WithLabelValues(p.provider, "daily").Set(float64(p.budget.RemainingDaily()))
		remaining.WithLabelValues(p.provider, "monthly").Set(float64(p.budget.RemainingMonthly()))
	}
}

// HealthCheck forwards provider availability checks.
func (p *InstrumentedEmbedder) HealthCheck(ctx context.Context) error {
	if h, ok := p.inner.(domain.HealthChecker); ok {
		if err := h.HealthCheck(ctx); err != nil {
			return fmt.Errorf("embedding health: %w", err)
		}
	}
	return nil
}
