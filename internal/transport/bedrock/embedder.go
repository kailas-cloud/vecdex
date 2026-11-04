// Package bedrock implements Cohere Embed v4 using an explicit Bedrock bearer key.
package bedrock

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/kailas-cloud/vecdex/internal/domain"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
	"github.com/kailas-cloud/vecdex/internal/metrics"
)

// MaxBatchSize is the maximum number of Cohere inputs per invocation.
const MaxBatchSize = 96
const maxPayloadBytes = 20_000_000
const maxResponseBytes = 16_000_000

// Config is local to this client. Ambient AWS credentials are never consulted.
type Config struct {
	Provider       string
	APIKey         string
	Region         string
	Model          string
	Dimensions     int
	Purpose        string
	Timeout        time.Duration
	MaxConcurrency int
	// ConcurrencyLimiter is shared by document and query clients of one provider.
	ConcurrencyLimiter chan struct{}
	// HTTPClient allows custom transports, including offline contract tests.
	HTTPClient *http.Client
	Observer   domusage.InvocationObserver
}

// Embedder calls Cohere Embed v4 over the Bedrock runtime API.
type Embedder struct {
	observer                        domusage.InvocationObserver
	client                          *http.Client
	endpoint, token, model, purpose string
	provider                        string
	dimensions                      int
	timeout                         time.Duration
	slots                           chan struct{}
	healthMu                        sync.Mutex
	healthAt                        time.Time
	healthErr                       error
}

// InputTooLongError identifies an explicit model input/context limit rejection.
// Callers may split the input without retrying unrelated validation errors.
type InputTooLongError struct{ RequestID string }

func (e *InputTooLongError) Error() string {
	return "Bedrock input exceeds model context: " + domain.ErrEmbeddingProviderError.Error()
}
func (e *InputTooLongError) Unwrap() error { return domain.ErrEmbeddingProviderError }

// APIError retains safe upstream status and request metadata without response bodies.
type APIError struct {
	StatusCode      int
	Code, RequestID string
}

func (e *APIError) Error() string {
	return fmt.Sprintf(
		"Bedrock request failed (status %d, code %s, request %s): %s",
		e.StatusCode,
		e.Code,
		e.RequestID,
		domain.ErrEmbeddingProviderError,
	)
}
func (e *APIError) Unwrap() error { return domain.ErrEmbeddingProviderError }

// NewEmbedder validates settings and creates an isolated bearer-authenticated client.
func NewEmbedder(cfg *Config) (*Embedder, error) {
	if err := validateConfig(cfg); err != nil {
		return nil, err
	}
	if cfg.ConcurrencyLimiter != nil && cap(cfg.ConcurrencyLimiter) == 0 {
		return nil, errors.New("bedrock limiter must have positive capacity")
	}
	settings := *cfg
	cfg = &settings
	applyConfigDefaults(cfg)

	slots := cfg.ConcurrencyLimiter
	if slots == nil {
		slots = make(chan struct{}, cfg.MaxConcurrency)
	}
	client := &http.Client{}
	if cfg.HTTPClient != nil {
		*client = *cfg.HTTPClient
	}
	// Never forward credentials to a redirect destination.
	client.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	return &Embedder{
		provider: cfg.Provider,
		observer: cfg.Observer,
		client:   client,
		endpoint: "https://bedrock-runtime." + cfg.Region + ".amazonaws.com/model/" + url.PathEscape(
			cfg.Model,
		) + "/invoke",
		token:      cfg.APIKey,
		model:      cfg.Model,
		dimensions: cfg.Dimensions,
		purpose:    cfg.Purpose,
		timeout:    cfg.Timeout,
		slots:      slots,
	}, nil
}

func applyConfigDefaults(cfg *Config) {
	if cfg.Timeout == 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.MaxConcurrency == 0 {
		cfg.MaxConcurrency = 4
	}
	if cfg.Provider == "" {
		cfg.Provider = "bedrock"
	}
}

// Embed embeds one complete text without truncation.
func (e *Embedder) Embed(ctx context.Context, text string) (domain.EmbeddingResult, error) {
	r, err := e.BatchEmbed(ctx, []string{text})
	if err != nil {
		return domain.EmbeddingResult{
			PromptTokens: r.PromptTokens,
			TotalTokens:  r.TotalTokens,
			UsageUnknown: r.UsageUnknown,
		}, err
	}
	return domain.EmbeddingResult{
		Embedding:    r.Embeddings[0],
		PromptTokens: r.PromptTokens,
		TotalTokens:  r.TotalTokens,
		UsageUnknown: r.UsageUnknown,
	}, nil
}

