# Build variants
BUILDTOOL := "go run ./internal/buildutil"

[private]
default:
    @just --list --unsorted --justfile {{source_file()}}

# Build with vet + compile via dashboard
[no-cd]
default-build:
    @{{BUILDTOOL}} build

# Release build (stripped)
[no-cd]
release:
    @{{BUILDTOOL}} build-only

# Full pipeline (clean, build, test, integration)
[no-cd]
full:
    @{{BUILDTOOL}} all

# Clean build artifacts
[no-cd]
clean:
    @{{BUILDTOOL}} clean

# Show binary size
[no-cd]
size: release
    @ls -lh bin/samantha
