# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

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

[1.0.0]: https://github.com/kailas-cloud/vecdex/releases/tag/v1.0.0
