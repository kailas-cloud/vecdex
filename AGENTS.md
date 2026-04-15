# Vecdex Agent Notes

## Project bootstrap

- This repository is a Go project with a Docker-based Python E2E test harness under `tests/`.
- Required local tools: `go` 1.25.x, `golangci-lint`, Docker with Compose, and optionally `just` for the helper recipes in `justfile`.
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
- Supported backend: Valkey 9 + Valkey Search 1.2.

```bash
cd tests
docker compose --profile valkey up --build --abort-on-container-exit --exit-code-from pytest-valkey
docker compose --profile valkey down -v
```

- The latest HTML pytest report is written to `tests/reports/report.html`.

## Practical notes

- `go test ./... -short` currently includes the `examples/` packages; CI excludes them in the coverage job but they pass locally.
- On a fresh environment, `golangci-lint` may need network access once to resolve uncached Go modules.
- Do not assume E2E failures are unit-test failures; they exercise the full containerized stack with a mock embedder.
