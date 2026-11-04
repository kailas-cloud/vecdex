<p align="center">
  <img src="docs/vecdex-banner.png" alt="vecdex" width="100%"/>
</p>

<h3 align="center">Lightweight vector search engine on top of Valkey</h3>

<p align="center">
  <a href="https://goreportcard.com/report/github.com/kailas-cloud/vecdex"><img src="https://goreportcard.com/badge/github.com/kailas-cloud/vecdex" alt="Go Report Card"></a>
  <a href="https://app.codacy.com/gh/kailas-cloud/vecdex/dashboard"><img src="https://app.codacy.com/project/badge/Grade/3ebe9ae848f348bca37551c9da1e77e2" alt="Codacy Grade"></a>
  <a href="https://app.codacy.com/gh/kailas-cloud/vecdex/dashboard"><img src="https://app.codacy.com/project/badge/Coverage/3ebe9ae848f348bca37551c9da1e77e2" alt="Codacy Coverage"></a>
  <a href="https://github.com/kailas-cloud/vecdex/actions/workflows/tests.yml"><img src="https://github.com/kailas-cloud/vecdex/actions/workflows/tests.yml/badge.svg" alt="CI"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache%202.0-blue.svg" alt="License"></a>
</p>

---

## Why vecdex?

Most vector databases are **heavy, cloud-locked, or expensive**. vecdex takes a different approach:

- **Zero new infrastructure** — runs on Valkey you already have
- **Automatic embeddings** — send text, get vectors (Nebius AI, OpenAI-compatible providers)
- **Three search modes** — hybrid (RRF), semantic (KNN), keyword (BM25) via one endpoint
- **Budget controls** — daily/monthly token limits with automatic tracking
- **300+ E2E tests** — battle-tested on the Valkey stack

## Examples

### Semantic code search

Define a struct, tag the fields, search with one line:

```go
package main

import (
    "context"
    "fmt"

    vecdex "github.com/kailas-cloud/vecdex/pkg/sdk"
)

type CodeChunk struct {
    ID       string `vecdex:"id"`
    Content  string `vecdex:"content,content"`
    Language string `vecdex:"language,tag"`
    Repo     string `vecdex:"repo,tag"`
}

func main() {
    ctx := context.Background()
    client, _ := vecdex.New(ctx,
        vecdex.WithValkey("localhost:6379", ""),
        vecdex.WithEmbedder(myEmbedder),       // any OpenAI-compatible provider
    )
    defer client.Close()

    idx, _ := vecdex.NewIndex[CodeChunk](client, "code-chunks")
    _ = idx.Ensure(ctx)

    // Index some code
    _ = idx.UpsertBatch(ctx, []CodeChunk{
        {ID: "1", Content: "CreateCollection validates the name and builds an FT index", Language: "go", Repo: "vecdex"},
        {ID: "2", Content: "SearchBuilder chains Query, Mode, Where, Limit into a query", Language: "go", Repo: "vecdex"},
        {ID: "3", Content: "BudgetTracker enforces daily and monthly token limits", Language: "go", Repo: "vecdex"},
    })

    // Semantic search — one line
    hits, _ := idx.Search().
        Query("how does collection creation work").
        Mode(vecdex.ModeSemantic).
        Where("language", "go").
        Limit(5).
        Do(ctx)

    for _, h := range hits {
        fmt.Printf("%.2f  %s\n", h.Score, h.Item.Content)
    }
}
```


### Low-level API

For full control without struct tags:

```go
client, _ := vecdex.New(ctx,
    vecdex.WithValkey("localhost:6379", ""),
    vecdex.WithEmbedder(myEmbedder),
)
defer client.Close()

// Create collection with filterable fields
client.Collections().Create(ctx, "articles",
    vecdex.WithField("author", vecdex.FieldTag),
    vecdex.WithField("year", vecdex.FieldNumeric),
)

// Upsert a document — embedding happens automatically
client.Documents("articles").Upsert(ctx, vecdex.Document{
    ID:       "article-1",
    Content:  "Vector search with HNSW indexes in Valkey",
    Tags:     map[string]string{"author": "alice"},
    Numerics: map[string]float64{"year": 2025},
})

// Hybrid search (vector KNN + BM25 fused via RRF)
resp, _ := client.Search("articles").Query(ctx, "HNSW performance", &vecdex.SearchOptions{
    Mode:  vecdex.ModeHybrid,
    Limit: 10,
})

for _, r := range resp.Results {
    fmt.Printf("%.2f  %s\n", r.Score, r.Content)
}
```

