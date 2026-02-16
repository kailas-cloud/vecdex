# vecdex justfile

# Show available recipes
default:
    @just --list

# Run unit tests (no Valkey required)
test-unit:
    go test ./... -short -v

# Run all tests
test: test-unit

# Start Valkey for local development
valkey-up:
    docker-compose up -d valkey
    @echo "Waiting for Valkey..."
    @sleep 3
    @echo "✓ Valkey started at localhost:6379"

# Stop Valkey
valkey-down:
    docker-compose down -v

# Check Valkey connection
valkey-ping:
    redis-cli -h localhost -p 6379 ping

# List loaded Valkey modules
valkey-modules:
    redis-cli -h localhost -p 6379 MODULE LIST

# Clean test cache and containers
clean:
    go clean -testcache
    docker-compose down -v

# Generate API code from OpenAPI spec
generate:
    cd api && go run github.com/oapi-codegen/oapi-codegen/v2/cmd/oapi-codegen --config=oapi-codegen.yaml openapi.yaml
    @echo "✓ Generated internal/transport/generated/api.gen.go"

# Build vecdex binary into build/
build:
    @mkdir -p build
    go build -o build/vecdex ./cmd/vecdex
    go fmt ./...
    go vet ./...

# Run linter
lint:
    golangci-lint run ./...

# Generate coverage report (excludes generated code)
coverage:
    go test $(go list ./... | grep -v /transport/generated) -coverprofile=coverage.out -covermode=atomic
    go tool cover -html=coverage.out -o coverage.html
    @echo "✓ Coverage report: coverage.html"

# Run months example (requires running vecdex on localhost:8080)
example-months:
    go run ./examples/months/

# Serve API docs (Redoc) at localhost:9090
docs:
    @echo "Opening http://localhost:9090/docs.html"
    @cd api && python3 -m http.server 9090

# Run pytest E2E suite against Valkey backend
test-pytest-valkey:
    cd tests && docker compose --profile valkey up --build --abort-on-container-exit --exit-code-from pytest-valkey
    cd tests && docker compose --profile valkey down -v

# Run pytest E2E suite against Redis backend
test-pytest-redis:
    cd tests && docker compose --profile redis up --build --abort-on-container-exit --exit-code-from pytest-redis
    cd tests && docker compose --profile redis down -v

# Run pytest E2E suite against both backends sequentially
test-pytest: test-pytest-valkey test-pytest-redis

# Clean pytest containers and volumes
clean-pytest:
    cd tests && docker compose --profile valkey down -v
    cd tests && docker compose --profile redis down -v

# Dry-run GoReleaser (snapshot build, no publish)
release-dry:
    goreleaser release --snapshot --clean

# Quick check before commit
pre-commit: build lint test-unit
    @echo "✓ Ready to commit"
