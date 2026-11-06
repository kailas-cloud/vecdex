//nolint:wrapcheck // The protocol boundary classifies domain errors centrally and preserves their recovery data.
package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/kailas-cloud/vecdex/internal/domain"
	domcol "github.com/kailas-cloud/vecdex/internal/domain/collection"
	"github.com/kailas-cloud/vecdex/internal/domain/collection/field"
	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/document/patch"
	"github.com/kailas-cloud/vecdex/internal/domain/metadata"
	domusage "github.com/kailas-cloud/vecdex/internal/domain/usage"
	"github.com/kailas-cloud/vecdex/internal/memorycontext"
	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (h *Handler) registerTools(s *sdk.Server, role string) error {
	definitions := h.toolDefinitions()
	for _, def := range definitions {
		output := toolOutputSchema(def.name)
		def.schema["properties"].(map[string]any)["max_output_bytes"] = integer(4096, 256<<10)
		if !allows(role, toolRoles[def.name]) {
			continue
		}
		var schema jsonschema.Schema
		if err := json.Unmarshal(jsonBytes(def.schema), &schema); err != nil {
			return err
		}
		resolved, err := schema.Resolve(nil)
		if err != nil {
			return fmt.Errorf("%s schema: %w", def.name, err)
		}
		readOnly := strings.HasSuffix(def.name,
			"_search") || strings.HasSuffix(def.name,
			"_read") || strings.HasSuffix(def.name,
			"_list") || strings.HasSuffix(def.name,
			"_get") || def.name == "memory_similar" || def.name == "memory_status"
		destructive := strings.HasSuffix(def.name, "_delete") || def.name == "memory_update"
		closed := false
		s.AddTool(&sdk.Tool{Name: def.name,
			Description:  def.description,
			InputSchema:  def.schema,
			OutputSchema: output,
			Annotations: &sdk.ToolAnnotations{ReadOnlyHint: readOnly,
				DestructiveHint: &destructive,
				OpenWorldHint:   &closed,
				IdempotentHint: strings.HasPrefix(def.name,
					"memory_") && !readOnly || strings.HasPrefix(def.name,
					"context_") && !readOnly}},
			h.toolHandler(def, resolved))
	}
	return nil
}

func sourceProperties(update bool) map[string]any {
	metadataSchema := map[string]any{"type": "object",
		"description": "Arbitrary source metadata; only declared collection fields are indexed."}
	if update {
		metadataSchema["type"] = []string{"object", "null"}
	}
	return map[string]any{"content": map[string]any{"type": "string",
		"minLength": 1,
		"maxLength": domdoc.MaxContentSize},
		"metadata": metadataSchema,
		"expires_at": map[string]any{"type": []string{"integer",
			"null"},
			"minimum":     0,
			"maximum":     domdoc.MaxExpiresAt,
			"description": "UTC Unix milliseconds. Null or zero clears expiry; omission preserves it."}}
}

type scopeArgs struct {
	ContextID string `json:"context_id"`
}
type sourceArgs struct {
	ContextID       string          `json:"context_id"`
	ID              string          `json:"id"`
	Content         *string         `json:"content"`
	Metadata        json.RawMessage `json:"metadata"`
	ExpiresAt       json.RawMessage `json:"expires_at"`
	IfVersion       string          `json:"if_version"`
	IdempotencyKey  string          `json:"idempotency_key"`
	Offset          int             `json:"offset"`
	MaxBytes        int             `json:"max_bytes"`
	Version         string          `json:"version"`
	IncludeMetadata bool            `json:"include_metadata"`
	IncludeContent  *bool           `json:"include_content"`
	MetadataFields  []string        `json:"metadata_fields"`
	Cursor          string          `json:"cursor"`
	Limit           int             `json:"limit"`
}

func (h *Handler) scope(ctx context.Context,
	id string) (scoped context.Context,
	saved memorycontext.Context,
	failure error) {
	if err := requiredString("context_id", id); err != nil {
		return ctx, memorycontext.Context{}, err
	}
	p, _ := PrincipalFromContext(ctx)
	c, err := h.deps.Contexts.Get(ctx, p.KeyID, id)
	if err != nil {
		return ctx, c, err
	}
	col, err := h.deps.Collections.Get(ctx, c.Collection)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return ctx, c, domain.ErrCollectionGenerationMismatch
		}
		return ctx, c, err
	}
	if col.Generation() != c.Generation {
		return ctx, c, domain.ErrCollectionGenerationMismatch
	}
	return domain.WithExpectedCollectionGeneration(ctx, c.Generation), c, nil
}

