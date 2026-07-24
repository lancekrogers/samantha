# Development utilities (imported flat into root)

# Lint and format (golangci-lint enabled set lives in .golangci.yml)
lint:
    #!/usr/bin/env bash
    set -euo pipefail
    go fmt ./...
    go vet ./...
    if command -v golangci-lint >/dev/null 2>&1; then
        golangci-lint run ./...
    else
        echo "golangci-lint not installed — skipping (install: https://golangci-lint.run/welcome/install/)"
    fi

# Update and tidy dependencies
deps:
    go get -u ./...
    go mod tidy

# Watch for changes and rerun (requires entr)
watch:
    find . -name '*.go' | entr -r go run ./cmd/samantha