// BatchEmbed preserves input order and usage from completed native batches.
func (e *Embedder) BatchEmbed(ctx context.Context, texts []string) (domain.BatchEmbeddingResult, error) {
	out := domain.BatchEmbeddingResult{}
	for start := 0; start < len(texts); start += MaxBatchSize {
		end := min(start+MaxBatchSize, len(texts))
		r, err := e.invoke(ctx, texts[start:end])
		out.PromptTokens += r.PromptTokens
		out.TotalTokens += r.TotalTokens
		out.UsageUnknown = out.UsageUnknown || r.UsageUnknown
		if err != nil {
			return out, err
		}
		out.Embeddings = append(out.Embeddings, r.Embeddings...)
	}
	return out, nil
}

func (e *Embedder) invoke(ctx context.Context, texts []string) (result domain.BatchEmbeddingResult, resultErr error) {
	body, err := e.encodeInput(texts)
	if err != nil {
		return result, err
	}
	if len(body) > maxPayloadBytes {
		if len(texts) > 1 {
			return e.invokeSplit(ctx, texts)
		}
		return result, &InputTooLongError{}
	}
	return e.invokePrepared(ctx, body, len(texts))
}

func (e *Embedder) encodeInput(texts []string) ([]byte, error) {
	body, err := json.Marshal(struct {
		Texts           []string `json:"texts"`
		InputType       string   `json:"input_type"`
		OutputDimension int      `json:"output_dimension"`
		MaxTokens       int      `json:"max_tokens"`
		Truncate        string   `json:"truncate"`
	}{texts, e.purpose, e.dimensions, 128000, "NONE"})
	if err != nil {
		return nil, fmt.Errorf("encode Bedrock request: %w", err)
	}
	return body, nil
}

func (e *Embedder) invokePrepared(
	ctx context.Context, body []byte, inputs int,
) (result domain.BatchEmbeddingResult, resultErr error) {
	start := time.Now()
	defer func() { e.observe(result, resultErr, time.Since(start)) }()
	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()
	select {
	case e.slots <- struct{}{}:
		defer func() { <-e.slots }()
	case <-ctx.Done():
		return result, fmt.Errorf("wait for Bedrock slot: %w", ctx.Err())
	}
	return e.invokeWithRetry(ctx, body, inputs)
}

func (e *Embedder) invokeWithRetry(ctx context.Context, body []byte, inputs int) (domain.BatchEmbeddingResult, error) {
	result := domain.BatchEmbeddingResult{}
	for attempt := 0; attempt < 3; attempt++ {
		resp, data, callErr := e.request(ctx, body, inputs)
		if callErr != nil {
			result.UsageUnknown = result.UsageUnknown || errors.Is(callErr, domain.ErrEmbeddingProviderError)
			return result, callErr
		}
		addInvocationUsage(&result, resp)
		if resp.StatusCode == http.StatusOK {
			decoded, decodeErr := e.decode(data, resp.Header, inputs)
			result.Embeddings = decoded.Embeddings
			return result, decodeErr
		}
		callErr = classifyError(resp, data)
		if attempt == 2 || !retryable(resp.StatusCode) {
			return result, callErr
		}
		if err := waitRetry(ctx, resp.Header, attempt); err != nil {
			return result, err
		}
	}
	return result, domain.ErrEmbeddingProviderError
}

func addInvocationUsage(result *domain.BatchEmbeddingResult, resp *upstreamResponse) {
	tokens, unknown := invocationUsage(resp.StatusCode, resp.Header)
	result.TotalTokens += tokens
	result.PromptTokens += tokens
	result.UsageUnknown = result.UsageUnknown || unknown
}

