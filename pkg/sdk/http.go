package vecdex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kailas-cloud/vecdex/internal/domain"
	gen "github.com/kailas-cloud/vecdex/internal/transport/generated"
)

// HTTPOption configures a client that accesses the public Vecdex HTTP API.
type HTTPOption func(*httpClientConfig)

type httpClientConfig struct{ client *http.Client }

// WithHTTPClient supplies a transport, timeout and connection pool. The caller
// retains ownership of this client; closing Vecdex does not close its pool.
func WithHTTPClient(client *http.Client) HTTPOption {
	return func(config *httpClientConfig) { config.client = client }
}

// NewHTTP connects the SDK facade to the public /api/v2 API. baseURL is the
// server root, for example http://localhost:8080. Embedding runs on the server.
// The SDK adds no retry loop. Use IdempotencyKey to replay a committed outcome;
// the supplied HTTP transport may also retry replayable keyed requests.
func NewHTTP(baseURL, apiKey string, options ...HTTPOption) (*Client, error) {
	endpoint, err := url.Parse(baseURL)
	if err != nil || endpoint.Host == "" || (endpoint.Scheme != "http" && endpoint.Scheme != "https") ||
		endpoint.RawQuery != "" || endpoint.Fragment != "" {
		return nil, fmt.Errorf("vecdex: HTTP server root must be an absolute http(s) URL")
	}
	config := httpClientConfig{}
	for _, option := range options {
		option(&config)
	}
	backend := &httpBackend{baseURL: strings.TrimRight(baseURL, "/"), apiKey: apiKey, client: config.client}
	if backend.client == nil {
		backend.client = &http.Client{Timeout: 330 * time.Second}
	}
	clientCopy := *backend.client
	clientCopy.CheckRedirect = func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }
	backend.client = &clientCopy
	obs, err := newObserver(nil, nil)
	if err != nil {
		return nil, err
	}
	return &Client{
		http: backend, obs: obs,
		collSvc: &httpCollections{backend}, docSvc: &httpDocuments{backend},
		batchSvc: &httpBatches{backend}, searchSvc: &httpSearch{backend},
		healthSvc: &httpHealth{backend}, usageSvc: &httpUsage{backend},
	}, nil
}

// HTTPError retains the server error and status. errors.Is works with the same
// SDK sentinels as the embedded client. VersionConflictError is also available
// through errors.As when the server returns version_conflict.
type HTTPError struct {
	StatusCode int
	Code       string
	Message    string
	RequestID  string
	cause      error
}

func (e *HTTPError) Error() string {
	return fmt.Sprintf("vecdex HTTP %d %s: %s", e.StatusCode, e.Code, e.Message)
}
func (e *HTTPError) Unwrap() error { return e.cause }

type httpBackend struct {
	client  *http.Client
	baseURL string
	apiKey  string
}

func (h *httpBackend) call(
	ctx context.Context, method, path string, body, reply any, acceptedStatuses ...int,
) (int, error) {
	var input io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, fmt.Errorf("encode HTTP request: %w", err)
		}
		input = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, method, h.baseURL+path, input)
	if err != nil {
		return 0, fmt.Errorf("create HTTP request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if h.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+h.apiKey)
	}
	if version := domain.ExpectedVersion(ctx); version != nil {
		req.Header.Set("If-Match", strconv.Quote(*version))
	}
	if key := domain.IdempotencyKey(ctx); key != "" {
		req.Header.Set("Idempotency-Key", key)
	}
	response, err := h.client.Do(req)
	if err != nil {
		return 0, fmt.Errorf("HTTP %s %s: %w", method, path, err)
	}
	defer func() { _ = response.Body.Close() }()
	if (response.StatusCode < 200 || response.StatusCode >= 300) &&
		!slices.Contains(acceptedStatuses, response.StatusCode) {
		return response.StatusCode, decodeHTTPError(response)
	}
	if reply == nil {
		if _, err := io.Copy(io.Discard, response.Body); err != nil {
			return response.StatusCode, fmt.Errorf("read HTTP response: %w", err)
		}
		return response.StatusCode, nil
	}
	if err := json.NewDecoder(response.Body).Decode(reply); err != nil {
		return response.StatusCode, fmt.Errorf("decode HTTP response: %w", err)
	}
	return response.StatusCode, nil
}

func decodeHTTPError(response *http.Response) error {
	var reply gen.ErrorResponse
	if err := json.NewDecoder(response.Body).Decode(&reply); err != nil {
		return &HTTPError{StatusCode: response.StatusCode, Message: response.Status,
			RequestID: response.Header.Get("X-Request-ID")}
	}
	return newHTTPError(response.StatusCode, response.Header.Get("X-Request-ID"), &reply)
}

var httpErrorCauses = map[gen.ErrorResponseCode]error{
	gen.ErrorResponseCodeCollectionNotFound:        ErrNotFound,
	gen.ErrorResponseCodeDocumentNotFound:          ErrDocumentNotFound,
	gen.ErrorResponseCodeCollectionAlreadyExists:   ErrAlreadyExists,
	gen.ErrorResponseCodeValidationFailed:          ErrInvalidSchema,
	gen.ErrorResponseCodeBadRequest:                ErrInvalidSchema,
	gen.ErrorResponseCodeVectorDimMismatch:         ErrVectorDimMismatch,
	gen.ErrorResponseCodeRateLimited:               ErrRateLimited,
	gen.ErrorResponseCodeEmbeddingQuotaExceeded:    ErrEmbeddingQuotaExceeded,
	gen.ErrorResponseCodeEmbeddingProviderError:    ErrEmbeddingProviderError,
	gen.ErrorResponseCodeEmbeddingSpaceMismatch:    ErrEmbeddingSpaceMismatch,
	gen.ErrorResponseCodeKeywordSearchNotSupported: ErrKeywordSearchNotSupported,
	gen.ErrorResponseCodeNotImplemented:            ErrNotImplemented,
	gen.ErrorResponseCodeIdempotencyConflict:       ErrIdempotencyConflict,
	gen.ErrorResponseCodeIdempotencyInProgress:     ErrIdempotencyInProgress,
}

func newHTTPError(status int, requestID string, reply *gen.ErrorResponse) error {
	cause := httpErrorCauses[reply.Code]
	if reply.Code == gen.ErrorResponseCodeVersionConflict {
		current := ""
		if reply.CurrentVersion != nil {
			current = *reply.CurrentVersion
		}
		cause = &VersionConflictError{CurrentVersion: current}
	}
	return &HTTPError{StatusCode: status, Code: string(reply.Code), Message: reply.Message,
		RequestID: requestID, cause: cause}
}

func collectionHTTPPath(name string) string { return "/api/v2/collections/" + url.PathEscape(name) }
func documentHTTPPath(collection, id string) string {
	return collectionHTTPPath(collection) + "/documents/" + url.PathEscape(id)
}
