package mcp

import "context"

func (h *Handler) boundedPage(ctx context.Context, n int, fetch func(int) (map[string]any, error)) (any, error) {
	original := n
	for {
		page, err := fetch(n)
		if err != nil {
			return nil, err
		}
		page["truncated"] = n < original
		if h.fitsAt(ctx, toolResponse(page, nil)) {
			return page, nil
		}
		if n == 1 {
			return nil,
				&agentError{Code: "response_too_large",
					Message: "One item exceeds the response budget.",
					Action:  "Omit metadata, select metadata_fields, or increase max_output_bytes."}
		}
		n = max(1, n/2)
	}
}
