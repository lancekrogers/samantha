# Development recipes

[private]
default:
    @just --list --unsorted --justfile {{source_file()}}

# Run with args
[no-cd]
run *ARGS:
    go run ./cmd/samantha {{ARGS}}

# Run tests with coverage
[no-cd]
coverage:
    go test ./... -coverprofile=coverage.out
    go tool cover -html=coverage.out -o coverage.html
    @echo "Coverage report: coverage.html"

# Watch for changes and rebuild (requires entr)
[no-cd]
watch:
    find . -name '*.go' | entr -r go run ./cmd/samantha
