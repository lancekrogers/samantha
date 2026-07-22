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

[doc('Release (tag, publish, formula sha)')]
mod release '.justfiles/release.just'

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

# Build and record the README demo from the real Bubble Tea TUI. The tape
# writes a raw VHS GIF; the final pass matches termcast's compact, low-noise
# terminal output and replaces it with the optimized artifact.
demo: build
    #!/usr/bin/env bash
    set -euo pipefail
    vhs demos/tool-calls.tape
    just _optimize-demo-gif demos/tool-calls.gif

# Voice meter animation demo (listening / hearing / speaking).
# Matches festival/termcast tapes: shell-exported color env, raw VHS GIF.
# Important: clear NO_COLOR from the parent env — agent/CI shells often set it
# and lipgloss then emits monochrome frames.
demo-voice-meter: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/voice-meter.tape
    ls -lh demos/voice-meter.gif

# Managed Qwen provider selection + nine model-native voices in Settings.
# Uses a disposable completion-marker fixture; no model download is required.
demo-qwen-voices: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/qwen-voices.tape
    just _optimize-demo-gif demos/qwen-voices.gif
    ls -lh demos/qwen-voices.gif

# Settings → Persona: list seeded agents and switch active persona.
demo-persona-switch: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/persona-switch.tape
    just _optimize-demo-gif demos/persona-switch.gif
    ls -lh demos/persona-switch.gif

# Opt-in live managed-Qwen gate. Requires the managed model to have already
# been installed with `samantha models ensure --tts`; outputs remain available
# for listening instead of disappearing with the Go test temp directory.
qwen-live:
    #!/usr/bin/env bash
    set -euo pipefail
    cache="${XDG_CACHE_HOME:-$HOME/.cache}"
    out="${SAMANTHA_QWEN_SMOKE_DIR:-$cache/samantha/smoke/qwen}"
    mkdir -p "$out"
    SAMANTHA_QWEN_REAL_MODEL=1 SAMANTHA_QWEN_SMOKE_DIR="$out" \
        go test -tags=integration -count=1 -timeout 30m -v ./internal/tts \
        -run '^TestLiveManagedQwenVoiceMatrix$'
    echo "Qwen smoke WAVs: $out"

# Main launcher + Meeting recorder (notes, ★ bookmarks, voice EQ). Full color.
demo-meeting: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/meeting.tape
    ls -lh demos/meeting.gif

# Calibre Library browser/viewer (browse → detail → search → audiobook).
# Uses demos/fixtures/fake-calibredb so recording does not need a real library.
demo-library: build
    #!/usr/bin/env bash
    set -euo pipefail
    chmod +x demos/fixtures/fake-calibredb
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/library.tape
    just _optimize-demo-gif demos/library.gif
    ls -lh demos/library.gif

# Meeting route picker + Speaker settings (camp discovery UX). Full color.
# Uses demos/fixtures/camp so recording does not need a real camp registry.
demo-meeting-route-speaker: build
    #!/usr/bin/env bash
    set -euo pipefail
    chmod +x demos/fixtures/camp
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/meeting-route-speaker.tape
    ls -lh demos/meeting-route-speaker.gif

# Multi-voice meeting conversation + speaker analysis status (VHS).
demo-meeting-speakers: build
    #!/usr/bin/env bash
    set -euo pipefail
    env -u NO_COLOR -u CLICOLOR \
        CLICOLOR_FORCE=1 FORCE_COLOR=1 \
        TERM=xterm-256color COLORTERM=truecolor \
        vhs demos/meeting-speakers.tape
    ls -lh demos/meeting-speakers.gif

