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
