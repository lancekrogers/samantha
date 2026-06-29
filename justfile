#!/usr/bin/env just --justfile
# Samantha — ultra-low-latency voice assistant for AI coding

set dotenv-load := true

BUILDTOOL := "go run ./internal/buildutil"
binary_name := "samantha"
bin_dir := "bin"
gobin := env_var_or_default("GOBIN", `go env GOPATH` + "/bin")

# Modules
[doc('Testing (unit, integration, dashboard)')]
mod test '.justfiles/test.just'

[doc('Voice & audio setup')]
mod voice '.justfiles/voice.just'

# Flat dev utilities
import '.justfiles/dev.just'

[private]
default:
    #!/usr/bin/env bash
    echo "Samantha — Give Claude a voice (Go)"
    echo ""
    just --list --unsorted

# Build (vet + compile via dashboard)
build:
    @{{BUILDTOOL}} build

# Build, sign, install to $GOBIN
install: build
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p {{gobin}}
    cp {{bin_dir}}/{{binary_name}} {{gobin}}/{{binary_name}}
    if [[ "$(uname)" == "Darwin" ]]; then
        codesign --force --sign - {{gobin}}/{{binary_name}} 2>/dev/null || \
        echo "Warning: Could not sign binary (non-fatal)"
    fi
    echo "samantha installed to {{gobin}}/{{binary_name}}"

# Build and run with any args
run *ARGS: build
    ./{{bin_dir}}/{{binary_name}} {{ARGS}}

# Talk to Samantha (full voice mode)
talk: build
    ./{{bin_dir}}/{{binary_name}}

# Full pipeline (clean, build, test, integration)
all:
    just clean
    just build
    just test unit
    just test integration

# Clean build artifacts
clean:
    @{{BUILDTOOL}} clean
