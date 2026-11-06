//nolint:lll // Declarative JSON schemas keep each small wire shape together.
package mcp

import "sort"

// ToolContract is a deterministic, transport-independent copy of a tool's wire schema.
type ToolContract struct {
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Role         string         `json:"role"`
	InputSchema  map[string]any `json:"input_schema"`
	OutputSchema map[string]any `json:"output_schema"`
}

// Contracts returns the complete catalog in stable order for documentation and fixtures.
func Contracts() []ToolContract {
	h := &Handler{}
	out := make([]ToolContract, 0, 17)
	for _, d := range h.toolDefinitions() {
		d.schema["properties"].(map[string]any)["max_output_bytes"] = integer(4096, 256<<10)
		out = append(out, ToolContract{d.name, d.description, toolRoles[d.name], d.schema, toolOutputSchema(d.name)})
	}
	return out
}

func scalar(kind string) map[string]any                              { return map[string]any{"type": kind} }
func arrayOf(item any) map[string]any                                { return map[string]any{"type": "array", "items": item} }
func fields(required []string, values map[string]any) map[string]any { return object(required, values) }
func stringFields(names ...string) map[string]any {
	out := map[string]any{}
	for _, n := range names {
		out[n] = scalar("string")
	}
	return out
}
func documentSchema() map[string]any {
	return fields([]string{"id", "version", "uri", "expires_at"}, props(stringFields("id", "version", "uri"), map[string]any{
		"expires_at": scalar("integer"), "metadata": scalar("object"), "content": scalar("string"), "offset": scalar("integer"),
		"total_bytes": scalar("integer"), "complete": scalar("boolean"), "next_offset": scalar("integer"),
	}))
}
func collectionSchema() map[string]any {
	return fields([]string{"name", "generation", "type", "fields", "uri", "created_at"}, props(
		stringFields("name", "generation", "type", "uri"), map[string]any{
			"created_at": scalar("integer"), "fields": arrayOf(fields([]string{"name", "type"}, stringFields("name", "type"))), "state": scalar("string"),
		}))
}
func contextSchema() map[string]any {
	return fields([]string{"context_id", "version", "name", "collection", "generation", "search", "pins", "expires_at", "created_at", "updated_at", "uri"}, props(
		stringFields("context_id", "version", "name", "collection", "generation", "created_at", "updated_at", "uri", "collection_state"), map[string]any{
			"search": map[string]any{"type": []string{"object", "null"}}, "pins": arrayOf(fields([]string{"document_id", "version"}, stringFields("document_id", "version"))),
			"expires_at": map[string]any{"type": []string{"string", "null"}}, "stale": scalar("boolean"),
			"pin_states": arrayOf(fields([]string{"document_id", "version", "state"}, stringFields("document_id", "version", "state", "current_version"))),
		}))
}
func pageSchema(item any, context bool) map[string]any {
	p := map[string]any{"items": arrayOf(item), "next_cursor": scalar("string"), "truncated": scalar("boolean")}
	if context {
		p["context_id"] = scalar("string")
	}
	required := []string{"items", "next_cursor", "truncated"}
	if context {
		required = append(required, "context_id")
	}
	return fields(required, p)
}
func toolOutputSchema(name string) map[string]any {
	data := toolDataSchema(name)
	errorSchema := fields([]string{"code", "message", "retryable", "action"}, props(stringFields("code", "message", "action", "current_version"), map[string]any{"retryable": scalar("boolean")}))
	out := fields([]string{"ok"}, map[string]any{"ok": scalar("boolean"), "data": data, "error": errorSchema})
	out["oneOf"] = []any{
		map[string]any{"properties": map[string]any{"ok": map[string]any{"const": true}}, "required": []string{"data"}, "not": map[string]any{"required": []string{"error"}}},
		map[string]any{"properties": map[string]any{"ok": map[string]any{"const": false}}, "required": []string{"error"}, "not": map[string]any{"required": []string{"data"}}},
	}
	return out
}

