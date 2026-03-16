# Testing
BUILDTOOL := "go run ./internal/buildutil"

[private]
default:
    @just --list --unsorted --justfile {{source_file()}}

# Run all unit tests with dashboard
[no-cd]
all:
    @{{BUILDTOOL}} test

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
    CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) go build -o bin/linux/samantha ./cmd/samantha
    echo "Running container-based integration tests..."
    DOCKER_HOST="unix://${HOME}/.colima/default/docker.sock" \
    TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock \
    go test -v -tags=integration ./tests/integration/... -timeout 5m 2>&1
