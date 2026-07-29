# Voice & audio setup
[private]
default:
    @just --list --unsorted --justfile {{source_file()}}

# Test mic and speaker
[no-cd]
test:
    go run ./cmd/samantha test

# List available TTS voices
[no-cd]
voices:
    go run ./cmd/samantha voices

# Show available providers
[no-cd]
providers:
    go run ./cmd/samantha providers

# Measure echo cancellation on real hardware (see docs/aec-probe.md)
[no-cd]
aec-probe LABEL="":
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Speaker at a normal listening level, no headphones, and stay quiet for ~20s."
    echo ""
    if [ -n "{{LABEL}}" ]; then
      go run ./cmd/samantha aec-probe --label "{{LABEL}}"
    else
      go run ./cmd/samantha aec-probe
    fi
