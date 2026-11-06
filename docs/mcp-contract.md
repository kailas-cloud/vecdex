# Vecdex MCP v2 contract

The tool catalog is the executable JSON Schema contract. A deterministic copy is checked in at [contracts.json](../internal/transport/mcp/testdata/contracts.json); [examples](mcp-examples.json) cover all 17 tools. Input objects reject unknown fields. Each tool has a single operation and an output envelope that also represents domain errors. Text content contains the same IDs, versions, cursors and errors as `structuredContent`, so clients that pass only text to the model retain recovery information.

## State, identity and generations

Seven memory tools require `context_id`. A context has one collection name and immutable generation, search defaults, a name and at most 100 pins. It belongs to the authenticated `principal_id`; the client cannot choose an owner through arguments. Context reads do not extend expiry. The default expiry is seven days from creation; explicit `null` requests persistence. Mutations require the context's opaque version and an idempotency key.

Collection names can be reused, generations cannot. Document URIs include the generation. Memory use cases validate the context generation before resolving a write or query; collection deletion checks the expected generation inside the repository. A stale context cannot silently operate on a recreated collection. Context deletion only removes the context. Saving a source and pinning its version are two independent conditional operations.

`collection_get` reports lifecycle state `creating`, `active`, `deleting` or `absent`. A pending lifecycle is not successful completion. Inspect it again after recovery. Collection catalogs use bounded opaque cursors; concurrent deletions or expiry may produce an empty page with a continuation.

## Conditional writes and uncertain outcomes

`memory_create` requires a stable ID and idempotency key and refuses an existing live source. `memory_update` and `memory_delete` additionally require `if_version`. Versions are opaque equality tokens. A version conflict requires reading the current source and reconciling the change; the server does not force an overwrite.

Text updates replace source text. Metadata uses JSON Merge Patch: absent means preserve, nested null deletes a property, arrays replace, and top-level null clears metadata. Expiry follows absent/preserve, timestamp/replace and null/clear semantics.

Write receipts retain the original committed version for 24 hours. Retrying the same input and key returns the old outcome; changing the input conflicts. A receipt can outlive the source or context and cannot resurrect it. Replay is not proof that the original version is still current. Read by ID to check current state. The commit guarantees source persistence, not synchronous Search indexing; results never claim `searchable: true`.

Errors include stable `code`, `retryable`, `message` and a next action. Domain errors use `isError: true`. Invalid protocol requests use JSON-RPC errors. Dependency failures and embedding-budget rejection must not be reported as successful storage. Logs exclude source content and credentials by default.

## Bounded reads

Search defaults to five short hits; listing defaults to 20. Limits cannot exceed 100. Search supports hybrid, semantic and keyword modes and existing structured filters. Each source hit carries ID, current version, URI and short content. Scores rank relevance, not truth. Search is top-k: it provides neither an exhaustive match count nor snapshot pagination. Memory vectors are not returned.

Exact reads can select content, metadata or both. Text offsets are UTF-8 byte offsets and later chunks require the same source version. A changed version invalidates continuation instead of mixing revisions. Metadata can be projected by fields. Resource reads expose current state, not historical snapshots.

The default application response budget is 32 KiB; `max_output_bytes` can raise it to 256 KiB. It counts JSON escaping, structured output, fallback text, metadata and references. A separate reserve covers protocol enrichment. Truncation is explicit and includes a continuation or a narrower read action. Serialized JSON and SSE frames are never cut to size. Oversized identifiers or metadata that cannot fit produce a recoverable error.

## Protocol and resource surfaces

`/mcp` uses Streamable HTTP and official Go SDK v1.8.0 with `Stateless: true`. Modern requests follow `2026-07-28` discovery and per-request metadata. The `2025-11-25` profile supports its initialize handshake without advertising modern Skills or subscriptions. The application state is durable independently of these transport lifecycles.

Resource templates and tool schemas are discoverable from the server. Static instructions and discovery are cacheable. Memory data is private with TTL 0. Search-result caching is not provided. Prompts `resume_context` and `review_memory` and completions add guidance; they do not replace required tools.

The Skills extension is Beta. The local SDK adapter implements its methods without forking the official SDK. The installable archive is built from `internal/skills/bundle`, the same source embedded by the server.

Subscriptions check subscribed document, context and schema fingerprints every five seconds, coalesce changes and allow at most 256 URIs per stream. Fingerprints include generation, version, presence and logical expiry. Polling sees mutations made through the embedded SDK or another replica. Notifications invalidate cached knowledge; they are not a durable event log. Reread after reconnect. Ordinary operation deadlines and stream lifetimes are separate.

## Engine coverage

| Engine capability | Agent contract | Other surface / limit |
| --- | --- | --- |
| Hybrid, semantic, keyword search and structured filters | `memory_search` | One context collection; top-k |
| Similar sources | `memory_similar` | Current source generation |
| Exact source and metadata projection | `memory_read` | No fragment vectors |
| Source enumeration | `memory_list` | Opaque cursor, no snapshot |
| Create, content replacement, metadata merge patch, expiry | Memory write tools | CAS and 24-hour receipts |
| Collection schema, lifecycle and generation | Collection tools/resources | Admin mutations |
| Persistent task scope and pinned references | Context tools/resources | Owner isolation, CAS, expiry |
| Health, embedding budget and accounting | `memory_status`, usage resource | Operational `/health`, `/metrics` |
| Static guidance, handoffs and review | Skills/prompts/completions | Same operations available as tools |
| Source change invalidation | Modern subscriptions | Polling; no guaranteed replay |
| Batch, chunk ingestion, typed indexing and vectors | Embedded Go SDK | Deliberately absent from MCP |
| Automatic observation/reflection, historical versions | Not provided | Agent notes are ordinary documents |

## Evidence rules for agents

Store optional note metadata `kind`, `statement_type`, `source_role`, `observed_at`, `effective_at`, `source_refs`, `thread_id` and `resource_id`. Distinguish a user assertion, an agent inference, a plan and a confirmed result. Read source references before using compressed observations. Missing detail and contradictions remain unresolved until supported by evidence. See the four bundled skills for workflows.

Client adapter tests should assert what is actually sent to the model, not only the raw protocol object. Record success, call count, result bytes and latency on fixed data. A deterministic adapter test is not an LLM reasoning benchmark; no competitive performance claim follows from it.
