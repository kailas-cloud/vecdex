# MCP contract tests

The public agent interface is `/mcp`. These tests call MCP tools directly and
check both structured results and the JSON text delivered to agents. `/health`
and `/metrics` remain operator endpoints. Removed REST paths must return 404.

From `tests/`, with Docker Compose available:

```bash
uv sync --frozen
docker compose --profile valkey up --build --abort-on-container-exit --exit-code-from pytest-valkey
docker compose --profile valkey down -v
```

For ONNX, use profile `valkey-onnx` and exit-code service `pytest-valkey-onnx`;
for Bedrock, use `valkey-bedrock` and `pytest-valkey-bedrock`. Run profiles
separately and remove the disposable volumes between runs, or use separate
Compose project names. The Bedrock profile uses a TLS mock with public test
credentials and checks provider purpose, unknown token usage, cache behavior
and shared accounting across replicas. Container runs write
`reports/report.html`; local pytest needs `--html=reports/report.html` to produce
that report. The profiles share this report path.

Against an already running fixture:

```bash
VECDEX_BASE_URL=http://localhost:8080 VECDEX_API_KEY=test-api-key uv run --frozen pytest -v
```

The default reader principal is `test-read-key`; override it with
`VECDEX_READ_API_KEY`. Optional URLs enable specific fixture tests:
`VECDEX_PEER_URL`, `VECDEX_INCOMPATIBLE_URL`, `VECDEX_BEDROCK_MOCK_URL`,
`VECDEX_BUDGET_URL` (isolated one-token budget).
`VECDEX_EMBEDDING_BACKEND=onnx` skips the mock-only failure marker.

`mcp==2.3.0` and `strands-agents==1.64.0` are exact direct dependencies;
`uv.lock` locks the full environment. The Python MCP SDK test uses
`ClientSession.discover()` for `2026-07-28`; `initialize()` is the legacy
handshake in MCP 2.3.0. Raw wire tests also cover the explicit `2025-11-25`
fallback.
The Strands test uses the actual `Agent` and `MCPClient` against the server with
a deterministic model. Each next call is built from the tool result the model
receives: source version, byte offset, catalog cursor and a version conflict.
The same test fetches the published skill manifest and files, loads them through
Strands `AgentSkills`, activates `memory-use`, and checks that the model receives
the exact instructions before using memory tools. It verifies adapter
compatibility, not LLM reasoning quality.

Semantic fixtures preserve questions, intentions, observed outcomes, negation
and corrections with source references. They do not turn Vecdex into an
automatic observation or reflection pipeline. An LLM extraction evaluation
needs a separate model and scoring configuration.

See [MIGRATION.md](MIGRATION.md) for the old-to-new assertion mapping and the
contracts retained in the embedded Go SDK.

The Codex scenario is a separate opt-in run with an actual model. See
[agent client checks](clients/README.md) for its pinned version, commands,
verification limits and Strands measurement output.
