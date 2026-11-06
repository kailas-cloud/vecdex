# Embedded SDK contracts

The package `github.com/kailas-cloud/vecdex/pkg/sdk` remains the in-process interface for batch ingestion, typed indices and source fragment preparation. The external agent interface is MCP. `NewHTTP`, HTTP options and REST errors have been removed.

Use `NewEmbedded` with a Valkey connection and the intended embedder, then close the client when its owning process stops. Inspect the examples in `examples/` and the exported Go documentation for configuration. The server's production provider, query/document purpose, cache, shared accounting and recovery composition lives in `internal/app`; constructing a bare SDK instance does not implicitly reproduce that server configuration.

`NewIndex[T]` maps typed documents to a collection schema. `NewTextIngestor` prepares source text and chunk fragments in Go. Batch writes remain independent source commits, with per-item outcomes. A partial batch is not an atomic whole-batch success. Use source IDs to confirm writes; Search indexing is asynchronous.

Use conditional write options for concurrent updates and `IfAbsent` for create-only writes. Idempotency fingerprints include the create-only condition. A stable idempotency key and unchanged input replay a receipt for 24 hours; replay after source deletion does not recreate it. Keep the original committed version distinct from current source state. Expected collection generation guards prevent a stale handle from resolving a reused name.

Source projection methods retrieve original source fields without loading fragment vectors. Collection inspection exposes pending lifecycle state and generation-conditional deletion. Catalog pagination is bounded and opaque; a concurrent deletion can produce an empty page with a next cursor. These guarantees apply to MCP and embedded calls through the same use cases.

The existing v3 source format is unchanged. Native Valkey and cluster regressions remain under `pkg/sdk`, `internal/repository` and `internal/db/valkey`. Enable them only against disposable fixtures with the environment variables documented in `AGENTS.md` and `tests/cluster/README.md`.
