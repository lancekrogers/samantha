#!/usr/bin/env just --justfile
# Samantha - Ultra-low-latency voice assistant for Claude Code

# Modules
[doc('Build options (release, cross-compile)')]
mod builds '.justfiles/build.just'

[doc('Development tasks')]
mod dev '.justfiles/dev.just'

[doc('Voice & audio setup')]
mod voice '.justfiles/voice.just'

[private]
default:
    #!/usr/bin/env bash
    echo "Samantha — Give Claude a voice (Go)"
    echo ""
    just --list --unsorted

# Build the binary
build:
    go build -o bin/samantha ./cmd/samantha

# Run samantha
run *ARGS: build
    ./bin/samantha {{ARGS}}

# Talk to Samantha (full voice mode)
talk: build
    ./bin/samantha

# Text mode (type + hear voice)
text: build
    ./bin/samantha --text

# Run all tests
test:
    go test ./... -v

# Run linter
lint:
    go vet ./...

# Install globally
install:
    go install ./cmd/samantha

# Clean build artifacts
clean:
    rm -rf bin/ dist/

# Show current config
config: build
    ./bin/samantha config

# Sync dependencies
deps:
    go mod tidy
