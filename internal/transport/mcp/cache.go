package mcp

import (
	"context"
	"strings"

	sdk "github.com/modelcontextprotocol/go-sdk/mcp"
)

func setCacheable(_ context.Context, req sdk.Request, cache *sdk.Cacheable) {
	cache.CacheScope = "private"
	cache.TTLMs = 0
	switch p := req.GetParams().(type) {
	case *sdk.ReadResourceParams:
		if p.URI == "vecdex://guide" || p.URI == "vecdex://schema" || strings.HasPrefix(p.URI, "skill://vecdex/") {
			cache.TTLMs = 300000
		}
	case *sdk.ListToolsParams,
		*sdk.ListPromptsParams,
		*sdk.ListResourcesParams,
		*sdk.ListResourceTemplatesParams,
		*sdk.DiscoverParams:
		cache.TTLMs = 300000
	}
}

//nolint:wrapcheck // The SDK must receive its typed JSON-RPC error with the resource URI intact.
func resourceURIError(uri string, err error) error {
	if classify(err).Code == "not_found" {
		return sdk.ResourceNotFoundError(uri)
	}
	return resourceError(err)
}