func (h *Handler) read(ctx context.Context, raw json.RawMessage) (any, error) {
	var a sourceArgs
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	ctx, c, err := h.scope(ctx, a.ContextID)
	if err != nil {
		return nil, err
	}
	d,
		err := h.deps.Documents.ReadSource(ctx,
		c.Collection,
		a.ID,
		domdoc.SourceProjection{Content: a.IncludeContent == nil || *a.IncludeContent,
			Metadata: a.IncludeMetadata || len(a.MetadataFields) > 0})
	if err != nil {
		return nil, err
	}
	return h.documentPage(ctx, &d, c.Collection, c.Generation, &a)
}

func (h *Handler) documentPage(ctx context.Context,
	d *domdoc.Document,
	collection,
	generation string,
	a *sourceArgs) (any,
	error) {
	if a.Version != "" && a.Version != d.Version() {
		return nil, domain.NewVersionConflict(d.Version())
	}
	out, err := projectedDocument(d, collection, generation, a)
	if err != nil {
		return nil, err
	}
	if a.IncludeContent != nil && !*a.IncludeContent {
		if a.Offset != 0 {
			return nil, invalid("metadata-only reads cannot have a content offset")
		}
		return out, nil
	}
	if err := validateReadOffset(d, a); err != nil {
		return nil, err
	}
	pageBytes := a.MaxBytes
	if pageBytes <= 0 {
		pageBytes = 16 << 10
	}
	pageBytes = min(pageBytes, h.resultBudget(ctx)/4)
	for {
		text := clip(d.Content()[a.Offset:], pageBytes)
		if text == "" && a.Offset < len(d.Content()) {
			return nil, invalid("max_bytes must fit the next UTF-8 rune")
		}
		next := a.Offset + len(text)
		out["content"], out["offset"] = text, a.Offset
		out["total_bytes"], out["complete"] = len(d.Content()), next == len(d.Content())
		delete(out, "next_offset")
		if next < len(d.Content()) {
			out["next_offset"] = next
		}
		if h.fitsAt(ctx, toolResponse(out, nil)) {
			return out, nil
		}
		if pageBytes <= 4 {
			return nil,
				&agentError{Code: "response_too_large",
					Message: "Source metadata exceeds the response budget.",
					Action:  "Select metadata_fields, omit metadata, or increase max_output_bytes."}
		}
		pageBytes = max(4, pageBytes/2)
	}
}
func validateReadOffset(d *domdoc.Document, a *sourceArgs) error {
	if a.Offset > 0 && a.Version == "" {
		return invalid("version is required when continuing a read page")
	}
	if a.Offset < 0 || a.Offset > len(d.Content()) {
		return invalid("offset is outside the source content")
	}
	if a.Offset < len(d.Content()) && !utf8.RuneStart(d.Content()[a.Offset]) {
		return invalid("offset must be a UTF-8 boundary")
	}
	return nil
}

func documentInfo(d *domdoc.Document, collection, generation string, includeMetadata bool) map[string]any {
	out := map[string]any{"id": d.ID(),
		"version": d.Version(),
		"uri": documentURI(collection,
			generation,
			d.ID()),
		"expires_at": d.ExpiresAt()}
	if includeMetadata {
		out["metadata"] = json.RawMessage(d.Metadata().JSON())
	}
	return out
}

func (h *Handler) list(ctx context.Context, raw json.RawMessage) (any, error) {
	var a sourceArgs
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	ctx, c, err := h.scope(ctx, a.ContextID)
	if err != nil {
		return nil, err
	}
	return h.boundedPage(ctx, limit(a.Limit), func(pageSize int) (map[string]any, error) {
		docs,
			next,
			listErr := h.deps.Documents.ListSources(ctx,
			c.Collection,
			a.Cursor,
			pageSize,
			domdoc.SourceProjection{Metadata: a.IncludeMetadata})
		if listErr != nil {
			return nil, listErr
		}
		items := make([]map[string]any, 0, len(docs))
		for i := range docs {
			items = append(items, documentInfo(&docs[i], c.Collection, c.Generation, a.IncludeMetadata))
		}
		return map[string]any{"items": items, "next_cursor": next, "context_id": c.ID}, nil
	})
}

