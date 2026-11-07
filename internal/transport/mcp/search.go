//nolint:wrapcheck // The protocol boundary classifies domain errors centrally and preserves their recovery data.
package mcp

import (
	"context"
	"encoding/json"

	"github.com/kailas-cloud/vecdex/internal/domain/search/mode"
	"github.com/kailas-cloud/vecdex/internal/domain/search/request"
	"github.com/kailas-cloud/vecdex/internal/domain/search/result"
)

type tuning struct {
	Mode            string     `json:"mode"`
	Limit           int        `json:"limit"`
	MaxContentBytes int        `json:"max_content_bytes"`
	MinScore        float64    `json:"min_score"`
	Filters         filterArgs `json:"filters"`
}

func (h *Handler) search(ctx context.Context, raw json.RawMessage) (any, error) {
	return h.retrieve(ctx, raw, false)
}
func (h *Handler) similar(ctx context.Context, raw json.RawMessage) (any, error) {
	return h.retrieve(ctx, raw, true)
}
func (h *Handler) retrieve(ctx context.Context, raw json.RawMessage, similar bool) (any, error) {
	var a struct {
		ContextID string `json:"context_id"`
		Query     string `json:"query"`
		ID        string `json:"id"`
	}
	if err := decode(raw, &a); err != nil {
		return nil, err
	}
	ctx, c, err := h.scope(ctx, a.ContextID)
	if err != nil {
		return nil, err
	}
	t, err := mergeTuning(c.Search, raw)
	if err != nil {
		return nil, err
	}
	f, err := t.Filters.domain()
	if err != nil {
		return nil, invalid(err.Error())
	}
	if t.MaxContentBytes == 0 || t.MaxContentBytes > h.resultBudget(ctx)/4 {
		t.MaxContentBytes = h.resultBudget(ctx) / 4
	}
	var hits []result.Result
	var total int
	if similar {
		r, validationErr := request.NewSimilar(f, t.Limit, t.MaxContentBytes, t.MinScore, false)
		if validationErr != nil {
			return nil, invalid(validationErr.Error())
		}
		hits, total, err = h.deps.Search.Similar(ctx, c.Collection, a.ID, &r)
	} else {
		r, validationErr := request.New(a.Query, mode.Mode(t.Mode), f, t.Limit, t.MaxContentBytes, t.MinScore, false)
		if validationErr != nil {
			return nil, invalid(validationErr.Error())
		}
		hits, total, err = h.deps.Search.Search(ctx, c.Collection, &r)
	}
	if err != nil {
		return nil, err
	}
	return h.searchPage(ctx, hits, total, c.ID, c.Collection, c.Generation)
}

func (h *Handler) searchPage(ctx context.Context, hits []result.Result, total int, id, col, gen string) (any, error) {
	items := make([]map[string]any, 0, len(hits))
	out := map[string]any{"items": items,
		"total_candidates": total,
		"truncated":        false,
		"context_id":       id,
		"next_action": "If truncated, reduce max_content_bytes or increase max_output_bytes. Read full sources " +
			"with memory_read."}
	for i := 0; i < len(hits); i++ {
		r := &hits[i]
		items = append(items,
			map[string]any{"id": r.ID(),
				"version":           r.Version(),
				"score":             r.Score(),
				"content":           r.Content(),
				"content_truncated": r.ContentTruncated(),
				"uri": documentURI(col,
					gen,
					r.ID())})
		out["items"] = items
		if !h.fitsAt(ctx, toolResponse(out, nil)) {
			items = items[:len(items)-1]
			out["items"] = items
			out["truncated"] = true
			if len(items) == 0 {
				return nil,
					&agentError{Code: "response_too_large",
						Message: "One search hit exceeds the response budget.",
						Action:  "Reduce max_content_bytes or increase max_output_bytes."}
			}
			return out, nil
		}
	}
	return out, nil
}

func mergeTuning(defaults, raw json.RawMessage) (tuning, error) {
	settings := map[string]json.RawMessage{}
	if len(defaults) > 0 {
		if err := json.Unmarshal(defaults, &settings); err != nil {
			return tuning{}, err
		}
	}
	var overrides map[string]json.RawMessage
	_ = json.Unmarshal(raw, &overrides)
	for k, v := range overrides {
		if k != "context_id" && k != "query" && k != "id" && k != "max_output_bytes" {
			settings[k] = v
		}
	}
	var t tuning
	if err := decode(jsonBytes(settings), &t); err != nil {
		return tuning{}, err
	}
	if t.Limit == 0 {
		t.Limit = 5
	}

	return t, nil
}
