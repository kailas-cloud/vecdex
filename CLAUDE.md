# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What This Is

Vecdex is a vector index management HTTP service (Go 1.25, Chi) on top of Valkey with valkey-search and valkey-json modules. REST API for creating collections, storing documents with automatic embedding (Nebius API), and KNN search.

## Common Commands

```bash
just build                  # go build + fmt + vet
just test-unit              # Unit tests only (no Valkey needed, uses -short)
just test-local             # E2E tests against running Valkey (needs NEBIUS_API_KEY)
just test-e2e               # Full E2E in Docker (CI-style, docker compose)
just test                   # Unit + local E2E combined
just lint                   # golangci-lint
just coverage               # HTML coverage report
just valkey-up              # Start local Valkey container (port 6379)
just valkey-down            # Stop Valkey
just pre-commit             # build + test-unit

# Run a single test
go test ./internal/tests/e2e/... -run TestName -v

# Run unit tests only (skip E2E)
go test ./... -short -v
```

## Architecture

Clean layered architecture: `transport/http` → `usecase` → `repository` → `internal/db`.

**Bootstrap flow** (`cmd/vecdex/main.go`): Load YAML config → init embedding registry (with DB cache) → connect DB (10s readiness wait) → wire repos (collection + document) → services → HTTP server → graceful shutdown on SIGTERM.

### Embedding Pipeline

The embedding system uses a decorator chain, assembled in `internal/embedding/registry.go:Init()`:

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

```
POST   /api/v1/:collection           → Create collection
GET    /api/v1/:collection           → Get collection metadata
DELETE /api/v1/:collection           → Delete collection
GET    /api/v1/collections           → List collections (?offset=0&limit=100)
POST   /api/v1/:collection/search    → KNN search (auto-vectorizes query)
POST   /api/v1/:collection/:id       → Create document (auto-vectorizes content)
PUT    /api/v1/:collection/:id       → Update document (re-vectorizes)
GET    /api/v1/:collection/:id       → Get document
DELETE /api/v1/:collection/:id       → Delete document
GET    /health                       → Health check
GET    /metrics                      → Prometheus metrics
```

Route ordering matters: `/search` is registered before `/:id` to avoid param collision.

### Key Packages

- **`internal/db/`** — DB facade: `Store` interface with sub-interfaces (`HashStore`, `JSONStore`, `KVStore`, `IndexManager`, `Searcher`). Index types, search query structs, sentinel errors. Implementation in `internal/db/redis/` and `internal/db/valkey/` via rueidis.

- **`internal/usecase/embedding/`** — Provider interfaces (`Provider[T]`, `ProviderWithUsage`), decorator chain (see above), vectorizer generics.

- **`internal/domain/`** — `Collection`, `Document`, `SearchRequest`, `SearchResult`. Sentinel errors: `ErrNotFound`, `ErrAlreadyExists`, `ErrDocumentNotFound`, `ErrInvalidSchema`.

- **`internal/usecase/document/`** — Document CRUD + search. Validates doc ID (alphanumeric + `_-`, max 256), validates tags/numerics against collection schema, auto-vectorizes on create/update.

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
- **E2E tests** live in `internal/tests/e2e/` and require running Valkey + `NEBIUS_API_KEY` (for document tests that call embedding API)
- **Pytest E2E** in `tests/` — Python-based API-level tests using httpx, run via Docker Compose
- **Docker CI**: `docker-compose up --build --abort-on-container-exit` spins up Valkey + runs full suite
- Custom Valkey image: `ghcr.io/kailas-cloud/valkey:0.0.3-alpine` (compiled valkey-json + valkey-search)

## Conventions

- Code comments are in Russian
- Go generics used for repository patterns, embedding provider chain, and vectorizer types
- Commit messages: short, imperative ("Add ...", "Fix ...")
- `gofmt` formatted
- Domain errors are sentinel values mapped to HTTP status codes in transport layer
- **No `init()` functions.** Use explicit constructors (`var v = newX()`) for package-level singletons — deterministic, testable, no hidden side effects
