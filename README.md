<p align="center"><img src="docs/vecdex-banner.png" alt="Vecdex" width="760"></p>

# Vecdex

Vecdex is a memory engine for agents, backed by Valkey Search. Its external interface is MCP over Streamable HTTP at `/mcp`. Agents can discover collections, keep durable working contexts, search and read sources, and make conditional changes. The embedded Go SDK provides batch ingestion and typed indexing.

This is a breaking transport change: REST, OpenAPI and `sdk.NewHTTP` have been removed. Existing v3 source data stays compatible. Working contexts use a separate namespace.

## Start the server

Use Go **1.27.3** and the official Valkey Bundle **9.1.2** image pinned in Compose (Valkey 9.1.1, Search 1.2.1 at runtime). The required Search workarounds and production deployment limits are in the [operations guide](docs/operations.md).

```sh
go mod download
go build -o build/vecdex ./cmd/vecdex
cp config/local.yaml.example config/local.yaml
# Set the embedding and Valkey configuration and credentials in config/local.yaml.
ENV=local ./build/vecdex
```

Bearer credentials have stable identities. Rotating a key while retaining `principal_id` preserves access to that principal's contexts. Configure at least one credential; the server refuses an empty or ambiguous credential set.

```yaml
auth:
  tokens:
    - principal_id: team-agent
      role: write
      key: ${VECDEX_AGENT_KEY}
    - principal_id: administrator
      role: admin
      key: ${VECDEX_ADMIN_KEY}
```

`read` permits memory reads and management of owned contexts. `write` adds document changes. `admin` adds collection changes. All MCP calls and resource reads enforce these roles. `/health` and `/metrics` remain operational HTTP endpoints; restrict their network exposure at the deployment boundary as needed.

## Connect an agent

Configure a Streamable HTTP MCP server with URL `http://localhost:8080/mcp` and `Authorization: Bearer <key>`. The primary protocol is `2026-07-28`. Clients using `2025-11-25` get the base tools, resources, prompts and completions. Skills and subscriptions require the modern profile.

The server uses the official Go MCP SDK **v1.8.0** with stateless transport. State belongs to explicit durable `context_id` handles; there is no implicit current collection or replica-bound session.

A normal workflow is:

1. Use `collection_list` and `collection_get` to choose a collection and inspect its schema.
2. Resume an owned context with `context_list` / `context_get`, or call `context_create`.
3. Pass `context_id` to memory tools. Search returns short source hits; read selected IDs for the evidence.
4. Create with a stable ID and idempotency key. Update or delete with the current `if_version` and an idempotency key.
5. Verify a write by reading its ID and committed version. Search indexing can lag source commits.

| Tools | Purpose |
| --- | --- |
| `memory_search`, `memory_read`, `memory_list`, `memory_similar` | Find and read source documents |
| `memory_create`, `memory_update`, `memory_delete` | Create without overwrite and make conditional changes |
| `collection_list`, `collection_get`, `collection_create`, `collection_delete` | Discover schemas and manage collection generations |
| `context_list`, `context_create`, `context_get`, `context_update`, `context_delete` | Save and restore owned working contexts |
| `memory_status` | Inspect health, limits and embedding usage |

The [MCP contract](docs/mcp-contract.md) explains versions, limits, resources and recovery. The [months example](examples/months) uses the official Go MCP client. Verified clients are Codex CLI **0.167.0**, Python `mcp==2.3.0` and `strands-agents==1.64.0`. See the [validation record](docs/validation.md) for results and limits, and [tests](tests/README.md) for reproducible commands.

## Skills and resources

Four skills ship with the server: `memory-use`, `memory-maintenance`, `memory-handoff`, and `memory-administration`. They cover source-based retrieval, conditional maintenance, attributed handoffs, and collection administration. The modern Skills extension and the installable archive use the same files.

```sh
./scripts/package-skills.sh
# Extract build/vecdex-skills.tar.gz into the skill directory of your agent host.
```

Resources expose the guide, limits, collection catalog and schemas, current documents, owned contexts and usage. Prompts `resume_context` and `review_memory` guide common workflows. Tools provide every required action even when a client does not support these extra surfaces.

A summary is an ordinary agent-authored document. Context pins store only IDs and versions. Read original evidence when a summary omits details; a stale version detects change but does not retrieve history. Plans are not completed actions, assistant advice is not a user statement, and top-k results are not exhaustive counts. These rules incorporate lessons from [Mastra Observational Memory](https://mastra.ai/docs/memory/observational-memory) without adding an automatic Observer or Reflector.

## Embedded Go SDK

Import `github.com/kailas-cloud/vecdex/pkg/sdk` for in-process ingestion and indexing. `NewEmbedded`, `NewIndex[T]`, `NewTextIngestor`, batch writes, source fragments and vectors remain available. No remote HTTP SDK backend is shipped. See [SDK contracts](docs/sdk.md).

The shared server runtime keeps separate document/query embedders, cache, shared token accounting, embedding-space checks and storage recovery. OpenAI-compatible, local ONNX and Bedrock providers remain supported. Provider credentials stay on the server. Context creation and metadata-only changes do not invoke embeddings.

## Validate

```sh
go test ./... -short
go vet ./...
golangci-lint run ./...
go build -o build/vecdex ./cmd/vecdex
```

Use golangci-lint **2.15.0**. Run the Valkey, ONNX and Bedrock MCP E2E profiles separately:

```sh
cd tests
docker compose --profile valkey up --build --abort-on-container-exit --exit-code-from pytest-valkey
docker compose --profile valkey down -v
# Other profiles: valkey-onnx / pytest-valkey-onnx, valkey-bedrock / pytest-valkey-bedrock.
```

Set `VECDEX_TEST_VALKEY_ADDR=127.0.0.1:16379` for native transaction, expiry, context and no-scripting regressions. Cluster fixtures and known engine limitations are documented in [tests/cluster](tests/cluster/README.md). Rate limiting is not implemented. Search does not offer snapshot pagination, exhaustive match counts, a proven indexing barrier or cross-collection retrieval.

MCP Tasks, Apps, elicitation, OAuth, document history, new ingest pipelines and automatic observers are outside this release. Do not infer client or performance guarantees beyond the recorded integration results.
