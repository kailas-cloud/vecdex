# Vecdex Agent Notes

## Project bootstrap

- This repository is a Go project with a Docker-based Python E2E test harness under `tests/`.
- Required local tools: `go` 1.27.3, `golangci-lint` 2.15.0, Docker with Compose, and optionally `just` for the helper recipes in `justfile`.
- On a fresh machine, run `go mod download` before linting if dependencies are not cached yet.
- The main binary is built from `./cmd/vecdex` and is written to `build/vecdex`.

## First local setup

```bash
go mod download
go build -o build/vecdex ./cmd/vecdex
```

If you want to use the repo helpers instead of raw commands:

```bash
just build
```

## Validation commands

- Lint: `golangci-lint run ./...`
- Unit tests: `go test ./... -short`
- Build: `go build -o build/vecdex ./cmd/vecdex`
- Vet: `go vet ./...`

Equivalent `just` targets:

- `just lint`
- `just test-unit`
- `just build`
- `just pre-commit`

## Local E2E tests

- The E2E suite runs through Docker Compose in `tests/docker-compose.yml`.
- Docker daemon access is required; these tests will not run in a restricted sandbox without Docker socket access.
- Supported backend: Valkey 9 + Valkey Search 1.2. Compose pins Valkey Bundle 9.1.2 by digest (Search 1.2.1).

```bash
cd tests
docker compose --profile valkey up --build --abort-on-container-exit --exit-code-from pytest-valkey
docker compose --profile valkey down -v
```

- The latest HTML pytest report is written to `tests/reports/report.html`.
- The `valkey-onnx` profile runs the same contract against ONNX (`pytest-valkey-onnx`).
- The `valkey-bedrock` profile runs a TLS Bedrock mock with two Vecdex replicas (`pytest-valkey-bedrock`). It uses a public test certificate and a dummy Bearer key; no AWS key is needed.
- Run profiles separately or use different Compose project names to isolate their data. Reports share the same output path.
- The Python suite uses MCP directly. Rate limiting is not implemented; the old REST-only placeholder xfails were removed. `tests/MIGRATION.md` maps preserved assertions to MCP or embedded SDK coverage.
- Set `VECDEX_TEST_VALKEY_ADDR=127.0.0.1:16379` to enable native transaction, lifecycle, catalog, durable contexts, cache TTL, and shared accounting regressions against an isolated local Valkey. These tests include an ACL that denies scripting.
- Set `VECDEX_TEST_VALKEY_CLUSTER_ADDRS` to a comma-separated list of reachable seed addresses to run `TestPartitionCatalogNativeLive` against an isolated cluster with at least three primaries. This fixture writes and removes its own generation namespace, verifies v3 key co-location, native commits, physical distribution, and catalog pagination; it does not cover Search, replicas, or resharding.

- `tests/cluster/README.md` describes the disposable Search fixture and `VECDEX_E2E_VALKEY_CLUSTER_ADDRS` SDK contracts. `VECDEX_E2E_CLUSTER_CONTAINER` explicitly opts into destructive topology changes on that fixture only. Review `tests/cluster/KNOWN_FAILURES.md` before interpreting scale-out results.

- `tests/conformance/manifest.json` pins the official MCP harness and records scoped coverage and fixture limitations for both protocol profiles and Skills.
- `internal/skills/bundle` is the single source for MCP skills and the installable archive (`scripts/package-skills.sh`).

## Practical notes

- Use only official public Valkey/Search releases and images. Do not patch or ship a custom engine build. Work around engine defects in Vecdex or supported configuration, and document remaining limits with native reproducers. Confirmed engine regressions may use strict xfail with evidence; application contracts must not be hidden by a blanket xfail.

- `go test ./... -short` currently includes the `examples/` packages; CI excludes them in the coverage job but they pass locally.
- On a fresh environment, `golangci-lint` may need network access once to resolve uncached Go modules.
- Do not assume E2E failures are unit-test failures; they exercise the full containerized stack with a mock embedder.

## Valkey scripting constraint

- Lua is allowed only for O(1) operations with at most 3–5 simple instructions.
- Never loop over document fields, fragments, keys, or request inputs in Lua.
- Document preparation, cleanup, budget calculations, and retries run in Go.
- Use native commands and WATCH/MULTI/EXEC for conditional commits. Keep document commit transactions constant in size.
