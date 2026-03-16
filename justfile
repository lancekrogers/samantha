#!/usr/bin/env just --justfile
# Samantha — ultra-low-latency voice assistant for Claude Code

set dotenv-load := true

# Configuration
binary_name := "samantha"
bin_dir := "bin"
main_path := "cmd/samantha/main.go"

# Modules
[doc('Build variants (release, cross-platform)')]
mod build '.justfiles/build.just'

[doc('Testing (unit, race, coverage)')]
mod test '.justfiles/test.just'

[doc('Voice & audio setup')]
mod voice '.justfiles/voice.just'

[doc('Install to $GOBIN')]
mod install '.justfiles/install.just'

# Flat dev utilities
import '.justfiles/dev.just'

[private]
default:
    #!/usr/bin/env bash
    echo "Samantha — Give Claude a voice (Go)"
    echo ""
    just --list --unsorted

# Build to bin/
quick:
    go build -o {{bin_dir}}/{{binary_name}} {{main_path}}

# Build and run with any args
run *ARGS: quick
    ./{{bin_dir}}/{{binary_name}} {{ARGS}}

# Talk to Samantha (full voice mode)
talk: quick
    ./{{bin_dir}}/{{binary_name}}

# Clean build artifacts
clean:
    rm -rf {{bin_dir}}/ dist/ coverage.out coverage.html