func (e *Embedder) request(ctx context.Context, body []byte, inputs int) (*upstreamResponse, []byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, e.endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, nil, fmt.Errorf("create Bedrock request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+e.token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	complete, err := domusage.BeginInvocation(ctx, e.observer, inputs)
	if err != nil {
		return nil, nil, fmt.Errorf("bedrock accounting: %w", err)
	}
	tokens, unknown := 0, true
	defer func() { complete(tokens, unknown) }()
	resp, err := e.client.Do(req)
	if err != nil {
		return nil, nil, fmt.Errorf("bedrock transport: %w: %w", domain.ErrEmbeddingProviderError, ctxError(ctx, err))
	}
	defer func() { _ = resp.Body.Close() }()
	tokens, unknown = invocationUsage(resp.StatusCode, resp.Header)
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, nil, fmt.Errorf("read Bedrock response: %w", domain.ErrEmbeddingProviderError)
	}
	if len(data) > maxResponseBytes {
		return nil, nil, fmt.Errorf("bedrock response too large: %w", domain.ErrEmbeddingProviderError)
	}
	return &upstreamResponse{StatusCode: resp.StatusCode, Header: resp.Header}, data, nil
}

type upstreamResponse struct {
	StatusCode int
	Header     http.Header
}

func classifyError(resp *upstreamResponse, data []byte) error {
	var detail struct {
		Message string `json:"message"`
	}
	_ = json.Unmarshal(data, &detail)
	code, _, _ := strings.Cut(resp.Header.Get("X-Amzn-Errortype"), ":")
	if resp.StatusCode == 400 && (code == "ValidationException" || code == "") && contextLimitMessage(detail.Message) {
		return &InputTooLongError{RequestID: resp.Header.Get("X-Amzn-Requestid")}
	}
	return &APIError{StatusCode: resp.StatusCode, Code: code, RequestID: resp.Header.Get("X-Amzn-Requestid")}
}

func contextLimitMessage(message string) bool {
	message = strings.ToLower(message)
	phrases := []string{
		"maximum context", "too many tokens", "exceeds the maximum token", "input is too long",
	}
	for _, phrase := range phrases {
		if strings.Contains(message, phrase) {
			return true
		}
	}
	return false
}

func retryable(status int) bool {
	switch status {
	case 429, 500, 502, 503, 504:
		return true
	default:
		return false
	}
}

func waitRetry(ctx context.Context, headers http.Header, attempt int) error {
	delay := time.Duration(100*(1<<attempt)) * time.Millisecond
	if n, err := strconv.Atoi(headers.Get("Retry-After")); err == nil && n > 0 {
		delay = time.Duration(min(n, 30)) * time.Second
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return fmt.Errorf("bedrock retry interrupted: %w", ctx.Err())
	}
}

func (e *Embedder) observe(result domain.BatchEmbeddingResult, err error, duration time.Duration) {
	status := "success"
	if err != nil {
		status = "error"
		metrics.EmbeddingErrorsTotal.WithLabelValues(e.provider, e.model, "api_error").Inc()
	}
	metrics.EmbeddingRequestsTotal.WithLabelValues(e.provider, e.model, status).Inc()
	metrics.EmbeddingRequestDuration.WithLabelValues(e.provider, e.model).Observe(duration.Seconds())
	if result.TotalTokens > 0 {
		metrics.EmbeddingTokensTotal.WithLabelValues(e.provider, e.model, "prompt").Add(float64(result.PromptTokens))
		metrics.EmbeddingTokensTotal.WithLabelValues(e.provider, e.model, "total").Add(float64(result.TotalTokens))
	}
	if result.UsageUnknown {
		metrics.EmbeddingUsageUnknownTotal.WithLabelValues(e.provider, e.model).Inc()
	}
}

func (e *Embedder) invokeSplit(ctx context.Context, texts []string) (domain.BatchEmbeddingResult, error) {
	a, err := e.invoke(ctx, texts[:len(texts)/2])
	if err != nil {
		return a, err
	}
	b, err := e.invoke(ctx, texts[len(texts)/2:])
	a.Embeddings = append(a.Embeddings, b.Embeddings...)
	a.PromptTokens += b.PromptTokens
	a.TotalTokens += b.TotalTokens
	a.UsageUnknown = a.UsageUnknown || b.UsageUnknown
	return a, err
}

