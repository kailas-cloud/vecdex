package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/memorycontext"
	"github.com/modelcontextprotocol/go-sdk/jsonrpc"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

type agentError struct {
	Code           string `json:"code"`
	Message        string `json:"message"`
	Retryable      bool   `json:"retryable"`
	Action         string `json:"action"`
	CurrentVersion string `json:"current_version,omitempty"`
}

func (e *agentError) Error() string { return e.Message }

type envelope struct {
	OK    bool        `json:"ok"`
	Data  any         `json:"data,omitempty"`
	Error *agentError `json:"error,omitempty"`
}

func invalid(message string) error {
	return &agentError{Code: "invalid_argument", Message: message, Action: "Correct the arguments and retry."}
}

func classify(err error) *agentError {
	var known *agentError
	if errors.As(err, &known) {
		return known
	}
	return domainFailure(err)
}

func domainFailure(err error) *agentError {
	e := &agentError{Code: "internal_error",
		Message: "Memory operation failed.",
		Action:  "Retry later; if the problem persists, contact the operator."}
	switch {
	case errors.Is(err, context.Canceled):
		e.Code,
			e.Message,
			e.Action = "cancelled",
			"The request was cancelled.",
			"Read current state before retrying a mutation with its original idempotency key."
	case errors.Is(err, context.DeadlineExceeded):
		e.Code, e.Message, e.Retryable = "timeout", "The request timed out.", true
	case errors.Is(err, domain.ErrCollectionGenerationMismatch):
		e.Code,
			e.Message,
			e.Action = "stale_context",
			"The collection generation changed.",
			"Inspect the collection and create a new context; do not retarget this context."
	case errors.Is(err,
		domain.ErrNotFound),
		errors.Is(err,
			domain.ErrDocumentNotFound),
		errors.Is(err,
			memorycontext.ErrNotFound):
		e.Code,
			e.Message,
			e.Action = "not_found",
			"The requested memory resource is absent or expired.",
			"List current resources or create a new context."
	case errors.Is(err, domain.ErrRevisionConflict), errors.Is(err, memorycontext.ErrConflict):
		e.Code,
			e.Message,
			e.Action = "version_conflict",
			"The resource changed since it was read.",
			"Read its current version, reconcile changes, and use a new idempotency key."
	case errors.Is(err, domain.ErrIdempotencyConflict), errors.Is(err, memorycontext.ErrIdempotencyConflict):
		e.Code,
			e.Message,
			e.Action = "idempotency_conflict",
			"The key was already used with different input.",
			"Use the original input for replay or a new key for a new operation."
	case errors.Is(err, domain.ErrIdempotencyInProgress), errors.Is(err, memorycontext.ErrBusy):
		e.Code, e.Message, e.Retryable = "operation_in_progress", "The same operation is still running.", true
	case errors.Is(err, domain.ErrAlreadyExists):
		e.Code,
			e.Message,
			e.Action = "already_exists",
			"The resource already exists.",
			"Read the existing resource before deciding whether to update it."
	case errors.Is(err, domain.ErrInvalidSchema), errors.Is(err, memorycontext.ErrInvalid):
		e.Code, e.Message, e.Action = "invalid_argument", clip(err.Error(), 512), "Correct the arguments and retry."
	case errors.Is(err, domain.ErrEmbeddingQuotaExceeded):
		e.Code,
			e.Message,
			e.Action = "budget_exhausted",
			"The embedding budget is exhausted.",
			"Use keyword search or wait for the budget reset."
	case errors.Is(err, domain.ErrEmbeddingSpaceMismatch):
		e.Code,
			e.Message,
			e.Action = "embedding_space_mismatch",
			"The collection uses another embedding configuration.",
			"Ask an administrator to migrate its sources to a new collection."
	case errors.Is(err, domain.ErrEmbeddingInputTooLong):
		e.Code,
			e.Message,
			e.Action = "input_too_long",
			"The embedding provider rejected the input length.",
			"Reduce the input and use a new idempotency key."
	case errors.Is(err, domain.ErrRateLimited), errors.Is(err, domain.ErrEmbeddingProviderError):
		e.Code, e.Message, e.Retryable = "temporarily_unavailable", "The service is temporarily unavailable.", true
	case errors.Is(err, domain.ErrKeywordSearchNotSupported), errors.Is(err, domain.ErrNotImplemented):
		e.Code,
			e.Message,
			e.Action = "unsupported",
			"This operation is not supported by the backend.",
			"Use another supported operation."
	}
	attachConflictVersion(err, e)
	return e
}

