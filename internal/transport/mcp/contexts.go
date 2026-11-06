//nolint:wrapcheck // The protocol boundary classifies domain errors centrally and preserves their recovery data.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"

	"github.com/kailas-cloud/vecdex/internal/domain"
	"github.com/kailas-cloud/vecdex/internal/domain/search/filter"
	"github.com/kailas-cloud/vecdex/internal/domain/search/mode"
	"github.com/kailas-cloud/vecdex/internal/domain/search/request"
	"github.com/kailas-cloud/vecdex/internal/memorycontext"
)

type contextArgs struct {
	ContextID       string               `json:"context_id"`
	Name            *string              `json:"name"`
	Collection      string               `json:"collection"`
	Generation      string               `json:"generation"`
	Search          json.RawMessage      `json:"search"`
	Pins            *[]memorycontext.Pin `json:"pins"`
	ExpiresAt       json.RawMessage      `json:"expires_at"`
	ExpectedVersion string               `json:"expected_version"`
	IdempotencyKey  string               `json:"idempotency_key"`
	Cursor          string               `json:"cursor"`
	Limit           int                  `json:"limit"`
	VerifyPins      bool                 `json:"verify_pins"`
}

func (h *Handler) contextList(ctx context.Context, raw json.RawMessage) (any, error) {
	var a contextArgs
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	p, _ := PrincipalFromContext(ctx)
	return h.boundedPage(ctx, limit(a.Limit), func(pageSize int) (map[string]any, error) {
		r, listErr := h.deps.Contexts.List(ctx, memorycontext.ListRequest{Owner: p.KeyID, Cursor: a.Cursor, Limit: pageSize})
		if listErr != nil {
			return nil, listErr
		}
		items := make([]map[string]any, 0, len(r.Contexts))
		for i := range r.Contexts {
			items = append(items, contextInfo(&r.Contexts[i]))
		}
		return map[string]any{"items": items, "next_cursor": r.NextCursor}, nil
	})
}

func contextInfo(c *memorycontext.Context) map[string]any {
	pins := c.Pins
	if pins == nil {
		pins = []memorycontext.Pin{}
	}
	return map[string]any{"context_id": c.ID,
		"version":    c.Version,
		"name":       c.Name,
		"collection": c.Collection,
		"generation": c.Generation,
		"search":     c.Search,
		"pins":       pins,
		"expires_at": c.ExpiresAt,
		"created_at": c.CreatedAt,
		"updated_at": c.UpdatedAt,
		"uri":        contextURI(c.ID)}
}

func contextMutation(r *memorycontext.MutationResult) map[string]any {
	return map[string]any{"context": contextInfo(&r.Context),
		"operation":       r.Operation,
		"replayed":        r.Replayed,
		"current_state":   r.CurrentState,
		"current_version": r.CurrentVersion}
}

func (h *Handler) contextGet(ctx context.Context, raw json.RawMessage) (any, error) {
	var a contextArgs
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	p, _ := PrincipalFromContext(ctx)
	c, err := h.deps.Contexts.Get(ctx, p.KeyID, a.ContextID)
	if err != nil {
		return nil, err
	}
	out := contextInfo(&c)
	col, err := h.deps.Collections.Inspect(ctx, c.Collection)
	if err != nil {
		return nil, err
	}
	out["collection_state"] = col.State
	out["stale"] = col.State != "active" || col.Generation != c.Generation
	if a.VerifyPins {
		states, pinErr := h.verifyPins(ctx, &c, out["stale"].(bool))
		if pinErr != nil {
			return nil, pinErr
		}
		out["pin_states"] = states
	}
	return out, nil
}

