# Install recipes
binary_name := "samantha"
bin_dir := "bin"
gobin := env_var_or_default("GOBIN", `go env GOPATH` + "/bin")

[private]
default:
    @just --list --unsorted --justfile {{source_file()}}

# Build and install to $GOBIN
[no-cd]
global:
    #!/usr/bin/env bash
    set -euo pipefail
    just quick
    just install _copy

# Install from current bin/ (whatever was last built)
[no-cd]
current:
    #!/usr/bin/env bash
    set -euo pipefail
    if [ ! -f {{bin_dir}}/{{binary_name}} ]; then
        echo "Error: no binary found at {{bin_dir}}/{{binary_name}}"
        echo "Run 'just quick' first"
        exit 1
    fi
    just install _copy

# Copy binary to $GOBIN and sign on macOS
[no-cd, private]
_copy:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Installing samantha..."
    mkdir -p {{gobin}}
    cp {{bin_dir}}/{{binary_name}} {{gobin}}/{{binary_name}}
    if [[ "$(uname)" == "Darwin" ]]; then
        echo "Signing samantha binary for macOS..."
        codesign --force --sign - {{gobin}}/{{binary_name}} 2>/dev/null || \
        echo "Warning: Could not sign binary (non-fatal)"
    fi
    echo "samantha installed to {{gobin}}/{{binary_name}}"
