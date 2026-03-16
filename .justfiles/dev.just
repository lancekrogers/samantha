# Development recipes
binary_name := "samantha"
bin_dir := "bin"

[private]
default:
    @just --list --unsorted --justfile {{source_file()}}

# Run with go run (no build step)
[no-cd]
run *ARGS:
    go run ./cmd/samantha {{ARGS}}

# Watch for changes and rebuild (requires entr)
[no-cd]
watch:
    find . -name '*.go' | entr -r go run ./cmd/samantha
