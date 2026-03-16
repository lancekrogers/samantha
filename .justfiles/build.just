# Build variants
binary_name := "samantha"
main_path := "cmd/samantha/main.go"
bin_dir := "bin"

[private]
default:
    @just --list --unsorted --justfile {{source_file()}}

# Release build (stripped)
[no-cd]
release:
    go build -ldflags="-s -w" -o {{bin_dir}}/{{binary_name}} {{main_path}}

# Show binary size
[no-cd]
size: release
    @ls -lh {{bin_dir}}/{{binary_name}}
