package mcp

import (
	"context"
	"time"

	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
	"github.com/kailas-cloud/vecdex/internal/domain/metadata"
	"github.com/kailas-cloud/vecdex/internal/domain/search/request"
	"github.com/kailas-cloud/vecdex/internal/memorycontext"
)

func applicationCapabilities(ctx context.Context) map[string]any {
	principal, _ := PrincipalFromContext(ctx)
	return map[string]any{
		"role": principal.Role, "search_modes": []string{"semantic", "keyword", "hybrid"},
		"create_only": true, "source_version_cas": true, "json_merge_patch": true, "source_projections": true,
		"source_mutations": allows(principal.Role, "write"), "collection_mutations": allows(principal.Role, "admin"),
		"durable_contexts": true, "owned_context_management": true, "advisory_pins": true,
		"prompts": true, "completions": true, "progress": true, "cancellation": true,
		"skills_protocol": latestVersion, "subscriptions_protocol": latestVersion,
		"history": false, "search_visibility_barrier": false, "snapshot_pagination": false, "rate_limiting": false,
		"tasks": false, "apps": false, "elicitation": false, "sampling": false,
	}
}
func applicationLimits(defaultOutputBytes, currentOutputBytes int) map[string]any {
	return map[string]any{
		"content_max_bytes": domdoc.MaxContentSize, "metadata_max_bytes": metadata.MaxSize,
		"context_max_bytes": memorycontext.MaxPayloadBytes, "context_pins_max": memorycontext.MaxPins,
		"context_default_ttl_ms":     memorycontext.DefaultLifetime.Milliseconds(),
		"context_idempotency_ttl_ms": memorycontext.ReceiptLifetime.Milliseconds(),
		"source_idempotency_ttl_ms":  (24 * time.Hour).Milliseconds(),
		"search_default_limit":       5, "list_default_limit": 20, "max_items": request.MaxLimit,
		"output_default_bytes": defaultOutputBytes, "output_max_bytes": 256 << 10,
		"read_default_content_bytes": min(16<<10, currentOutputBytes/4),
		"operation_deadline_ms":      (120 * time.Second).Milliseconds(),
	}
}