func (h *Handler) create(ctx context.Context, raw json.RawMessage) (any, error) {
	var a sourceArgs
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if a.Content == nil {
		return nil, invalid("content is required")
	}
	ctx, c, err := h.scope(ctx, a.ContextID)
	if err != nil {
		return nil, err
	}
	ctx, err = writeContext(ctx, &a, false)
	if err != nil {
		return nil, err
	}
	d, err := domdoc.New(a.ID, *a.Content, nil, nil)
	if err != nil {
		return nil, invalid(err.Error())
	}
	if a.Metadata != nil {
		value, err := metadata.Parse(a.Metadata)
		if err != nil {
			return nil, err
		}
		d.SetMetadata(value)
	}
	expiry, err := parseSourceExpiry(a.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if expiry != nil {
		if err := d.SetExpiresAt(*expiry); err != nil {
			return nil, err
		}
	}
	ctx, outcome := domain.WithWriteOutcome(ctx)
	created, err := h.deps.Documents.Upsert(domain.WithRequireAbsent(ctx, true), c.Collection, &d)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": d.ID(),
			"version":  d.Version(),
			"created":  created,
			"replayed": outcome.Replayed(),
			"uri": documentURI(c.Collection,
				c.Generation,
				d.ID())},
		nil
}

func (h *Handler) update(ctx context.Context, raw json.RawMessage) (any, error) {
	var a sourceArgs
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	ctx, c, err := h.scope(ctx, a.ContextID)
	if err != nil {
		return nil, err
	}
	ctx, err = writeContext(ctx, &a, true)
	if err != nil {
		return nil, err
	}
	expiry, err := parseSourceExpiry(a.ExpiresAt)
	if err != nil {
		return nil, err
	}
	p, err := patch.NewSource(a.Content, a.Metadata, expiry)
	if err != nil {
		return nil, invalid(err.Error())
	}
	ctx, outcome := domain.WithWriteOutcome(ctx)
	r, err := h.deps.Documents.Patch(ctx, c.Collection, a.ID, p)
	if err != nil {
		return nil, err
	}
	return map[string]any{"id": r.ID,
			"version":  r.Version,
			"replayed": outcome.Replayed(),
			"uri": documentURI(c.Collection,
				c.Generation,
				r.ID)},
		nil
}

func (h *Handler) delete(ctx context.Context, raw json.RawMessage) (any, error) {
	var a sourceArgs
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	ctx, c, err := h.scope(ctx, a.ContextID)
	if err != nil {
		return nil, err
	}
	ctx, err = writeContext(ctx, &a, true)
	if err != nil {
		return nil, err
	}
	ctx, outcome := domain.WithWriteOutcome(ctx)
	if err := h.deps.Documents.Delete(ctx, c.Collection, a.ID); err != nil {
		return nil, err
	}
	return map[string]any{"id": a.ID,
			"deleted":         true,
			"deleted_version": a.IfVersion,
			"replayed":        outcome.Replayed(),
			"uri": documentURI(c.Collection,
				c.Generation,
				a.ID)},
		nil
}

func writeContext(ctx context.Context, a *sourceArgs, conditional bool) (context.Context, error) {
	if err := requireKey(a.IdempotencyKey); err != nil {
		return ctx, err
	}
	p, _ := PrincipalFromContext(ctx)
	digest := sha256.Sum256([]byte(p.KeyID + "\x00" + a.IdempotencyKey))
	ctx = domain.WithIdempotencyKey(ctx, hex.EncodeToString(digest[:]))
	if conditional {
		if err := requiredString("if_version", a.IfVersion); err != nil {
			return ctx, err
		}
		if err := domain.ValidateExpectedVersion(&a.IfVersion); err != nil {
			return ctx, err
		}
		ctx = domain.WithExpectedVersion(ctx, &a.IfVersion)
	}
	return ctx, nil
}

func limit(n int) int {
	if n <= 0 {
		return 20
	}
	return min(n, 100)
}

