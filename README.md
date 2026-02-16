# vecdex

[![Go Report Card](https://goreportcard.com/badge/github.com/kailas-cloud/vecdex)](https://goreportcard.com/report/github.com/kailas-cloud/vecdex)
[![Codacy Badge](https://app.codacy.com/project/badge/Grade/TgdxZXSGCIJe00LeVpdK)](https://app.codacy.com/gh/kailas-cloud/vecdex/dashboard)
[![CI](https://github.com/kailas-cloud/vecdex/actions/workflows/tests.yml/badge.svg)](https://github.com/kailas-cloud/vecdex/actions/workflows/tests.yml)

Vector index management HTTP service built on Valkey with valkey-search and valkey-json modules.

## Architecture

```
internal/
├── db/              # DB facade (Store interface, index/search types)
│   ├── redis/       # Redis implementation via rueidis
│   └── valkey/      # Valkey implementation via rueidis
├── domain/          # Domain models
├── repository/      # Repository implementations (consumer interfaces)
├── usecase/         # Business logic
├── transport/       # HTTP transport (Chi + oapi-codegen)
└── tests/e2e/       # End-to-end tests
```

## Features

- Generic repository pattern with Go generics
- Type-safe FT.CREATE index builder
- Support for HASH and JSON storage
- Vector search (FLAT and HNSW algorithms)
- TAG, NUMERIC fields with filtering
- Automatic embedding via OpenAI-compatible providers
- Embedding cache, budget tracking, instrumentation
- Comprehensive test coverage (unit + E2E)

## Requirements

- Go 1.25+
- Valkey with search and json modules
- Docker + Docker Compose (for local testing)

## Quick Start

```bash
# Install just
brew install just

# Run unit tests (no Valkey needed)
just test-unit

# Start Valkey locally
just valkey-up

# Run E2E tests
CACHE_ADDR=localhost:6379 just test-e2e

# Or run all tests in Docker
just test-docker
```

## Development

### Available Commands

```bash
just              # Show all available recipes
just build        # Build and format code
just test-unit    # Run unit tests
just test-e2e     # Run E2E tests
just test-ci      # Run CI-like tests with Docker
just valkey-up    # Start Valkey container
just valkey-down  # Stop Valkey container
just clean        # Clean test cache and containers
just coverage     # Generate coverage report
just pre-commit   # Quick check before commit
```

### Running Tests

**Unit tests** (fast, no dependencies):
```bash
go test ./... -short
```

**E2E tests** (requires Valkey):
```bash
# With external Valkey
CACHE_ADDR=valkey.example.com:6397 \
DB_PASSWORD=secret \
go test ./...

# With local Docker
just valkey-up
CACHE_ADDR=localhost:6379 go test ./...
```

**Docker Compose** (fully isolated):
```bash
docker-compose up --abort-on-container-exit test
```

## Usage Example

```go
import (
    dbRedis "github.com/kailas-cloud/vecdex/internal/db/redis"
    collectionrepo "github.com/kailas-cloud/vecdex/internal/repository/collection"
)

// Create database store
store, _ := dbRedis.NewStore(dbRedis.Config{
    Addrs:    []string{"localhost:6379"},
    Password: "secret",
})
defer store.Close()

// Create repository (accepts narrow consumer interface via ISP)
repo := collectionrepo.New(store, 1024)
```

## CI/CD

GitHub Actions runs lint, unit tests, and E2E tests on every push to `main` and PRs.

See `.github/workflows/tests.yml` for details.

## Index Builder

Create FT indexes with type-safe builder:

```go
import "github.com/kailas-cloud/vecdex/internal/db"

// Simple index
idx := db.NewIndex("my-idx").
    OnHash().
    Prefix("doc:").
    Tag("category").
    Numeric("price").
    MustBuild()

// Vector index with HNSW
idx := db.NewIndex("vec-idx").
    OnHash().
    Prefix("emb:").
    Tag("source").
    VectorHNSW("embedding", 1536, db.DistanceCosine, 32, 400).
    MustBuild()

// Create via Store facade
store.CreateIndex(ctx, idx)
```

## License

MIT
