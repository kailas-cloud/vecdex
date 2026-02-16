# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

Vecdex is a vector index management HTTP service (Go 1.25, Chi) on top of Valkey/Redis with valkey-search and Redis Search modules. REST API for creating collections, storing documents with automatic embedding (Nebius API), and multi-mode search (hybrid/semantic/keyword).

## Common Commands

```bash
just build                  # go build + fmt + vet
just test-unit              # Unit tests only (no Valkey needed, uses -short)
just test                   # Unit tests (alias for test-unit)
just lint                   # golangci-lint
just coverage               # HTML coverage report (excludes generated code)
just pre-commit             # build + lint + test-unit
just generate               # Regenerate api.gen.go from OpenAPI spec
just valkey-up              # Start local Valkey container (port 6379)
just valkey-down            # Stop Valkey + remove volumes

# Pytest E2E (runs in Docker, no API keys needed — uses mock embedder)
just test-pytest-valkey     # E2E against Valkey backend
just test-pytest-redis      # E2E against Redis backend
just test-pytest            # Both backends sequentially

# Run a single Go test
go test ./internal/usecase/document/... -run TestName -v

# Run unit tests only (skip E2E)
go test ./... -short -v
```

## Architecture

Clean layered architecture: `transport/chi` → `usecase` → `repository` → `internal/db`.

**Bootstrap flow** (`cmd/vecdex/main.go`): Load YAML config → init embedding registry (with DB cache) → connect DB (10s readiness wait) → wire repos (collection + document) → services → HTTP server → graceful shutdown on SIGTERM.

### Code Generation

HTTP transport is generated from `api/openapi.yaml` via oapi-codegen v2. The `Server` struct in `internal/transport/chi/server.go` implements the generated `ServerInterface` from `internal/transport/generated/api.gen.go`. To regenerate after spec changes:

```bash
just generate  # runs oapi-codegen with api/oapi-codegen.yaml config
```

Generated file should not be edited manually. Route handlers, request/response types, and path parameter types all come from the spec.

### Embedding Pipeline

The embedding system uses a decorator chain, assembled in `internal/usecase/embedding/registry.go`:

```
OpenAIProvider (Nebius API call)
  → CachedProvider (SHA256-keyed cache in Valkey, key prefix: vecdex:emb_cache:)
    → InstrumentedProvider (Prometheus metrics + BudgetTracker + zap logging)
      → StringVectorizer (prepends document/query instruction text)
```

Registry is a global singleton with separate document and query vectorizers per `CollectionType`. The `DocumentVectorizer`/`QueryVectorizer` interfaces are what the service layer calls. Provider chain supports `ProviderWithUsage` interface for token tracking through the decorator stack.

`BudgetTracker` is in-memory with daily/monthly token limits and auto-reset. Actions: "warn" (log and continue) or "reject" (return `ErrBudgetExceeded`).

### Valkey Data Model

Documents stored as JSON via `JSON.SET` with key pattern `vecdex:{collection}:{docID}`. Document JSON layout:

```json
{"__content": "...", "__vector": [...], "tagField": "value", "numField": 123}
```

Tags and numerics are top-level for FT.INDEX compatibility. `__content` and `__vector` are reserved prefixed fields.

Collection metadata: HASH at `vecdex:collection:{name}`, sorted set entry in `vecdex:collections` (scored by createdAt), FT index at `vecdex:{name}:idx`. Vector field indexed at `$.__vector` with HNSW (M=32, EF=400).

KNN search uses `FT.SEARCH` with DIALECT 2, supports pre-filtering by TAG fields.

### API Endpoints

Routes are generated from `api/openapi.yaml`. Current endpoints:

```
POST   /collections                              → Create collection
GET    /collections                              → List collections (cursor pagination)
GET    /collections/{name}                       → Get collection metadata
DELETE /collections/{name}                       → Delete collection
PUT    /collections/{name}/documents/{id}        → Upsert document (auto-embeds)
GET    /collections/{name}/documents/{id}        → Get document
PATCH  /collections/{name}/documents/{id}        → Partial update (metadata or content)
DELETE /collections/{name}/documents/{id}        → Delete document
GET    /collections/{name}/documents             → List documents (cursor pagination)
POST   /collections/{name}/documents/search      → Search (hybrid/semantic/keyword)
POST   /collections/{name}/documents/batch-upsert → Batch upsert (up to 100)
POST   /collections/{name}/documents/batch-delete → Batch delete (up to 100)
GET    /usage                                    → Embedding usage & budget info
GET    /health                                   → Health check
GET    /metrics                                  → Prometheus metrics
```

### Key Packages

- **`internal/db/`** — DB facade: `Store` interface with sub-interfaces (`HashStore`, `JSONStore`, `KVStore`, `IndexManager`, `Searcher`). Two implementations: `internal/db/redis/` and `internal/db/valkey/`, both via rueidis.

- **`internal/domain/`** — Rich domain model with sub-packages: `collection/` (+ `field/`), `document/` (+ `patch/`), `search/` (`filter/`, `mode/`, `request/`, `result/`), `batch/`, `usage/`. Sentinel errors in `internal/domain/errors.go`.

- **`internal/usecase/`** — Business logic: `collection/`, `document/`, `search/`, `batch/`, `embedding/`, `health/`, `usage/`. Each use case accepts narrow repository interfaces (ISP).

- **`internal/transport/chi/`** — HTTP handlers implementing generated `ServerInterface`. Error handling uses a chain of sentinel error handlers mapping domain errors to HTTP status codes.

- **`internal/transport/generated/`** — oapi-codegen output. Do not edit manually.

### Observability

Prometheus metrics (namespace `vecdex`):
- `embedding_requests_total`, `embedding_request_duration_seconds`, `embedding_tokens_total`, `embedding_errors_total` — per provider/model
- `embedding_budget_tokens_remaining` — per provider/period
- `embedding_cache_total` — hit/miss counters
- HTTP request metrics via `observability.MetricsMiddleware()`

### Configuration

YAML files in `config/` selected by `ENV` variable (default: `local`). Supports `${VAR_NAME}` interpolation.

- `local.yaml.example` — template for local development
- `dev.yaml` — Docker Compose (`valkey:6379`, Nebius API with budget limits)
- `docker.yaml` — Docker Compose test config
- `prod.yaml.example` — template for production deployment

## Testing

- **Unit tests** use `-short` flag to skip anything requiring Valkey
- **Pytest E2E** in `tests/` — 14 test modules (300+ tests) using httpx + tenacity, run via Docker Compose with a mock embedding server (no API keys needed). Tests are numbered for execution order and use `p0`/`p1`/`p2` priority markers. Supports both Valkey and Redis backends via Docker Compose profiles.
- **CI** (`.github/workflows/tests.yml`): lint → unit-tests (with Codacy coverage upload) → e2e-pytest (matrix: valkey + redis)
- Custom Valkey image: `valkey/valkey-bundle:9` (includes valkey-json + valkey-search)

## Conventions

- Code comments are in Russian
- Go generics used for repository patterns, embedding provider chain, and vectorizer types
- Commit messages: short, imperative ("Add ...", "Fix ...")
- `gofmt` formatted
- Domain errors are sentinel values mapped to HTTP status codes in transport layer
- **No `init()` functions.** Use explicit constructors (`var v = newX()`) for package-level singletons — deterministic, testable, no hidden side effects
- Dual backend support (Redis 8 / Valkey 9) — both must pass E2E tests
