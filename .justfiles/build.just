# Build recipes

[private]
default:
    @just --list --unsorted --justfile {{source_file()}}

# Build debug binary
[no-cd]
debug:
    go build -gcflags="all=-N -l" -o bin/samantha ./cmd/samantha

# Build release binary (stripped, optimized)
[no-cd]
release:
    go build -ldflags="-s -w" -o bin/samantha ./cmd/samantha

# Cross-compile for Linux amd64
[no-cd]
linux:
    GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o bin/samantha-linux-amd64 ./cmd/samantha

# Show binary size
[no-cd]
size: release
    @ls -lh bin/samantha