//nolint:cyclop // The switch is the exhaustive public result catalog.
func toolDataSchema(name string) map[string]any {
	switch name {
	case "memory_read":
		return documentSchema()
	case "memory_list":
		return pageSchema(documentSchema(), true)
	case "collection_list":
		return pageSchema(collectionSchema(), false)
	case "context_list":
		return pageSchema(contextSchema(), false)
	case "memory_search", "memory_similar":
		p := pageSchema(fields([]string{"id", "version", "score", "content", "uri", "content_truncated"}, props(stringFields("id", "version", "content", "uri"), map[string]any{"score": scalar("number"), "content_truncated": scalar("boolean")})), true)
		delete(p["properties"].(map[string]any), "next_cursor")
		p["required"] = []string{"items", "truncated", "context_id", "total_candidates", "next_action"}
		p["properties"].(map[string]any)["total_candidates"] = scalar("integer")
		p["properties"].(map[string]any)["next_action"] = scalar("string")
		return p
	case "memory_create", "memory_update":
		p := props(stringFields("id", "version", "uri"), map[string]any{"replayed": scalar("boolean")})
		if name == "memory_create" {
			p["created"] = scalar("boolean")
		}
		required := []string{"id", "version", "uri", "replayed"}
		if name == "memory_create" {
			required = append(required, "created")
		}
		return fields(required, p)
	case "memory_delete":
		return fields([]string{"id", "deleted", "deleted_version", "uri", "replayed"}, props(stringFields("id", "deleted_version", "uri"), map[string]any{"deleted": scalar("boolean"), "replayed": scalar("boolean")}))
	case "collection_create":
		return requireExtra(collectionSchema(), "state")
	case "collection_get":
		return fields([]string{"name", "generation", "state"}, props(stringFields("name", "generation", "state"), map[string]any{"collection": collectionSchema()}))
	case "collection_delete":
		return fields([]string{"name", "generation", "state", "deleted"}, props(stringFields("name", "generation", "state"), map[string]any{"deleted": scalar("boolean")}))
	case "context_get":
		return requireExtra(contextSchema(), "collection_state", "stale")
	case "context_create", "context_update", "context_delete":
		return fields([]string{"context", "operation", "replayed", "current_state", "current_version"}, props(stringFields("operation", "current_state", "current_version"), map[string]any{"context": contextSchema(), "replayed": scalar("boolean")}))
	case "memory_status":
		return statusSchema()
	default:
		panic("missing output schema: " + name)
	}
}
func statusSchema() map[string]any {
	return fields([]string{"protocol_versions", "max_result_bytes", "max_request_bytes", "max_subscriptions", "subscription_poll_ms", "cache_scope", "capabilities", "limits"}, map[string]any{
		"protocol_versions": arrayOf(scalar("string")), "max_result_bytes": scalar("integer"), "max_request_bytes": scalar("integer"),
		"max_subscriptions": scalar("integer"), "subscription_poll_ms": scalar("integer"), "cache_scope": scalar("string"),
		"status": scalar("string"), "checks": scalar("object"), "context": contextSchema(),
		"capabilities": capabilitiesSchema(), "limits": limitsSchema(),
		"embedding_usage": fields([]string{"period", "tokens", "requests", "unknown_requests", "budget_limit", "budget_remaining", "exhausted", "resets_at"}, map[string]any{
			"period": scalar("string"), "tokens": scalar("integer"), "requests": scalar("integer"), "unknown_requests": scalar("integer"),
			"budget_limit": scalar("integer"), "budget_remaining": scalar("integer"), "exhausted": scalar("boolean"), "resets_at": scalar("integer"),
		}),
	})
}

func typedFields(kind string, names ...string) map[string]any {
	out := map[string]any{}
	for _, name := range names {
		out[name] = scalar(kind)
	}
	return out
}
func completeObject(properties map[string]any) map[string]any {
	required := make([]string, 0, len(properties))
	for key := range properties {
		required = append(required, key)
	}
	sort.Strings(required)
	return object(required, properties)
}
func capabilitiesSchema() map[string]any {
	return completeObject(props(stringFields("role", "skills_protocol", "subscriptions_protocol"),
		map[string]any{"search_modes": arrayOf(scalar("string"))},
		typedFields("boolean", "create_only", "source_version_cas", "json_merge_patch", "source_projections",
			"source_mutations", "collection_mutations", "durable_contexts", "owned_context_management", "advisory_pins",
			"prompts", "completions", "progress", "cancellation", "history", "search_visibility_barrier", "snapshot_pagination",
			"rate_limiting", "tasks", "apps", "elicitation", "sampling")))
}
func limitsSchema() map[string]any {
	return completeObject(typedFields("integer", "content_max_bytes", "metadata_max_bytes", "context_max_bytes", "context_pins_max",
		"context_default_ttl_ms", "context_idempotency_ttl_ms", "source_idempotency_ttl_ms", "search_default_limit", "list_default_limit",
		"max_items", "output_default_bytes", "output_max_bytes", "read_default_content_bytes", "operation_deadline_ms"))
}

func requireExtra(schema map[string]any, names ...string) map[string]any {
	schema["required"] = append(schema["required"].([]string), names...)
	return schema
}
