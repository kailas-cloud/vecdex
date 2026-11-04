package bedrock

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kailas-cloud/vecdex/internal/domain"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }
func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}
}
func fixture(t *testing.T, transport roundTripFunc) *Embedder {
	t.Helper()
	e, err := NewEmbedder(
		&Config{
			APIKey:     "explicit-token",
			Region:     "eu-west-1",
			Model:      "cohere.embed-v4:0",
			Dimensions: 256,
			Purpose:    "search_document",
			HTTPClient: &http.Client{Transport: transport},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	return e
}
func success(count int) *http.Response {
	v := make([][]float32, count)
	for i := range v {
		v[i] = make([]float32, 256)
		v[i][0] = float32(i + 1)
	}
	b, _ := json.Marshal(map[string]any{"embeddings": v})
	return response(200, string(b))
}
func TestContractAndNativeBatches(t *testing.T) {
	t.Setenv("AWS_ACCESS_KEY_ID", "ambient")
	t.Setenv("AWS_BEARER_TOKEN_BEDROCK", "ambient")
	calls := 0
	e := fixture(t, func(r *http.Request) (*http.Response, error) {
		calls++
		if r.URL.Host+r.URL.Path != "bedrock-runtime.eu-west-1.amazonaws.com/model/cohere.embed-v4:0/invoke" {
			t.Fatalf("URL %s", r.URL)
		}
		if r.Header.Get("Authorization") != "Bearer explicit-token" {
			t.Fatal("incorrect token")
		}
		var body struct {
			Texts     []string `json:"texts"`
			Purpose   string   `json:"input_type"`
			Dimension int      `json:"output_dimension"`
			Truncate  string   `json:"truncate"`
			MaxTokens int      `json:"max_tokens"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		for field, ok := range map[string]bool{
			"purpose": body.Purpose == "search_document", "dimensions": body.Dimension == 256,
			"truncate": body.Truncate == "NONE", "context": body.MaxTokens == 128000,
		} {
			if !ok {
				t.Fatalf("invalid %s", field)
			}
		}

		if len(body.Texts) > 96 {
			t.Fatal("native batch too large")
		}
		// No CountTokens request and no tokenization/truncation of long input.
		if calls == 1 && len(body.Texts[0]) != 10000 {
			t.Fatal("document was shortened")
		}
		out := success(len(body.Texts))
		out.Header.Set("X-Amzn-Bedrock-Input-Token-Count", strconv.Itoa(len(body.Texts)*10))
		return out, nil
	})
	texts := make([]string, 97)
	texts[0] = strings.Repeat("a", 10000)
	result, err := e.BatchEmbed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 || len(result.Embeddings) != 97 || result.TotalTokens != 970 || result.UsageUnknown {
		t.Fatalf("calls %d, result %+v", calls, result)
	}
	if got := result.Embeddings[96][0]; got != 1 {
		t.Fatalf("batch merge order: %v", got)
	}
}
func TestUsageHeaders(t *testing.T) {
	for _, header := range []string{"", "invalid", "-1", "0", "42"} {
		t.Run(header, func(t *testing.T) {
			e := fixture(t, func(_ *http.Request) (*http.Response, error) {
				r := success(1)
				r.Header.Set("X-Amzn-Bedrock-Input-Token-Count", header)
				return r, nil
			})
			r, err := e.Embed(context.Background(), "text")
			if err != nil {
				t.Fatal(err)
			}
			if r.UsageUnknown != (header != "0" && header != "42") {
				t.Fatalf("usage %+v", r)
			}
		})
	}
}
func TestInvalidResponses(t *testing.T) {
	for _, body := range []string{`{}`, `not-json`, `{"embeddings":[[]]}`, `{"embeddings":[[1]]}`} {
		t.Run(body, func(t *testing.T) {
			e := fixture(t, func(_ *http.Request) (*http.Response, error) { return response(200, body), nil })
			_, err := e.Embed(context.Background(), "text")
			if !errors.Is(err, domain.ErrEmbeddingProviderError) {
				t.Fatalf("error %v", err)
			}
		})
	}
}
func TestRetryAndAuthFailure(t *testing.T) {
	for _, status := range []int{403, 429, 503} {
		t.Run(strconv.Itoa(status), func(t *testing.T) {
			calls := 0
			e := fixture(t, func(_ *http.Request) (*http.Response, error) {
				calls++
				if calls < 3 {
					return response(status, `{"message":"secret must not escape"}`), nil
				}
				return success(1), nil
			})
			_, err := e.Embed(context.Background(), "text")
			if status == 403 {
				if calls != 1 || err == nil || strings.Contains(err.Error(), "secret") {
					t.Fatalf("calls %d err %v", calls, err)
				}
			} else if calls != 3 || err != nil {
				t.Fatalf("calls %d err %v", calls, err)
			}
		})
	}
}
func TestPartialUsageOnLateFailure(t *testing.T) {
	calls := 0
	e := fixture(t, func(_ *http.Request) (*http.Response, error) {
		calls++
		if calls > 1 {
			return response(403, "{}"), nil
		}
		r := success(96)
		r.Header.Set("X-Amzn-Bedrock-Input-Token-Count", "123")
		return r, nil
	})
	r, err := e.BatchEmbed(context.Background(), make([]string, 97))
	if err == nil || r.TotalTokens != 123 {
		t.Fatalf("usage lost: %+v %v", r, err)
	}
}
func TestCancellationAndConcurrency(t *testing.T) {
	var active, maxActive atomic.Int32
	entered := make(chan struct{}, 4)
	e := fixture(t, func(r *http.Request) (*http.Response, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maxActive.Load(); n > old; old = maxActive.Load() {
			if maxActive.CompareAndSwap(old, n) {
				break
			}
		}
		entered <- struct{}{}
		<-r.Context().Done()
		return nil, r.Context().Err()
	})
	e.slots = make(chan struct{}, 1)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 2)
	for range 2 {
		go func() { _, err := e.Embed(ctx, "text"); done <- err }()
	}
	<-entered
	cancel()
	for range 2 {
		select {
		case err := <-done:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("error %v", err)
			}
		case <-time.After(time.Second):
			t.Fatal("cancellation hung")
		}
	}
	if maxActive.Load() != 1 {
		t.Fatalf("concurrency %d", maxActive.Load())
	}
}
func TestContextLimitClassification(t *testing.T) {
	for _, message := range []string{"Input is too long", "unknown parameter"} {
		t.Run(message, func(t *testing.T) {
			e := fixture(t, func(_ *http.Request) (*http.Response, error) {
				r := response(400, `{"message":"`+message+`"}`)
				r.Header.Set("X-Amzn-Errortype", "ValidationException")
				return r, nil
			})
			_, err := e.Embed(context.Background(), "text")
			var tooLong *InputTooLongError
			if errors.As(err, &tooLong) != (message == "Input is too long") {
				t.Fatalf("error %v", err)
			}
		})
	}
}

func TestTypedFloatResponse(t *testing.T) {
	e := fixture(t, func(_ *http.Request) (*http.Response, error) {
		v := make([]float32, 256)
		b, _ := json.Marshal(map[string]any{"embeddings": map[string]any{"float": [][]float32{v}}})
		return response(200, string(b)), nil
	})
	r, err := e.Embed(context.Background(), "text")
	if err != nil || len(r.Embedding) != 256 {
		t.Fatalf("typed float response: %+v %v", r, err)
	}
}

type accountingCall struct {
	tokens  int
	unknown bool
}
type recordingObserver struct {
	inputs []int
	calls  []accountingCall
	err    error
}

func (o *recordingObserver) Begin(_ context.Context, inputs int) (func(int, bool), error) {
	if o.err != nil {
		return nil, o.err
	}
	o.inputs = append(o.inputs, inputs)
	return func(tokens int, unknown bool) { o.calls = append(o.calls, accountingCall{tokens, unknown}) }, nil
}

func TestAccountingIncludesNativeBatchesAndHealth(t *testing.T) {
	recorder := &recordingObserver{}
	e := fixture(t, func(r *http.Request) (*http.Response, error) {
		var body struct {
			Texts []string `json:"texts"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		resp := success(len(body.Texts))
		resp.Header.Set("X-Amzn-Bedrock-Input-Token-Count", "11")
		return resp, nil
	})
	e.observer = recorder
	if _, err := e.BatchEmbed(context.Background(), make([]string, 97)); err != nil {
		t.Fatal(err)
	}
	if err := e.HealthCheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.HealthCheck(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(recorder.inputs) != 3 || recorder.inputs[0] != 96 || recorder.inputs[1] != 1 || recorder.inputs[2] != 1 {
		t.Fatalf("incorrect upstream accounting: %+v", recorder)
	}
	for _, call := range recorder.calls {
		if call.tokens != 11 || call.unknown {
			t.Fatalf("incorrect usage: %+v", call)
		}
	}
}

func TestAccountingOnUncertainAndRejectedCalls(t *testing.T) {
	for _, tc := range []struct {
		name     string
		status   int
		unknown  bool
		attempts int
	}{
		{"missing header", 200, true, 1}, {"bad input", 400, false, 1}, {"upstream failure", 500, true, 3},
	} {
		t.Run(tc.name, func(t *testing.T) {
			recorder := &recordingObserver{}
			e := fixture(t, func(*http.Request) (*http.Response, error) { return response(tc.status, `{}`), nil })
			e.observer = recorder
			_, _ = e.Embed(context.Background(), "source")
			if len(recorder.calls) != tc.attempts {
				t.Fatalf("calls: %+v", recorder)
			}
			for _, call := range recorder.calls {
				if call.unknown != tc.unknown {
					t.Fatalf("incorrect usage state: %+v", call)
				}
			}
		})
	}
	recorder := &recordingObserver{err: domain.ErrEmbeddingQuotaExceeded}
	e := fixture(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("upstream invoked without reservation")
		return nil, errors.New("unexpected request")
	})
	e.observer = recorder
	if _, err := e.Embed(context.Background(), "source"); !errors.Is(err, domain.ErrEmbeddingQuotaExceeded) {
		t.Fatal(err)
	}
}

func TestSharedProviderConcurrencyAcrossPurposes(t *testing.T) {
	limiter := make(chan struct{}, 3)
	var active, peak atomic.Int32
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		current := active.Add(1)
		defer active.Add(-1)
		for previous := peak.Load(); current > previous; previous = peak.Load() {
			if peak.CompareAndSwap(previous, current) {
				break
			}
		}
		select {
		case <-r.Context().Done():
			return nil, r.Context().Err()
		case <-time.After(10 * time.Millisecond):
		}
		return success(1), nil
	})
	clients := make([]*Embedder, 2)
	for i, purpose := range []string{"search_document", "search_query"} {
		client, err := NewEmbedder(
			&Config{APIKey: "explicit", Region: "us-east-1", Model: "cohere.embed-v4:0", Dimensions: 256,
				Purpose: purpose, ConcurrencyLimiter: limiter, HTTPClient: &http.Client{Transport: transport}},
		)
		if err != nil {
			t.Fatal(err)
		}
		clients[i] = client
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	failures := make(chan error, 32)
	for i := range 32 {
		go func() { _, err := clients[i%2].Embed(ctx, "source"); failures <- err }()
	}
	for range 32 {
		if err := <-failures; err != nil {
			t.Fatal(err)
		}
	}
	if peak.Load() != 3 {
		t.Fatalf("provider concurrency %d, expected three across both clients", peak.Load())
	}
}
