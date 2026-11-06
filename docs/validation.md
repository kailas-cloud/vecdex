# MCP v2 validation

Validated on 2026-11-06. The [integration manifest](../tests/integration-manifest.json) records exact versions and observed results. These are local checks of the working tree; no release tag or package has been published.

## Build and engine checks

The required `go test ./... -short`, `go vet ./...`, `golangci-lint run ./...` and `go build -o build/vecdex ./cmd/vecdex` passed with Go 1.27.3 and golangci-lint 2.15.0. The lint result was zero issues.

Native tests used an isolated official Valkey Bundle fixture. They covered conditional commits, create-only writes, receipts after deletion, generation fences, projections, contexts, expiry and an ACL that denies scripting. The cluster checks passed for native partition catalogs, key placement, interrupted topology recovery and fork windows. The live SDK/MCP contract passed: an SDK update invalidated an MCP subscription, and another replica restored the context and read the current source.

All 17 documented examples validate against the checked-in input schemas. All four packaged skills pass the skill validator. Transport tests cover the exact two protocol profiles, role denials, owner isolation, both required prompt names, output schemas, byte budgets, long cursors, Unicode, cancellation, Origin checks and subscriptions.

## Provider contracts

| Docker profile | Passed | Skipped | Suite time |
| --- | ---: | ---: | ---: |
| `valkey` | 100 | 4 | 69.15 s |
| `valkey-onnx` | 97 | 7 | 43.07 s |
| `valkey-bedrock` | 102 | 2 | 38.51 s |

All profiles ran through MCP. Skips select provider-specific mock behavior, replica accounting, incompatible embedding spaces or the isolated budget fixture. These counts are not application failures hidden behind xfail. The [migration map](../tests/MIGRATION.md) identifies assertions retained in the embedded SDK for batch and ingest.

Reports from this run are `tests/reports/report-valkey.html`, `report-onnx.html` and `report-bedrock.html`. Reports are ignored build artifacts. See [test commands](../tests/README.md) to reproduce them.

## Client results

Codex CLI 0.167.0 with the account's `gpt-6-astra` model completed the fixed workflow: create sources, read by version in chunks, follow list cursors, perform a conditional update, observe a stale-version conflict, save a source-based handoff and verify a pinned version. It also loaded the published skill resource. The recorded run made 22 MCP calls in 79.58 seconds; normalized client results occupied 23,610 bytes. The runner retained the normal `on-request` approval policy, automatic review and a read-only shell sandbox.

Strands 1.64.0 with Python MCP 2.3.0 passed using the real `Agent`, `MCPClient` and `AgentSkills` adapters with a deterministic model. Its workflow made eight MCP calls and one skill activation. The measured agent run took 0.0903 seconds and delivered 9,953 bytes of tool-result blocks, including skill instructions, to the model callback. The separate official Python MCP client test also passed.

These are single compatibility observations on different workflows. They do not establish model reasoning quality or comparative performance. Codex token counts include host instructions; its byte count uses a different representation from Strands. The [client guide](../tests/clients/README.md) explains the metrics, verifier boundaries and reproducible commands.

## Protocol conformance scope

The pinned official harness commit `7169291ec0b68eb370fddcd9947313ab0d5e4156` (0.2.0-alpha.11) produced 93 successful checks and one informational result across the two profiles and separate Skills scenarios. Four checks remain explicit fixture limitations because they require upstream artificial tools absent from the product; two optional notification checks are skipped because those capabilities are not advertised.

This is a passing scoped run, not full conformance certification. Catalog checks in that harness inspect the first page; product tests separately traverse discovery and assert all 17 tools. Raw upstream statuses are retained. The [conformance manifest](../tests/conformance/manifest.json) lists every selected scenario, excluded group and allowed fixture limitation. Authorization, product resource reads, progress, cancellation and polling behavior have separate transport/server/E2E contracts.

Rate limiting and the documented Search/cluster limits remain unchanged. No competitive benchmark claim follows from these checks.
