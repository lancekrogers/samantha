# Testing
BUILDTOOL := "go run ./internal/buildutil"

[private]
default:
    @just --list --unsorted --justfile {{source_file()}}

# Unit + Docker integration only (CI-friendly core).
# Prefer `just test full` for the complete local suite.
[no-cd]
all: unit integration

# Full local suite: unit, Docker integration, voiceflow, speaker diarization
# (fixture + optional VHS), and audio crackle. Requires Docker for integration.
[no-cd]
full: unit integration voiceflow speakerflow audio-crackle
    @echo ""
    @echo "✓ just test full complete (unit + integration + voiceflow + speakerflow + audio-crackle)"

# Run unit tests only with dashboard
[no-cd]
unit:
    @{{BUILDTOOL}} test

# Run tests for a specific package
[no-cd]
pkg PACKAGE:
    go test ./internal/{{PACKAGE}}/... -v

# Run integration tests with dashboard (requires Docker)
[no-cd]
integration:
    @{{BUILDTOOL}} integration

# Run integration tests with verbose output
[no-cd]
integration-verbose:
    #!/usr/bin/env bash
    set -euo pipefail
    echo "Building Linux binary for containers..."
    mkdir -p bin/linux
    CGO_ENABLED=0 GOOS=linux GOARCH=$(go env GOARCH) go build -tags=integration -o bin/linux/samantha ./cmd/samantha
    echo "Running container-based integration tests..."
    go test -v -tags=integration ./tests/integration/... -timeout 5m 2>&1

# Run fixture-audio voice flow tests (tagged integration, no Docker).
[no-cd]
voiceflow:
    #!/usr/bin/env bash
    set -euo pipefail
    go test -race -tags integration ./tests/voiceflow/... -count=1 -timeout 5m

# Multi-voice meeting diarization (real YouTube fixture + optional VHS GIF).
# Delegates to the root recipe so fixture fetch / demo stay in one place.
[no-cd]
speakerflow:
    just speakerflow

# Software crackle + playback layout regression suite (no hardware).
# Pins Studio Display mono-client crackle class: stereo upmix, expand, and
# underrun/impulse metrics. Also covered by CI: go test -race ./...
[no-cd]
audio-crackle:
    go test -race ./internal/audio/ -count=1 -run 'Crackle|Expand|Choose|Pick|StudioDisplay|CallbackExpand|MultiChannelExpand|Whole|Chunk|FinalizeSegmentThen|Early|Pump|Analyze|Layout|DebugMetadataRecords'

# Live Kokoro regression: real model, real WAV sample counts for contractions.
# Requires Kokoro assets under models_dir (config.yaml / MODELS_DIR). Skips if missing.
# Pins: U+0329 token alias, no text mangling of wasn't/isn't/don't, full audio vs stock tokens.
[no-cd]
kokoro-contractions:
    go test -tags=integration ./internal/tts/ -count=1 -run 'LiveKokoro' -timeout 5m -v

# Opt-in hardware playback on the real default device (Studio Display, etc.).
# Requires speakers; plays a short tone. Use after any internal/audio change.
[no-cd]
audio-hardware:
    #!/usr/bin/env bash
    set -euo pipefail
    SAMANTHA_HARDWARE_AUDIO=1 go test -race ./internal/audio/ -count=1 -run 'Hardware' -v -timeout 60s