func (e *Embedder) decode(data []byte, h http.Header, count int) (domain.BatchEmbeddingResult, error) {
	out := domain.BatchEmbeddingResult{UsageUnknown: true}
	if n, err := strconv.Atoi(h.Get("X-Amzn-Bedrock-Input-Token-Count")); err == nil && n >= 0 {
		out.PromptTokens = n
		out.TotalTokens = n
		out.UsageUnknown = false
	}
	var body struct {
		Embeddings json.RawMessage `json:"embeddings"`
	}
	if json.Unmarshal(data, &body) != nil {
		return out, fmt.Errorf("invalid Bedrock JSON: %w", domain.ErrEmbeddingProviderError)
	}
	vectors, err := decodeEmbeddings(body.Embeddings)
	if err != nil {
		return out, err
	}
	if err := e.validateVectors(vectors, count); err != nil {
		return out, err
	}
	out.Embeddings = vectors
	return out, nil
}

func decodeEmbeddings(data json.RawMessage) ([][]float32, error) {
	var vectors [][]float32
	if json.Unmarshal(data, &vectors) == nil {
		return vectors, nil
	}
	var typed struct {
		Float [][]float32 `json:"float"`
	}
	if json.Unmarshal(data, &typed) != nil {
		return nil, fmt.Errorf("invalid Bedrock embeddings: %w", domain.ErrEmbeddingProviderError)
	}
	return typed.Float, nil
}

func (e *Embedder) validateVectors(vectors [][]float32, count int) error {
	if len(vectors) != count {
		return fmt.Errorf("invalid Bedrock embedding count: %w", domain.ErrEmbeddingProviderError)
	}

	for _, v := range vectors {
		if len(v) != e.dimensions {
			return fmt.Errorf("bedrock vector dimension mismatch: %w", domain.ErrEmbeddingProviderError)
		}
		for _, f := range v {
			if math.IsNaN(float64(f)) || math.IsInf(float64(f), 0) {
				return fmt.Errorf("invalid Bedrock vector: %w", domain.ErrEmbeddingProviderError)
			}
		}
	}
	return nil
}

func ctxError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("bedrock context: %w", ctx.Err())
	}
	return err
}

// HealthCheck probes the model at most once per minute.
func (e *Embedder) HealthCheck(ctx context.Context) error {
	e.healthMu.Lock()
	defer e.healthMu.Unlock()
	if !e.healthAt.IsZero() && time.Since(e.healthAt) < time.Minute {
		return e.healthErr
	}
	_, e.healthErr = e.Embed(ctx, "health")
	e.healthAt = time.Now()
	return e.healthErr
}

func validateConfig(cfg *Config) error {
	if cfg == nil {
		return errors.New("bedrock configuration is required")
	}
	if strings.TrimSpace(cfg.APIKey) == "" || strings.ContainsAny(cfg.APIKey, "\r\n") {
		return errors.New("bedrock requires a nonempty single-line API key")
	}
	if cfg.Region == "" || strings.ContainsAny(cfg.Region, "/:?# \t\r\n") {
		return errors.New("bedrock requires a valid region")
	}
	return validateModelConfig(cfg)
}

func validateModelConfig(cfg *Config) error {
	switch cfg.Model {
	case "cohere.embed-v4:0", "eu.cohere.embed-v4:0", "us.cohere.embed-v4:0", "global.cohere.embed-v4:0":
	default:
		return errors.New("bedrock requires Cohere Embed v4 model ID")
	}
	switch cfg.Dimensions {
	case 256, 512, 1024, 1536:
	default:
		return errors.New("bedrock dimensions must be 256, 512, 1024 or 1536")
	}
	return validateExecutionConfig(cfg)
}

func validateExecutionConfig(cfg *Config) error {
	if cfg.Purpose != "search_document" && cfg.Purpose != "search_query" {
		return errors.New("bedrock purpose must be search_document or search_query")
	}
	if cfg.Timeout < 0 || cfg.MaxConcurrency < 0 {
		return errors.New("bedrock timeout and concurrency must be positive")
	}
	return nil
}

// Is lets ingestion split explicit context errors without depending on this transport.
func (e *InputTooLongError) Is(target error) bool { return target == domain.ErrEmbeddingInputTooLong }

func invocationUsage(status int, headers http.Header) (int, bool) {
	if value, err := strconv.Atoi(headers.Get("X-Amzn-Bedrock-Input-Token-Count")); err == nil && value >= 0 {
		return value, false
	}
	if status >= 400 && status < 500 {
		return 0, false
	}
	return 0, true
}
