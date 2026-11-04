# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
