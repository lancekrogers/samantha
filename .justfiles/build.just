# Build recipes — dev, release, cross-platform
binary_name := "samantha"
main_path := "cmd/samantha/main.go"
bin_dir := "bin"

[private]
default:
    @just --list --unsorted --justfile {{source_file()}}

# Quick development build (no optimization)
[no-cd]
dev:
    go build -o {{bin_dir}}/{{binary_name}} {{main_path}}

# Release build (stripped, optimized)
[no-cd]
release:
    go build -ldflags="-s -w" -o {{bin_dir}}/{{binary_name}} {{main_path}}

# Debug build (with symbols)
[no-cd]
debug:
    go build -gcflags="all=-N -l" -o {{bin_dir}}/{{binary_name}} {{main_path}}

# Build for Linux AMD64
[no-cd]
linux:
    GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" -o {{bin_dir}}/{{binary_name}}-linux-amd64 {{main_path}}

# Build for macOS Apple Silicon
[no-cd]
darwin-arm64:
    GOOS=darwin GOARCH=arm64 go build -ldflags="-s -w" -o {{bin_dir}}/{{binary_name}}-darwin-arm64 {{main_path}}

# Build all platforms
[no-cd]
all: linux darwin-arm64
    @echo "All platform builds complete"

# Show binary size
[no-cd]
size: release
    @ls -lh {{bin_dir}}/{{binary_name}}
