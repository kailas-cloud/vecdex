# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- Operations runbook with pinned-engine settings, explicit alert activation, capacity thresholds, controlled expansion and the current restriction on routine shard removal. Prometheus rules include per-node memory, Search writer/snapshot failures, eviction, API errors and budget exhaustion.
- `NewHTTP` exposes the same Go SDK facade through the public API, including JSON metadata, source versions, typed indexes, CAS, batch errors, pagination, health and usage. It preserves cancellation and returns redirects to the caller. The SDK adds no retry loop; the HTTP transport may retry keyed requests.
- Source-scoped idempotency keys retain successful write outcomes for 24 hours. The receipt commits with the source transaction, so retries after a lost response return the original version without another embedding. PUT/PATCH/DELETE accept `Idempotency-Key`; batches accept per-item keys. Pending attempts are fenced and use bounded leases.
- Optional source deadlines (`expires_at`, UTC Unix milliseconds) work through REST and both SDK transports. Deadline-only patches preserve content and avoid embedding. Partition deadline catalogs provide immediate live counts and restart recovery; native WATCH markers fence writes that cross expiry during preparation. Expired sources disappear from reads and search before physical cleanup. Maintenance removes their manifests, fragments and index records without SCAN; old cleanup cannot delete a recreated source.
- Token accounting distributes invocation receipts and measured counters over 256 virtual partitions. Usage reports combine atomic per-partition snapshots with existing history and fail if any owner fails. Ordinary invocations no longer read global totals. Warn metrics refresh on a bounded timer; repeated warnings are limited to one per UTC period.
- Strict budgets allocate bounded 30-second grants across up to eight partitions and reserve the first invocation before publishing idle credit for recovery. Normal reserve/complete operations stay on their partition. Bounded recovery fences every pool before cumulative refunds, handles partial installation and late replies, and keeps unknown consumption reserved. Initial history includes prior measured usage and reservations; unknown historical consumption conservatively exhausts affected limited periods. Replicas must use consistent policies and limits.
- Opaque source versions bind an incarnation and revision. The embedded SDK exposes `Document.Version`, `IfVersion` for Upsert/Patch/Delete, and typed conflict details. HTTP document responses expose the same version as a strong ETag; conditional PUT/PATCH/DELETE reject stale incarnations with `version_conflict` and current validators. Native regressions cover delete/recreate, restart receipts, and concurrent CAS. Batch upsert/delete also accept per-item source versions, retain conflict details and return the committed version on a successful upsert. Embedded and HTTP SDKs share this contract; native tests cover mixed outcomes, delete/recreate and concurrent batch writers.
- V3 storage layout primitives with 256 virtual document partitions and 16 control buckets, independent of physical shard count. Collection, document and search repositories now use this layout.
- Bounded catalog merging and pipelined cross-slot catalog reads, with native transaction and placement checks on an isolated three-primary cluster.
- Durable document preparation journals, commit receipts and resumable cleanup queues. Document writes register work before creating fragments; recovery fences late writers and checks ownership before unlinking payload. Cleanup progress and publication use native transactions without Lua. Server and embedded SDK maintenance workers discover and recover queued document and collection work after restart.

### Changed

- Required cluster CI covers the supported maintenance/recovery path and native engine regressions. Unsupported online topology qualification remains an ordinary test in a separate manually dispatched workflow.
- PUT/PATCH and the Go SDK return compact write receipts (`id`, `version`, `created`). `Get` returns the full source. Receipts report the exact committed version even when another writer immediately updates the source.
- HTTP resources use `/api/v2`; search is `POST /api/v2/collections/{collection}/search`. Document writes and responses use one arbitrary JSON `metadata` object. The Go SDK exposes the same metadata model and retains nested values and exact JSON integers.
- Search has one source-document `limit` (default 10, maximum 100) and a combined `max_content_bytes` snippet budget (default 16 KiB). Search hits include a source version usable for CAS; `Get` returns full text. Removed `top_k`, public `tags`/`numerics`, and search `content` aliases.
- Parent documents retain fragment boundaries and dimensions without duplicate binary or base64 vectors. Reads restore exact fragment vectors in bounded batches and recompute the shared representative vector; replacement races retry against the current source version.
- Removed unused SearchList/SearchCount SCAN fallbacks from the database facade and implementation. Legacy scan comparisons remain in benchmarks only.
- Readiness checks full slot coverage and Search/coordinator availability on every participating primary and replica. Index creation waits for backfill on both roles because the coordinator can select replicas.
- Search rotates primary coordinator routing across slots instead of pinning one collection's reads to its index-name slot. Index lifecycle commands keep primary routing, and searches retain full-shard consistency requirements.
- SDK `NewTextIngestor` now takes only the document service and ingestion config. It uploads original source documents; fragmentation belongs to the shared ingestion pipeline. Removed public client-side chunker types and synthetic parent/chunk metadata.
- SciFact uploads whole original sources through API v2 and evaluates their original corpus IDs. Removed client chunking, implicit parent metadata and `top_k`. Evaluation queries run once; failed searches stop the run and empty rankings count toward retrieval metrics.
- Removed implicit `parent_doc_id`/`chunk_index` indexes and metadata-based result grouping. These are ordinary metadata names; declare them explicitly to filter them. Only private fragment ownership determines a source's identity.

