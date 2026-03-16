# Testing recipes
[private]
default:
    @just --list --unsorted --justfile {{source_file()}}

# Run all tests
[no-cd]
all:
    go test ./... -v

# Run tests with race detection
[no-cd]
race:
    go test ./... -race -v

# Run tests with coverage
[no-cd]
coverage:
    go test ./... -coverprofile=coverage.out
    go tool cover -html=coverage.out -o coverage.html
    @echo "Coverage report: coverage.html"

# Run tests for a specific package
[no-cd]
pkg PACKAGE:
    go test ./internal/{{PACKAGE}}/... -v
