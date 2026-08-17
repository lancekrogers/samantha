#!/usr/bin/env bash
# Capture the config surface's --json payloads from the real binary.
#
# These fixtures are the wire contract between samantha and the Obey Voice Mac
# app: the app's decoding tests read copies of them. They are captured, never
# hand-written — a hand-written fixture proves the app can decode a payload
# samantha does not actually emit.
#
# Usage: cmd/samantha/cmd/testdata/capture-config-fixtures.sh
#
# The install root is a throwaway HOME so the real ~/.obey is never touched;
# the captured paths are then normalized to a canonical home so the fixture is
# stable across machines. Nothing else is edited.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../../../.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

bin="$work/samantha"
(cd "$repo" && go build -o "$bin" ./cmd/samantha)

export HOME="$work/home"
root="$HOME/.obey/agents/voice/festival-voice"
mkdir -p "$root"
cat >"$root/config.yaml" <<'YAML'
# Samantha settings.
tts_provider: kokoro

# Voice activity detection.
vad_silence_duration: 0.8
barge_in_enabled: true

speaker:
  live:
    window_ms: 1500
YAML

# An active persona, so config-get.json carries a real persona block: the Mac
# app's badge decoding must be tested against one samantha actually emits.
mkdir -p "$root/personas/ada"
cat >"$root/personas/ada/persona.yaml" <<'YAML'
schema: festival-voice.persona.v1
id: ada
display_name: Ada
brain:
  provider: ollama
  model: llama3.1
tts:
  provider: kokoro
  voice: af_sky
prompts:
  persona: ada
YAML
printf 'active_persona: ada\n' >>"$root/config.yaml"

# Two defaults (models_dir, whispercpp_model_path) live under the cache root
# rather than the install root, so both have to be normalized or the fixtures
# carry this run's mktemp path and churn on every re-capture.
canonical='/Users/lance/.obey/agents/voice/festival-voice'
canonicalCache='/Users/lance/.cache/festival-voice'
normalize() { sed -e "s#$root#$canonical#g" -e "s#$HOME/.cache/festival-voice#$canonicalCache#g"; }

"$bin" config schema --json | normalize >"$here/config-schema.json"
OLLAMA_MODEL=qwen2.5:14b "$bin" config get --json | normalize >"$here/config-get.json"
"$bin" config get vad_silence_duration --json | normalize >"$here/config-get-key.json"
"$bin" config set vad_silence_duration 0.9 --json | normalize >"$here/config-set-ok.json"
"$bin" config set vad_silence_duration 0.9 --json | normalize >"$here/config-set-noop.json"
# An error case exits 1 by design; the payload is what is being captured.
{ "$bin" config set vad_silence_duration fast --json 2>/dev/null || true; } | normalize >"$here/config-set-error.json"

# The backup path carries a capture timestamp; pin it so re-running the script
# does not churn the fixture. Written through a temp file rather than sed -i,
# whose in-place flag differs between BSD and GNU.
sed 's#config\.yaml\.bak\.[0-9TZ.]*#config.yaml.bak.20260817T031500.000000000Z#' \
	"$here/config-set-ok.json" >"$work/pinned.json"
mv "$work/pinned.json" "$here/config-set-ok.json"

echo "captured into $here"
