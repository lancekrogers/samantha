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
    # Accept both `just voice aec-probe macbook-builtin` and the LABEL=... form,
    # because just takes recipe arguments positionally and the second spelling
    # otherwise lands in metrics.json verbatim as "LABEL=macbook-builtin".
    label="{{LABEL}}"
    label="${label#LABEL=}"
    echo "Speaker at a normal listening level, no headphones, and stay quiet for ~20s."
    echo "Aim for echo_rms above 0.02 — quieter than that and the result is meaningless."
    echo ""
    if [ -n "$label" ]; then
      go run ./cmd/samantha aec-probe --label "$label"
    else
      go run ./cmd/samantha aec-probe
    fi