## How it compares

| | vecdex | Pinecone | Qdrant | Weaviate | pgvector |
|---|---|---|---|---|---|
| Self-hosted | Yes | No | Yes | Yes | Yes |
| Managed cloud | No | Yes | Yes | Yes | Yes |
| Underlying storage | Valkey | Proprietary | Custom | Custom | PostgreSQL |
| Auto-embedding | Yes | No | No | Yes | No |
| Hybrid search (RRF) | Yes | Yes | Yes | Yes | No |
| Token budget tracking | Yes | No | No | No | No |
| Go SDK with generics | Yes | No | Yes | No | No |
| Setup complexity | Low | None | Medium | High | Low |
| License | Apache 2.0 | Proprietary | Apache 2.0 | BSD-3 | PostgreSQL |

## Search modes

| Mode | How it works | Embedding cost | Backend support |
|------|-------------|----------------|-----------------|
| `hybrid` (default) | Vector KNN + BM25 fused via Reciprocal Rank Fusion | 1 call | Valkey 9 + Valkey Search 1.2+ |
| `semantic` | Pure cosine-similarity KNN | 1 call | Valkey 9 |
| `keyword` | BM25 full-text search | 0 calls | Valkey Search 1.2+ |

## Backend support

| Backend | Status | Notes |
|---------|--------|-------|
| **Valkey 9 + Valkey Search 1.2+** | Supported | The only supported runtime target |
| AWS ElastiCache | Planned | |
| PostgreSQL + pgvector | Planned | |

## Key features

| Feature | Description |
|---------|-------------|
| **Hybrid search** | Reciprocal Rank Fusion combining vector KNN and BM25 keyword search |
| **Semantic search** | Pure cosine-similarity KNN over HNSW vectors |
| **Keyword search** | BM25 full-text search — zero embedding tokens consumed |
| **Structured filters** | `must` / `should` / `must_not` with tag match and numeric range operators |
| **Auto-embedding** | Send text, get vectors via any OpenAI-compatible provider |
| **Typed Go SDK** | Schema-first generics with `TypedIndex[T]` and fluent search builder |
| **Batch operations** | Upsert/delete up to 100 items per call with per-item status |
| **Token budget** | Daily/monthly limits with warn or reject policies |
| **Cursor pagination** | Stable, opaque-cursor pagination for collections and documents |
| **Embedding cache** | SHA256-keyed cache in Valkey — identical content is never re-embedded |
| **Prometheus metrics** | Request latency, embedding tokens, budget, cache hit/miss |

## Architecture

```
┌─────────────────────────────────────────────────────┐
│              Go SDK  (pkg/sdk)                       │
│  TypedIndex[T] · SearchBuilder · Fluent API          │
├─────────────────────────────────────────────────────┤
│              HTTP API  (Chi)                          │
│  Auth · Metrics · Wide-event logging · Recovery       │
├─────────────────────────────────────────────────────┤
│                 Use Cases                             │
│  Collection · Document · Search · Batch · Embedding   │
├─────────────────────────────────────────────────────┤
│                Repositories                           │
│  Consumer interfaces (ISP) over Store facade          │
├─────────────────────────────────────────────────────┤
│              Valkey backend (rueidis, RESP2)         │
└─────────────────────────────────────────────────────┘
                         │
              Valkey 9 + valkey-search 1.2
```

Embedding pipeline (decorator chain):
```
OpenAIProvider → CachedProvider → InstrumentedProvider → StringVectorizer
       ↓               ↓                  ↓
  Nebius API     SHA256 cache       Prometheus +
                  in Valkey        BudgetTracker
```

## API reference

### Go SDK

```
go get github.com/kailas-cloud/vecdex
```

| Type | Description |
|------|-------------|
| `vecdex.Client` | Connection to Valkey, entry point for all operations |
| `vecdex.TypedIndex[T]` | Generic index with schema inferred from struct tags |
| `vecdex.SearchBuilder[T]` | Fluent search: `.Query()`, `.Mode()`, `.Where()`, `.Limit()`, `.Do()` |
| `vecdex.Hit[T]` | Search result with `.Item` and `.Score` |
| `vecdex.Document` | Untyped document for low-level API |
| `vecdex.Embedder` | Interface for text-to-vector providers |

