# Voice & audio setup recipes

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

# Set TTS voice
[no-cd]
set-voice VOICE:
    go run ./cmd/samantha config tts_voice {{VOICE}}

# Set speech speed
[no-cd]
set-speed SPEED:
    go run ./cmd/samantha config speech_speed {{SPEED}}

# Download required models
[no-cd]
models:
    go run ./cmd/samantha models download
