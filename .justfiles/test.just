# Testing
[private]
default:
    @just --list --unsorted --justfile {{source_file()}}

# Run all tests
[no-cd]
all:
    go test ./... -v

# Run tests for a specific package
[no-cd]
pkg PACKAGE:
    go test ./internal/{{PACKAGE}}/... -v