Struct tag format: `vecdex:"name,modifier"` — modifiers: `id`, `content`, `tag`, `numeric`, `stored`.

### REST API

Full OpenAPI 3.0 specification: [`api/openapi.yaml`](api/openapi.yaml)

| Method | Path | Description |
|--------|------|-------------|
| `POST` | `/collections` | Create collection |
| `GET` | `/collections` | List collections (cursor pagination) |
| `GET` | `/collections/{name}` | Get collection details |
| `DELETE` | `/collections/{name}` | Delete collection |
| `PUT` | `/collections/{name}/documents/{id}` | Upsert document (auto-embeds) |
| `GET` | `/collections/{name}/documents/{id}` | Get document |
| `PATCH` | `/collections/{name}/documents/{id}` | Partial update (no re-vectorization) |
| `DELETE` | `/collections/{name}/documents/{id}` | Delete document |
| `GET` | `/collections/{name}/documents` | List documents (cursor pagination) |
| `POST` | `/collections/{name}/documents/search` | Search documents |
| `POST` | `/collections/{name}/documents/batch-upsert` | Batch upsert (up to 100) |
| `POST` | `/collections/{name}/documents/batch-delete` | Batch delete (up to 100) |
| `GET` | `/usage` | Embedding usage & budget info |
| `GET` | `/health` | Health check |
| `GET` | `/metrics` | Prometheus metrics |

## Quick start

### Docker Compose (recommended)

```bash
git clone https://github.com/kailas-cloud/vecdex.git
cd vecdex
cp .env.example .env
# Edit .env — set NEBIUS_API_KEY and optionally VECDEX_API_KEY

docker compose up vecdex
# API is running at http://localhost:8080
```

The Docker image preinstalls CPU ONNX Runtime from the official Linux release tarballs for `linux/amd64` and `linux/arm64`, enables CGO by default, and exposes `ONNXRUNTIME_DIR`/`LD_LIBRARY_PATH` inside the container. For local ONNX runs, the image copies the Git LFS-tracked model assets from `./models` into `/app/models/all-MiniLM-L6-v2`.

### From source

```bash
# Prerequisites: Go 1.27.3+, just
git clone https://github.com/kailas-cloud/vecdex.git
cd vecdex

just build
# Binary at build/vecdex

# Start Valkey locally
just valkey-up

# Run the server
ENV=local ./build/vecdex
```

## Configuration

vecdex uses YAML config files from `config/` selected by the `ENV` environment variable (default: `local`). Supports `${VAR_NAME}` interpolation from environment.

| Variable | Description | Default |
|----------|-------------|---------|
| `ENV` | Config file to load (`local`, `dev`, `docker`, `prod`) | `local` |
| `VALKEY_ADDR` | Valkey address | `localhost:6379` |
| `VALKEY_PASSWORD` | Valkey password | — |
| `HTTP_PORT` | HTTP server port | `8080` |
| `NEBIUS_API_KEY` | Nebius AI embedding API key | — |
| `VECDEX_API_KEY` | API authentication key | — |

### Bedrock and token accounting

Use [`config/bedrock.yaml.example`](config/bedrock.yaml.example) for Cohere Embed v4.
Set `AWS_BEARER_TOKEN_BEDROCK` on the server and supply the region explicitly.
The HTTP client uses that Bearer key directly. It does not load AWS profiles or
request role credentials. The default vector has 1024 dimensions and the model
accepts up to 128,000 tokens per input. Documents are sent whole first; an explicit
context-limit error triggers server-side splitting without losing the source text.
The provider shares its concurrency limit across document and query calls. Defaults
are four concurrent calls and a 30-second call timeout. Document preparation has a
120-second deadline; batch preparation has a 300-second deadline. The default HTTP
write timeout is 310 seconds so a batch can return its result.

`GET /usage?period=day|month|total` reports actual upstream calls and measured tokens,
including health probes and completed parts of failed batches. Accounting remains
active without configured limits. `unknown_requests` counts calls whose consumption
could not be measured; in that case `tokens` is a lower bound. Cache hits do not add
upstream calls or tokens. Collection filtering is not supported and returns 400.
Day and month boundaries use UTC. Total usage is retained independently of those periods.

