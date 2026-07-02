# Testing
BUILDTOOL := "go run ./internal/buildutil"

[private]
default:
    @just --list --unsorted --justfile {{source_file()}}

# Run unit then integration tests (requires Docker)
[no-cd]
all: unit integration

# Run unit tests only with dashboard
[no-cd]
unit:
    @{{BUILDTOOL}} test

# Run tests for a specific package
[no-cd]
pkg PACKAGE:
    go test ./internal/{{PACKAGE}}/... -v

# Run integration tests with dashboard (requires Docker)
[no-cd]
integration:
    @{{BUILDTOOL}} integration

# Run integration tests with verbose output
[no-cd]
integration-verbose:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Building Linux binary for containers..."
    mkdir -p bin/linux
    CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) go build -tags=integration -o bin/linux/samantha ./cmd/samantha
    echo "Running container-based integration tests..."
    go test -v -tags=integration ./tests/integration/... -timeout 5m 2>&1

# Run fixture-audio voice flow tests (tagged integration, no Docker).
# Advisory until PR #27's interrupt-controller changes land.
[no-cd]
voiceflow:
    #!/usr/bin/env bash
    set -euo pipefail
    if ! go test -race -tags integration ./tests/voiceflow/...; then
        echo "::warning::voiceflow suite failed (advisory until PR #27 merges)"
    fi