func (h *Handler) collectionList(ctx context.Context, raw json.RawMessage) (any, error) {
	var a sourceArgs
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	return h.boundedPage(ctx, limit(a.Limit), func(pageSize int) (map[string]any, error) {
		cols, next, listErr := h.deps.Collections.ListPage(ctx, a.Cursor, pageSize)
		if listErr != nil {
			return nil, listErr
		}
		items := make([]map[string]any, 0, len(cols))
		for _, c := range cols {
			items = append(items, collectionInfo(c))
		}
		return map[string]any{"items": items, "next_cursor": next}, nil
	})
}
func collectionInfo(c domcol.Collection) map[string]any {
	fields := make([]map[string]string, 0, len(c.Fields()))
	for _, f := range c.Fields() {
		fields = append(fields, map[string]string{"name": f.Name(), "type": string(f.FieldType())})
	}
	return map[string]any{"name": c.Name(),
		"generation": c.Generation(),
		"type":       c.Type(),
		"fields":     fields,
		"uri": collectionURI(c.Name(),
			c.Generation()),
		"created_at": c.CreatedAt()}
}
func (h *Handler) collectionGet(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Name string `json:"name"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	r, err := h.deps.Collections.Inspect(ctx, a.Name)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"name": r.Name, "generation": r.Generation, "state": r.State}
	if r.Collection != nil {
		out["collection"] = collectionInfo(*r.Collection)
	}
	return out, nil
}
func (h *Handler) collectionCreate(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Name   string `json:"name"`
		Fields []struct {
			Name string `json:"name"`
			Type string `json:"type"`
		} `json:"fields"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	fields := make([]field.Field, 0, len(a.Fields))
	for _, f := range a.Fields {
		v, err := field.New(f.Name, field.Type(f.Type))
		if err != nil {
			return nil, invalid(err.Error())
		}
		fields = append(fields, v)
	}
	c, err := h.deps.Collections.Create(ctx, a.Name, domcol.TypeText, fields)
	if err != nil {
		return nil, err
	}
	out := collectionInfo(c)
	out["state"] = "active"
	return out, nil
}
func (h *Handler) collectionDelete(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Name        string `json:"name"`
		Generation  string `json:"generation"`
		ConfirmName string `json:"confirm_name"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if a.Name != a.ConfirmName {
		return nil, invalid("confirm_name must exactly match name")
	}
	if err := h.deps.Collections.DeleteIfGeneration(ctx, a.Name, a.Generation); err != nil {
		return nil, err
	}
	inspection, err := h.deps.Collections.Inspect(ctx, a.Name)
	if err != nil {
		return nil, err
	}
	return map[string]any{"name": a.Name,
			"generation": a.Generation,
			"deleted":    inspection.State == "absent",
			"state":      inspection.State},
		nil
}

func (h *Handler) status(ctx context.Context, raw json.RawMessage) (any, error) {
	var a scopeArgs
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	out := map[string]any{"protocol_versions": []string{latestVersion,
		legacyVersion},
		"max_result_bytes":     h.resultBudget(ctx),
		"max_request_bytes":    h.opts.MaxRequestBytes,
		"max_subscriptions":    256,
		"subscription_poll_ms": h.opts.PollInterval.Milliseconds(),
		"cache_scope":          "private",
		"capabilities":         applicationCapabilities(ctx),
		"limits":               applicationLimits(h.opts.MaxResultBytes, h.resultBudget(ctx))}
	if h.deps.Health != nil {
		r := h.deps.Health.Check(ctx)
		out["status"], out["checks"] = r.Status, r.Checks
	}
	if h.deps.Usage != nil {
		r, err := h.deps.Usage.GetReport(ctx, domusage.PeriodMonth)
		if err != nil {
			return nil, err
		}
		m, b := r.Metrics(), r.Budget()
		out["embedding_usage"] = map[string]any{"period": r.Period(),
			"tokens":           m.Tokens(),
			"requests":         m.EmbeddingRequests(),
			"unknown_requests": m.UnknownRequests(),
			"budget_limit":     b.TokensLimit(),
			"budget_remaining": b.TokensRemaining(),
			"exhausted":        b.IsExhausted(),
			"resets_at":        b.ResetsAt()}
	}
	if a.ContextID != "" {
		c, err := h.contextGet(ctx, raw)
		if err != nil {
			return nil, err
		}
		out["context"] = c
	}
	return out, nil
}

func parseExpiry(raw json.RawMessage) (memorycontext.Expiry, error) {
	if raw == nil {
		return memorycontext.Expiry{}, nil
	}
	if string(raw) == "null" {
		return memorycontext.Expiry{Set: true}, nil
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return memorycontext.Expiry{}, invalid("expires_at must be an RFC3339 timestamp or null")
	}
	t, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return memorycontext.Expiry{}, invalid("expires_at must be an RFC3339 timestamp")
	}
	return memorycontext.Expiry{Set: true, At: &t}, nil
}

func (h *Handler) toolHandler(def toolDef, resolved *jsonschema.Resolved) sdk.ToolHandler {
	return func(ctx context.Context, req *sdk.CallToolRequest) (*sdk.CallToolResult, error) {
		var args map[string]any
		if err := decode(req.Params.Arguments, &args); err != nil {
			return h.boundedToolAt(ctx, nil, err), nil
		}
		if args == nil {
			args = map[string]any{}
		}
		if err := resolved.Validate(args); err != nil {
			return h.boundedToolAt(ctx, nil, invalid(clip(err.Error(), 512))), nil
		}
		h.progress(ctx, req, 0, "Working")
		data, err := def.call(ctx, req.Params.Arguments)
		h.progress(ctx, req, 1, "Finished")
		return h.boundedToolAt(ctx, data, err), nil
	}
}
