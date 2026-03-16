#!/usr/bin/env just --justfile
# Samantha — ultra-low-latency voice assistant for Claude Code

set dotenv-load := true

# Configuration
binary_name := "samantha"
bin_dir := "bin"
main_path := "cmd/samantha/main.go"

# Modules
[doc('Build variants (dev, release, cross-platform)')]
mod build '.justfiles/build.just'

[doc('Testing (unit, coverage)')]
mod test '.justfiles/test.just'

[doc('Voice & audio setup')]
mod voice '.justfiles/voice.just'

[doc('Install to $GOBIN')]
mod install '.justfiles/install.just'

[private]
default:
    #!/usr/bin/env bash
    echo "Samantha — Give Claude a voice (Go)"
    echo ""
    just --list --unsorted

# Quick build to bin/
quick:
    go build -o {{bin_dir}}/{{binary_name}} {{main_path}}

# Run samantha
run *ARGS: quick
    ./{{bin_dir}}/{{binary_name}} {{ARGS}}

# Talk to Samantha (full voice mode)
talk: quick
    ./{{bin_dir}}/{{binary_name}}

# Text mode (type + hear voice)
text: quick
    ./{{bin_dir}}/{{binary_name}} --text

# Format Go code
fmt:
    go fmt ./...

# Run linter
lint:
    go vet ./...

# Clean build artifacts
clean:
    rm -rf {{bin_dir}}/ dist/

# Show current config
config: quick
    ./{{bin_dir}}/{{binary_name}} config

# Update and tidy dependencies
deps:
    go get -u ./...
    go mod tidy
