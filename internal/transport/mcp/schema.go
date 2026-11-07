package mcp

import (
	"context"
	"encoding/json"

	domdoc "github.com/kailas-cloud/vecdex/internal/domain/document"
)

var toolRoles = map[string]string{
	"memory_search": "read", "memory_read": "read", "memory_list": "read", "memory_similar": "read",
	"memory_create": "write", "memory_update": "write", "memory_delete": "write",
	"collection_list": "read", "collection_get": "read", "collection_create": "admin", "collection_delete": "admin",
	"context_list": "read", "context_create": "read", "context_get": "read",
	"context_update": "read", "context_delete": "read",
	"memory_status": "read",
}

type toolDef struct {
	name, description string
	schema            map[string]any
	call              func(context.Context, json.RawMessage) (any, error)
}

func str(description string) map[string]any {
	return map[string]any{"type": "string", "minLength": 1, "maxLength": 256, "description": description}
}
func integer(minimum, maximum int) map[string]any {
	return map[string]any{"type": "integer", "minimum": minimum, "maximum": maximum}
}
func object(required []string, properties map[string]any) map[string]any {
	out := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}
func props(values ...map[string]any) map[string]any {
	out := map[string]any{}
	for _, v := range values {
		for key, value := range v {
			out[key] = value
		}
	}
	return out
}