For `budget.action: reject`, all replicas reserve capacity atomically in Valkey
using native WATCH/MULTI/EXEC transactions before each upstream attempt.
All calculations and retries run in Go. Cohere v4 reserves 128,000 tokens per input and then
releases the unused portion when AWS reports measured consumption. A small daily
limit can therefore reject even a short input until it has room for that reserve.
A missing usage header, network failure, or interrupted request keeps the conservative
reserve until the period resets. Validation/authentication/throttling rejections
with no usage release it. Store failures prevent a request from starting without a
reservation. `warn` records usage without rejecting requests. Unlimited budgets use
`-1` for both limit and remaining values.

The embedded Go client's `Usage` method now returns `(UsageReport, error)` because
reading shared counters can fail.

### Vector compatibility and cache

A collection records the fingerprint of its vectorizer. Changing the model,
region/endpoint, dimensions, or document/query instructions requires a new
collection and reindexing. Incompatible vector writes, semantic/hybrid search,
and similar search return `embedding_space_mismatch`. Source reads, deletion,
metadata-only PATCH and keyword search remain available for reindexing.
Credential rotation, timeout, concurrency and cache retention do not change the
fingerprint. The fingerprint contains no credentials.

Embedding cache entries expire after seven days. Set `cache_ttl_sec` on the
provider to change retention. Batches deduplicate equal texts before cache lookup
and upstream embedding, preserve input order, and pipeline cache commands.
Malformed, non-finite or incompatible cached vectors are treated as misses.

A [local cache benchmark](tests/benchmarks/cache-2026-11-04.txt) compared the
previous serial algorithm with the pipeline on real Valkey, using 96 inputs,
1024-dimensional vectors and a deterministic embedder. The table gives the median of three mean batch-latency measurements on an
isolated local Valkey 9.0.0 container. Upstream model latency is excluded.

| Inputs / cache | Serial | Pipeline | GET / SET commands |
|---|---:|---:|---|
| 96 unique / cold | 76.32 ms | 3.47 ms | 96 / 96 in both |
| 96 unique / warm | 37.17 ms | 2.41 ms | 96 / 0 in both |
| 32 unique repeated 3 times / cold | 75.66 ms | 1.90 ms | 96 / 96 → 32 / 32 |
| 32 unique repeated 3 times / warm | 38.90 ms | 1.07 ms | 96 / 0 → 32 / 0 |

Pipeline uses one cache read group and at most one write group. With all unique
inputs it allocated about 3–4% more bytes than the serial path; with repeated
inputs it used about 65% fewer bytes. Results depend on backend latency and batch
contents. Reproduce against an isolated test instance:

```bash
VECDEX_TEST_VALKEY_ADDR=127.0.0.1:16379 go test ./internal/repository/embcache \
  -run '^$' -bench BenchmarkCacheLive -benchmem -benchtime=30x -count=3
```

### Document pagination and count

Each collection generation has a sorted catalog of source IDs. Parent replacement
and catalog membership commit together with three native commands; deletion uses
two. Preparation and fragment cleanup run in Go outside the commit. No Lua is used.
List reads a bounded ID range and pipelines only the requested parent hashes;
count uses `ZCARD`. Search-index backfill does not affect this count.

PUT fully replaces the previous document; metadata-only PATCH does not call the
embedding provider. Send the returned strong `ETag` as `If-Match` to guard a write
against concurrent changes. A stale revision returns 409. Search preserves tags
that are not declared as indexed fields, including when vectors are omitted.
If a later embedding sub-batch fails, earlier successful results are still stored
and returned with per-document statuses.

Cursors are opaque and bound to the collection generation. They advance after the
last scanned ID. Deleting earlier records does not shift later pages. Inserts
before the cursor appear on a new traversal; concurrent deletes can shorten a page.
Reusing a cursor after collection deletion and recreation returns 400.

The [catalog benchmark](tests/benchmarks/catalog-2026-11-04.txt) used real Valkey,
1k/100k source documents with one fragment each, 256-dimensional vectors, and
540-byte texts written through the repository. Each read case has 100 samples.
Embedding providers and search indexing are excluded. Values are local measurements.