# Download the multi-speaker YouTube meeting clip (first 90s only, 16 kHz mono).
# Source: https://www.youtube.com/watch?v=lBVtvOpU80Q (~43 min full video — we
# only pull 0:00–1:30). Stored in a shared user cache so worktrees reuse it:
#   ${XDG_CACHE_HOME:-$HOME/.cache}/samantha/fixtures/meetings/
# Override with SAMANTHA_FIXTURE_CACHE=… or FORCE_FETCH=1 to re-download.
fetch-meeting-fixture:
    #!/usr/bin/env bash
    set -euo pipefail
    chmod +x scripts/fetch-meeting-fixture.sh
    ./scripts/fetch-meeting-fixture.sh

# Diarization integration against real multi-voice meeting audio.
#
# 1. Ensures the shared-cache YouTube fixture (auto-fetch once if missing)
# 2. Runs tests/speakerflow
# 3. Refreshes demos/meeting-speakers.gif when `vhs` is installed
#    (skip with SPEAKERFLOW_SKIP_VHS=1)
#
# Also available as: just test speakerflow  (and as part of just test full)
speakerflow:
    #!/usr/bin/env bash
    set -euo pipefail
    # Resolve shared cache path (same rules as scripts/fetch-meeting-fixture.sh).
    if [[ -n "${SAMANTHA_MEETING_FIXTURE:-}" ]]; then
        fixture="$SAMANTHA_MEETING_FIXTURE"
    else
        if [[ -n "${SAMANTHA_FIXTURE_CACHE:-}" ]]; then
            cache="${SAMANTHA_FIXTURE_CACHE}"
        elif [[ -n "${XDG_CACHE_HOME:-}" ]]; then
            cache="${XDG_CACHE_HOME}/samantha/fixtures/meetings"
        else
            cache="${HOME}/.cache/samantha/fixtures/meetings"
        fi
        fixture="${cache}/product-marketing-meeting-90s.wav"
    fi
    if [[ ! -f "$fixture" ]]; then
        echo "shared fixture missing — fetching once into cache…"
        just fetch-meeting-fixture
    fi
    if [[ ! -f "$fixture" ]]; then
        echo "fixture still missing after fetch: $fixture" >&2
        exit 1
    fi
    echo "==> speakerflow: using $fixture"
    echo "==> speakerflow: integration tests"
    go test -tags=integration -count=1 -timeout 3m -v ./tests/speakerflow/...
    if [[ "${SPEAKERFLOW_SKIP_VHS:-}" == "1" ]]; then
        echo "==> speakerflow: skipping VHS (SPEAKERFLOW_SKIP_VHS=1)"
        exit 0
    fi
    if ! command -v vhs >/dev/null 2>&1; then
        echo "==> speakerflow: vhs not installed — skip meeting-speakers.gif refresh"
        echo "    install vhs to auto-update demos/meeting-speakers.gif"
        exit 0
    fi
    echo "==> speakerflow: refreshing demos/meeting-speakers.gif"
    just demo-meeting-speakers

[private]
_optimize-demo-gif path:
    #!/usr/bin/env bash
    set -euo pipefail
    gif="{{path}}"
    palette="$(mktemp /tmp/samantha-demo-palette.XXXXXX.png)"
    optimized="$(mktemp /tmp/samantha-demo-output.XXXXXX.gif)"
    trap 'rm -f "$palette" "$optimized"' EXIT
    # Optional compact pass for README weight — prefer full palette.
    ffmpeg -y -loglevel error -i "$gif" \
        -vf "fps=20,scale=1000:-1:flags=lanczos,palettegen=max_colors=256:stats_mode=full" \
        "$palette"
    ffmpeg -y -loglevel error -i "$gif" -i "$palette" \
        -lavfi "fps=20,scale=1000:-1:flags=lanczos,paletteuse=dither=bayer:bayer_scale=3:diff_mode=rectangle" \
        "$optimized"
    mv "$optimized" "$gif"
    trap - EXIT
    rm -f "$palette"
    ls -lh "$gif"

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

# Full pipeline: clean, build, then the complete test suite
# (unit + Docker integration + voiceflow + speakerflow/VHS + audio-crackle).
all:
    just clean
    just build
    just test full

# Clean build artifacts
clean:
    @{{BUILDTOOL}} clean