func (h *Handler) contextCreate(ctx context.Context, raw json.RawMessage) (any, error) {
	var a contextArgs
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if a.Name == nil {
		return nil, invalid("name is required")
	}
	if err := requireKey(a.IdempotencyKey); err != nil {
		return nil, err
	}
	if err := validateTuning(a.Search); err != nil {
		return nil, err
	}
	expiry, err := parseExpiry(a.ExpiresAt)
	if err != nil {
		return nil, err
	}
	p, _ := PrincipalFromContext(ctx)
	r := memorycontext.CreateRequest{Owner: p.KeyID,
		Name:           *a.Name,
		Collection:     a.Collection,
		Generation:     a.Generation,
		Search:         a.Search,
		Expiry:         expiry,
		IdempotencyKey: a.IdempotencyKey}
	if a.Pins != nil {
		r.Pins = *a.Pins
	}
	value, err := h.deps.Contexts.CreateValidated(ctx, r, func(checkCtx context.Context) error {
		col, err := h.deps.Collections.Get(checkCtx, a.Collection)
		if err != nil {
			return err
		}
		if col.Generation() != a.Generation {
			return domain.ErrCollectionGenerationMismatch
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return contextMutation(&value), nil
}

func (h *Handler) contextUpdate(ctx context.Context, raw json.RawMessage) (any, error) {
	var a contextArgs
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if err := requireKey(a.IdempotencyKey); err != nil {
		return nil, err
	}
	if err := validateTuning(a.Search); err != nil {
		return nil, err
	}
	expiry, err := parseExpiry(a.ExpiresAt)
	if err != nil {
		return nil, err
	}
	if a.Name == nil && a.Search == nil && a.Pins == nil && !expiry.Set {
		return nil, invalid("at least one context setting must be supplied")
	}
	p, _ := PrincipalFromContext(ctx)
	r := memorycontext.UpdateRequest{Owner: p.KeyID,
		ID:              a.ContextID,
		ExpectedVersion: a.ExpectedVersion,
		Name:            a.Name,
		Pins:            a.Pins,
		Expiry:          expiry,
		IdempotencyKey:  a.IdempotencyKey}
	if a.Search != nil {
		r.Search = &a.Search
	}
	value, err := h.deps.Contexts.Update(ctx, r)
	if err != nil {
		return nil, err
	}
	return contextMutation(&value), nil
}

func (h *Handler) contextDelete(ctx context.Context, raw json.RawMessage) (any, error) {
	var a contextArgs
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	if err := requireKey(a.IdempotencyKey); err != nil {
		return nil, err
	}
	p, _ := PrincipalFromContext(ctx)
	value,
		err := h.deps.Contexts.Delete(ctx,
		memorycontext.DeleteRequest{Owner: p.KeyID,
			ID:              a.ContextID,
			ExpectedVersion: a.ExpectedVersion,
			IdempotencyKey:  a.IdempotencyKey})
	if err != nil {
		return nil, err
	}
	return contextMutation(&value), nil
}

func validateTuning(raw json.RawMessage) error {
	if len(raw) == 0 {
		return nil
	}
	var t tuning
	if err := decode(raw, &t); err != nil {
		return err
	}
	f, err := t.Filters.domain()
	if err != nil {
		return invalid(err.Error())
	}
	_, err = request.New("validate", mode.Mode(t.Mode), f, t.Limit, t.MaxContentBytes, t.MinScore, false)
	if err != nil {
		return invalid(err.Error())
	}
	return nil
}

type rangeArgs struct {
	GT  *float64 `json:"gt"`
	GTE *float64 `json:"gte"`
	LT  *float64 `json:"lt"`
	LTE *float64 `json:"lte"`
}
type conditionArgs struct {
	Key   string     `json:"key"`
	Match *string    `json:"match"`
	Range *rangeArgs `json:"range"`
}
type filterArgs struct {
	Must    []conditionArgs `json:"must"`
	Should  []conditionArgs `json:"should"`
	MustNot []conditionArgs `json:"must_not"`
}

func (f filterArgs) domain() (filter.Expression, error) {
	convert := func(group []conditionArgs) ([]filter.Condition, error) {
		out := make([]filter.Condition, 0, len(group))
		for _, c := range group {
			if (c.Match == nil) == (c.Range == nil) {
				return nil, fmt.Errorf("filter %q requires exactly one of match and range: %w", c.Key, domain.ErrInvalidSchema)
			}
			var value filter.Condition
			var err error
			if c.Match != nil {
				value, err = filter.NewMatch(c.Key, *c.Match)
			} else {
				if err := validateRangeInterval(c.Range); err != nil {
					return nil, err
				}
				r, rangeErr := filter.NewRangeFilter(c.Range.GT, c.Range.GTE, c.Range.LT, c.Range.LTE)
				if rangeErr != nil {
					return nil, rangeErr
				}
				value, err = filter.NewRange(c.Key, r)
			}
			if err != nil {
				return nil, err
			}
			out = append(out, value)
		}
		return out, nil
	}
	m, err := convert(f.Must)
	if err != nil {
		return filter.Expression{}, err
	}
	s, err := convert(f.Should)
	if err != nil {
		return filter.Expression{}, err
	}
	n, err := convert(f.MustNot)
	if err != nil {
		return filter.Expression{}, err
	}
	return filter.NewExpression(m, s, n)
}

func filterSchema() map[string]any {
	rangeSchema := object(nil,
		map[string]any{"gt": map[string]any{"type": "number"},
			"gte": map[string]any{"type": "number"},
			"lt":  map[string]any{"type": "number"},
			"lte": map[string]any{"type": "number"}})
	condition := object([]string{"key"},
		map[string]any{"key": str("Declared indexed metadata field."),
			"match": str("Exact tag match."),
			"range": rangeSchema})
	group := map[string]any{"type": "array", "maxItems": 32, "items": condition}
	return object(nil, map[string]any{"must": group, "should": group, "must_not": group})
}

func (h *Handler) verifyPins(ctx context.Context, c *memorycontext.Context, stale bool) ([]map[string]string, error) {
	states := make([]map[string]string, 0, len(c.Pins))
	ctx = domain.WithExpectedCollectionGeneration(ctx, c.Generation)
	for _, pin := range c.Pins {
		state := map[string]string{"document_id": pin.DocumentID, "version": pin.Version, "state": "stale"}
		if !stale {
			doc, err := h.deps.Documents.ReadSource(ctx, c.Collection, pin.DocumentID, domdoc.SourceProjection{})
			switch {
			case errors.Is(err, domain.ErrNotFound), errors.Is(err, domain.ErrDocumentNotFound):
				state["state"] = "missing"
			case err != nil:
				return nil, err
			default:
				state["current_version"] = doc.Version()
				if doc.Version() == pin.Version {
					state["state"] = "current"
				}
			}
		}
		states = append(states, state)
	}
	return states, nil
}

func validateRangeInterval(r *rangeArgs) error {
	lower, upper := r.GTE, r.LTE
	if r.GT != nil {
		lower = r.GT
	}
	if r.LT != nil {
		upper = r.LT
	}
	if lower != nil && upper != nil {
		if *lower > *upper || (*lower == *upper && (r.GT != nil || r.LT != nil)) {
			return invalid("range lower bound must precede upper bound; equal bounds must be inclusive")
		}
	}
	return nil
}