//nolint:lll // This declarative catalog keeps wire fields beside each tool description.
func (h *Handler) toolDefinitions() []toolDef {
	ctxProp := map[string]any{"context_id": str("Owned durable context handle; never a transport session ID.")}
	idProp := map[string]any{"id": str("Source document ID.")}
	page := map[string]any{"cursor": map[string]any{"type": "string", "minLength": 1, "maxLength": 1024, "description": "Opaque cursor from this operation and scope."}, "limit": integer(1, 100)}
	key := map[string]any{"idempotency_key": map[string]any{"type": "string", "minLength": 1, "maxLength": 128, "description": "Keep unchanged for retries of this exact operation."}}
	sourceVersion := map[string]any{"if_version": str("Exact opaque source version returned by the latest read.")}
	version := map[string]any{"expected_version": str("Exact opaque context version returned by the latest read.")}
	searchProps := map[string]any{
		"mode":  map[string]any{"type": "string", "enum": []string{"semantic", "keyword", "hybrid"}},
		"limit": integer(1, 100), "max_content_bytes": integer(0, 160*1024),
		"min_score": map[string]any{"type": "number", "minimum": 0, "maximum": 1}, "filters": filterSchema(),
	}
	searchDefaults := object(nil, searchProps)
	searchDefaults["description"] = "Saved search defaults; explicit search arguments override them."
	expires := map[string]any{"type": []string{"string", "null"}, "format": "date-time", "description": "UTC timestamp; null makes the context persistent. Omission preserves/defaults its expiry."}
	pins := map[string]any{"type": "array", "maxItems": 100, "items": object([]string{"document_id", "version"}, map[string]any{"document_id": str("Pinned source ID."), "version": str("Pinned source version.")})}
	return []toolDef{
		{"memory_search", "Find source memories in the context's collection using hybrid, keyword or semantic retrieval. Returns evidence excerpts and versions, never vectors.", object([]string{"context_id", "query"}, props(ctxProp, searchProps, map[string]any{"query": map[string]any{"type": "string", "minLength": 1, "maxLength": 4096}})), h.search},
		{"memory_read", "Read a source memory in UTF-8 byte pages. Supply version when continuing a page. Metadata is opt-in.", object([]string{"context_id", "id"}, props(ctxProp, idProp, map[string]any{"include_content": map[string]any{"type": "boolean", "description": "False returns metadata only; default true."}, "metadata_fields": map[string]any{"type": "array", "maxItems": 64, "items": str("Top-level metadata field to include.")}, "offset": integer(0, domdoc.MaxContentSize), "max_bytes": integer(1, domdoc.MaxContentSize), "version": str("Version from the first page; required when offset > 0."), "include_metadata": map[string]any{"type": "boolean"}})), h.read},
		{"memory_list", "List source identities and versions with an opaque cursor. Content is fetched separately with memory_read.", object([]string{"context_id"}, props(ctxProp, page, map[string]any{"include_metadata": map[string]any{"type": "boolean"}})), h.list},
		{"memory_similar", "Find other memories similar to a source, reusing its stored vector without another embedding call.", object([]string{"context_id", "id"}, props(ctxProp, idProp, searchProps)), h.similar},
		{"memory_create", "Create an absent memory. Never overwrites an existing ID. Requires a write key and retry-safe idempotency key.", object([]string{"context_id", "id", "content", "idempotency_key"}, props(ctxProp, idProp, key, sourceProperties(false))), h.create},
		{"memory_update", "Update a memory with exact version CAS. Metadata uses JSON Merge Patch; omitted fields remain unchanged.", object([]string{"context_id", "id", "if_version", "idempotency_key"}, props(ctxProp, idProp, key, sourceVersion, sourceProperties(true))), h.update},
		{"memory_delete", "Delete exactly the source version reviewed by the caller. Requires a write key, if_version and idempotency_key.", object([]string{"context_id", "id", "if_version", "idempotency_key"}, props(ctxProp, idProp, key, sourceVersion)), h.delete},
		{"collection_list", "List current collection identities and generations with a bounded cursor.", object(nil, page), h.collectionList},
		{"collection_get", "Inspect a collection, including active/deleting/absent lifecycle state and immutable generation.", object([]string{"name"}, map[string]any{"name": str("Collection name.")}), h.collectionGet},
		{"collection_create", "Create a text collection with declared tag/numeric metadata indexes. Administrator only.", object([]string{"name"}, map[string]any{"name": str("Collection name."), "fields": map[string]any{"type": "array", "maxItems": 64, "items": object([]string{"name", "type"}, map[string]any{"name": str("Metadata field name."), "type": map[string]any{"type": "string", "enum": []string{"tag", "numeric"}}})}}), h.collectionCreate},
		{"collection_delete", "Delete exactly the named collection generation and all of its sources. Administrator only; confirm_name must match name.", object([]string{"name", "generation", "confirm_name"}, map[string]any{"name": str("Collection name."), "generation": str("Exact generation from collection_get."), "confirm_name": str("Explicitly repeat the reviewed collection name.")}), h.collectionDelete},
		{"context_list", "List this principal's durable contexts. Read keys may manage their own contexts.", object(nil, page), h.contextList},
		{"context_create", "Create an owned durable context pinned to a collection generation, with search defaults, pins and expiry. Default lifetime is seven days.", object([]string{"name", "collection", "generation", "idempotency_key"}, props(key, map[string]any{"name": str("Human-readable context name."), "collection": str("Collection name."), "generation": str("Generation from collection_get or collection_list."), "search": searchDefaults, "pins": pins, "expires_at": expires})), h.contextCreate},
		{"context_get", "Read an owned context and report whether its pinned collection generation is still usable.", object([]string{"context_id"}, props(ctxProp, map[string]any{"verify_pins": map[string]any{"type": "boolean"}})), h.contextGet},
		{"context_update", "Update owned context settings using exact version CAS. Collection and generation cannot be retargeted.", object([]string{"context_id", "expected_version", "idempotency_key"}, props(ctxProp, key, version, map[string]any{"name": str("New context name."), "search": searchDefaults, "pins": pins, "expires_at": expires})), h.contextUpdate},
		{"context_delete", "Delete an owned context using exact version CAS; does not delete memories.", object([]string{"context_id", "expected_version", "idempotency_key"}, props(ctxProp, key, version)), h.contextDelete},
		{"memory_status", "Report readiness, embedding budget and transport limits; optionally inspect an owned context.", object(nil, ctxProp), h.status},
	}
}