func toolResponse(data any, err error) *sdk.CallToolResult {
	body := envelope{OK: err == nil, Data: data}
	if err != nil {
		body.Data = nil
		body.Error = classify(err)
	}
	raw, marshalErr := json.Marshal(body)
	if marshalErr != nil {
		body = envelope{Error: classify(marshalErr)}
		raw, _ = json.Marshal(body)
	}
	return &sdk.CallToolResult{IsError: !body.OK,
		StructuredContent: json.RawMessage(raw),
		Content:           []sdk.Content{&sdk.TextContent{Text: string(raw)}}}
}

func resourceError(err error) error {
	e := classify(err)
	raw, _ := json.Marshal(e)
	code := int64(jsonrpc.CodeInvalidParams)
	if e.Code == "not_found" {
		code = -32002
	}
	if e.Code == "internal_error" {
		code = jsonrpc.CodeInternalError
	}
	return &jsonrpc.Error{Code: code, Message: e.Message, Data: raw}
}

// fits measures both representations, JSON escaping, and metadata. The reserved
// bytes cover SDK resultType/serverInfo enrichment, not HTTP/SSE framing.
func (h *Handler) fits(res sdk.Result) bool {
	return h.fitsAt(context.Background(), res)
}

type responseBudgetKey struct{}

func (h *Handler) resultBudget(ctx context.Context) int {
	if n, ok := ctx.Value(responseBudgetKey{}).(int); ok {
		return n
	}
	return h.opts.MaxResultBytes
}

func (h *Handler) requestBudget(ctx context.Context, raw []byte) context.Context {
	var a struct {
		MaxOutputBytes int `json:"max_output_bytes"`
	}
	if json.Unmarshal(raw, &a) == nil && a.MaxOutputBytes >= 4096 && a.MaxOutputBytes <= 256<<10 {
		return context.WithValue(ctx, responseBudgetKey{}, a.MaxOutputBytes)
	}
	return ctx
}

func (h *Handler) fitsAt(ctx context.Context, res sdk.Result) bool {
	raw, err := json.Marshal(res)
	return err == nil && len(raw)+h.protocolReserve <= h.resultBudget(ctx)
}

func (h *Handler) boundedTool(data any, err error) *sdk.CallToolResult {
	return h.boundedToolAt(context.Background(), data, err)
}

func (h *Handler) boundedToolAt(ctx context.Context, data any, err error) *sdk.CallToolResult {
	res := toolResponse(data, err)
	if h.fitsAt(ctx, res) {
		return res
	}
	return toolResponse(nil,
		&agentError{Code: "response_too_large",
			Message: "The result exceeds the response budget.",
			Action:  "Increase max_output_bytes, request fewer items, omit metadata, or read a smaller content page."})
}

func clip(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

func requireKey(key string) error {
	if err := domain.ValidateIdempotencyKey(key); err != nil {
		return invalid(err.Error())
	}
	return nil
}

func decode(raw []byte, dst any) error {
	if len(raw) == 0 {
		raw = []byte("{}")
	}
	if err := json.Unmarshal(raw, dst); err != nil {
		return invalid(fmt.Sprintf("Invalid arguments: %s", clip(err.Error(), 256)))
	}
	return nil
}

func attachConflictVersion(err error, e *agentError) {
	var vc *domain.VersionConflictError
	if errors.As(err, &vc) {
		e.CurrentVersion = vc.CurrentVersion
	}
	var cc *memorycontext.ConflictError
	if errors.As(err, &cc) {
		e.CurrentVersion = cc.CurrentVersion
	}
}
