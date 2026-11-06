package mcp

import (
	"context"
	"encoding/json"
	"time"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func (h *Handler) observe(ctx context.Context, event Event) {
	if h.opts.Observer != nil {
		h.opts.Observer(ctx, event)
	}
}

func (h *Handler) observeRequest(ctx context.Context,
	method string,
	req sdk.Request,
	res sdk.Result,
	err error,
	start time.Time) {
	event := Event{Method: observedMethod(method), Outcome: "ok", Duration: time.Since(start)}
	if params, ok := req.GetParams().(*sdk.CallToolParamsRaw); ok {
		if _, known := toolRoles[params.Name]; known {
			event.Tool = params.Name
		} else {
			event.Tool = "unknown"
		}
	}
	if err != nil {
		event.Outcome = "protocol_error"
	}
	if res != nil {
		event.ResponseBytes = len(jsonBytes(res))
	}
	if tool, ok := res.(*sdk.CallToolResult); ok {
		var body struct {
			OK    bool        `json:"ok"`
			Error *agentError `json:"error"`
			Data  struct {
				Replayed  bool `json:"replayed"`
				Truncated bool `json:"truncated"`
			} `json:"data"`
		}
		if json.Unmarshal(jsonBytes(tool.StructuredContent), &body) == nil {
			event.Replayed, event.Truncated = body.Data.Replayed, body.Data.Truncated
			if body.Error != nil {
				event.Outcome = body.Error.Code
				event.Conflict = body.Error.Code == "version_conflict" || body.Error.Code == "idempotency_conflict"
			}
		}
	}
	h.observe(ctx, event)
}

func observedMethod(method string) string {
	switch method {
	case "initialize",
		"server/discover",
		"tools/list",
		"tools/call",
		"resources/list",
		"resources/templates/list",
		"resources/read",
		"resources/subscribe",
		"resources/unsubscribe",
		"prompts/list",
		"prompts/get",
		"completion/complete",
		"subscriptions/listen",
		"skills/list",
		"skills/get",
		"ping",
		"notifications/initialized":
		return method
	default:
		return "unknown"
	}
}