| Corpus / operation | SCAN mean / p99 | Catalog mean / p99 | Go allocated bytes per query |
|---|---:|---:|---:|
| 1k / middle page, 20 documents | 19.56 / 31.90 ms | 1.54 / 2.94 ms | 335,045 → 129,090 |
| 1k / count | 9.06 / 10.51 ms | 0.40 / 0.50 ms | 207,296 → 256 |
| 100k / middle page, 20 documents | 914.69 / 972.40 ms | 1.33 / 1.55 ms | 24,873,558 → 129,127 |
| 100k / count | 880.87 / 1052.00 ms | 0.42 / 0.64 ms | 24,745,776 → 300 |

At 100k documents the former path ran about 1,944 SCAN commands per request.
The catalog page uses one `ZRANGEBYLEX` and 20 pipelined `HGETALL` commands;
count uses one `ZCARD`. Beginning, middle and end pages had similar catalog latency.
Search requests use an explicit field projection, so unrequested result vectors
are not transferred from Valkey.

```bash
VECDEX_TEST_VALKEY_ADDR=127.0.0.1:16379 go test ./internal/repository/document \
  -run '^$' -bench BenchmarkDocumentCatalogLive -benchmem -benchtime=100x -count=1
```

## Testing

```bash
just test-unit              # Unit tests (no Valkey needed)
just test-pytest-valkey     # E2E — supported Valkey stack (300+ pytest tests)
just test-pytest-valkey-onnx  # E2E — local ONNX model
just test-pytest            # Alias to the Valkey E2E suite
just pre-commit             # build + lint + unit tests
```

The pytest E2E suite runs in Docker Compose with a mock embedding server — no API keys required for CI.
The `vecdex` Docker image used in the stack already carries ONNX Runtime for both Intel/AMD64 Linux and Apple Silicon Docker hosts (`linux/arm64`).
The root `test` helper service now reuses the same Go + ONNX Runtime Docker base instead of a separate Alpine image, so future CGO-backed inference tests can run in the same environment shape.
The local ONNX path uses the dedicated `tests/docker-compose.yml` `valkey-onnx` profile. It builds the server with CGO enabled by default, copies the repository `./models` directory into the image, switches `ENV=docker-onnx`, and runs pytest against the in-container model directory `/app/models/all-MiniLM-L6-v2`.
The model artifacts are expected to be stored in Git LFS. After clone, fetch them with `git lfs pull` if your checkout did not already do that.
Before committing model updates, install Git LFS locally and run `git lfs install`; otherwise raw binaries can end up in normal git history instead of LFS objects.

## Roadmap

- [ ] `vecdex-cli` — command-line client
- [ ] Performance & latency benchmarks
- [ ] Demo use cases (RAG, code search, etc.)
- [x] Valkey full-text search (Valkey Search 1.2+)
- [ ] AWS ElastiCache backend
- [ ] PostgreSQL + pgvector backend
- [ ] Multi-tenancy / namespaces
- [ ] Reranking support
- [ ] Additional embedding providers
- [ ] Admin UI
- [ ] Helm chart for Kubernetes

## License

[Apache License 2.0](LICENSE)

## Contributing

Contributions are welcome! Please open an issue first to discuss what you'd like to change.

```bash
just build          # Build + fmt + vet
just lint           # Run linter
just test-unit      # Fast unit tests
just pre-commit     # Full pre-commit check
```


### Amazon Bedrock — Cohere Embed v4

Copy `config/bedrock.yaml.example` to `config/local.yaml` and export
`AWS_BEARER_TOKEN_BEDROCK`, `AWS_REGION` (default `us-east-1` in the example),
and `VECDEX_API_KEY` before starting the server. Use a region where your key and
model are available. The Bedrock key stays on the server; HTTP clients use the
Vecdex key.

The adapter uses `cohere.embed-v4:0`, 1024 dimensions, explicit Bearer
credentials, and separate document/query input types. Text is sent with
`truncate: NONE`. Native batches contain at most 96 inputs. AWS profiles,
access keys and instance credentials are not consulted.

Token usage comes from Bedrock's `X-Amzn-Bedrock-Input-Token-Count` response
header. When it is absent, the Vecdex response has `X-Embedding-Usage: unknown`
and omits `X-Embedding-Tokens`. Cohere Embed v4 does not support the Bedrock
`CountTokens` API.

The opt-in live transport test makes a small paid inference call:

```bash
VECDEX_BEDROCK_LIVE=1 AWS_REGION=us-east-1 go test ./internal/transport/bedrock -run '^TestLiveBedrock$' -v -count=1
```