### Fixed

- Shard joins wait for membership convergence after adding the primary and again before assigning its replica, avoiding CLI configuration-agreement and unknown-node races. Native engine abort matching also recognizes the exact inlined AMD64 signature from the pinned public image; unrelated crashes remain failures.
- Search 1.2.1 readiness requires one active writer and fork suspension set to zero on every participant, including standalone nodes. Compose supplies both public configuration workarounds for upstream #1651 and #1260. Native transactions remain intact; Vecdex does not change server settings at runtime.
- Hybrid fusion ranks source documents once per search mode, preserving the best matching fragment and stable ordering for equal fused scores.
- Typed SDK search honors requested limits above ten.
- Native transactions retry proven topology rejections on fresh connections while retaining ambiguous EXEC outcomes for recovery.
- Final collection purge fences late queue registration and persistent journal publication. Concurrent lifecycle workers cannot reopen purged guards; native tests verify that deleted generations retain no source catalog or work queue entries.
- Collection creation and deletion retain durable lifecycle jobs after cancellation and resume through background recovery. Collection creation waits for local index readiness on each participating node.
- Index lifecycle commands carry primary routing slots, so creation cannot randomly hit a replica. Search requests require all shards and consistent topology. Only official public Valkey/Search images are supported. Native strict xfails document Search 1.2.1 transaction-visibility and slot-handoff omissions and its topology consistency-check crash. Drained-query memory-pressure scale-out passes on the official image using one-key migration and replica rebuild/failover to reclaim HNSW memory; concurrent online scale-out remains limited by the public engine. See `tests/cluster/KNOWN_FAILURES.md`.
- Cluster membership refreshes periodically, allowing readiness to rediscover a restarted primary that returned as a replica without another MOVED response. The disposable topology procedure can resume split-slot transfers through native CLI repair and repeat completed expansion/contraction without changing committed source versions.
- Official-image acceptance covers BGSAVE on all three primaries and three replicas under SDK writes and distributed search. Each member must return complete local search results while INFO confirms its snapshot is active before and after the query. Independent memory-pressure testing still reproduces the public Search coordinator crash during online scale-out.
- KNN requests send an explicit response LIMIT, avoiding the backend default of ten hits.
- Malformed Search rows, field pairs and vector distances fail the entire query rather than silently producing partial success.

## [2.0.0] - 2026-11-04

### Added

- Amazon Bedrock provider for Cohere Embed v4 with explicit Bearer authentication, document/query input types, and configurable dimensions.
- Whole-document embedding with server-side splitting only after an explicit context-limit error; original text is preserved.
- Shared token accounting and conservative budget reservations across replicas, including partial batches and unknown usage.
- Bedrock TLS mock E2E coverage, native transaction regressions with scripting disabled, and cache/catalog benchmarks.

### Changed

- Storage uses isolated v2 keyspaces, collection generations, private fragments, and a source-document catalog. Existing collections must be recreated and reindexed; no storage migration is provided.
- Conditional writes use WATCH/MULTI/EXEC with revision checks and strong ETags. Preparation and cleanup run in Go without Lua.
- Embedding cache uses provider fingerprints, pipelining, deduplication, and a seven-day default TTL. Collections reject incompatible vector spaces.
- The embedded Go client's `Usage` method returns `(UsageReport, error)`.
- Go is pinned to 1.27.3 and golangci-lint to 2.15.0. Compose pins Valkey Bundle 9.1.2 with Search 1.2.1.
- Removed Redis backend support from runtime configuration, SDK options, Docker Compose, and E2E helpers. Vecdex targets only Valkey 9 with Valkey Search 1.2+.

### Fixed

- Full replacement, metadata-only PATCH, collection deletion/recreation, reserved fields, and invalid vector validation.
- Pagination, chunked similar requests, Location headers, Unicode query lengths, and preservation of unindexed search metadata.
- Successful embedding results and token usage are retained when a later sub-batch fails.

Python SDK work remains deferred. Rate limiting remains unimplemented; its nine E2E checks are strict expected failures.

## [1.0.0] - 2025-02-16

### Added

- REST API for vector collection management (create, list, get, delete)
- Document CRUD with automatic embedding via configurable providers (Nebius/OpenAI-compatible)
- Multi-mode search: hybrid, semantic, and keyword (Valkey only)
- Batch upsert and delete operations (up to 100 documents)
- Cursor-based pagination for collections and documents
- Embedding cache with SHA256-keyed deduplication in Valkey
- Token budget tracking with daily/monthly limits and warn/reject actions
- Dual database backend support: Valkey 9 (valkey-search) and Redis 8 (RediSearch)
- Prometheus metrics for embeddings, cache, and HTTP requests
- Health endpoint with dependency checks and version reporting
- Bearer token authentication
- OpenAPI 3.1 specification with generated server code (oapi-codegen)
- Multi-arch Docker images (amd64 + arm64) published to ghcr.io
- Comprehensive E2E test suite (pytest, 300+ tests) for both backends
- Codacy integration for code quality and coverage tracking

[2.0.0]: https://github.com/kailas-cloud/vecdex/releases/tag/v2.0.0
[1.0.0]: https://github.com/kailas-cloud/vecdex/releases/tag/v1.0.0
