#!/usr/bin/env bash
# Capture `models status --json --all` from the real binary.
#
# These fixtures are the wire contract the Obey Voice Mac app's Assets section
# and Qwen tier picker decode (spec 55-models §3.2, §3.4). They are captured,
# never hand-written: a hand-written fixture proves the app can decode a payload
# samantha does not actually emit.
#
# Two configurations, because the acceptance run found the tier rows missing in
# exactly the first one:
#   models-status-kokoro-no-tiers.json  tts_provider: kokoro, nothing installed
#                                       -> no coarse tts.qwen3.native row, but
#                                          both tts.qwen3.tier.* rows, missing
#   models-status-qwen-both-tiers.json  tts_provider: qwen3-tts, a native
#                                       package carrying 0.6b and 1.7b
#                                       -> coarse row + both tier rows installed
#
# Usage: cmd/samantha/cmd/testdata/capture-models-fixtures.sh
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../../../.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

bin="$work/samantha"
(cd "$repo" && go build -o "$bin" ./cmd/samantha)

# Two throwaway HOMEs, one per fixture, so the real ~/.obey and ~/.cache are
# never touched and neither capture can influence the other. Separate roots
# matter: `models status` loads through the persona overlay, and the first run
# in a fresh install seeds a persona from whatever the config said at the time —
# capturing both fixtures under one HOME silently pinned the second one to the
# first one's TTS provider.
canonicalCache='/Users/lance/.cache/festival-voice'

# capture <home> <fixture>  — run the command and normalize this run's paths to
# a canonical home so the fixture is stable across machines.
capture() {
	HOME="$1" "$bin" models status --json --all |
		sed -e "s#$1/.cache/festival-voice#$canonicalCache#g" >"$here/$2"
}

# 1. The acceptance run's configuration: kokoro, nothing installed.
kokoroHome="$work/home-kokoro"
mkdir -p "$kokoroHome/.obey/agents/voice/festival-voice"
printf 'tts_provider: kokoro\n' >"$kokoroHome/.obey/agents/voice/festival-voice/config.yaml"
capture "$kokoroHome" models-status-kokoro-no-tiers.json

# 2. qwen3-tts with a native package holding both tiers. InspectNative verifies
# install.json's shape and that every file it names is present (presence-only —
# no hashing — so the digests below are placeholders, exactly as the real
# manifest's are irrelevant to a status read).
qwenHome="$work/home-qwen"
mkdir -p "$qwenHome/.obey/agents/voice/festival-voice"
printf 'tts_provider: qwen3-tts\n' >"$qwenHome/.obey/agents/voice/festival-voice/config.yaml"
native="$qwenHome/.cache/festival-voice/models/qwen3-tts"
mkdir -p "$native/bin" "$native/models/presets"
printf '#!/bin/sh\nexit 0\n' >"$native/bin/qwen3-tts-worker"
chmod +x "$native/bin/qwen3-tts-worker"
: >"$native/bin/libqwen3tts.dylib"
: >"$native/bin/libggml.dylib"
printf '{"voices":[]}\n' >"$native/models/presets/presets.json"
: >"$native/models/qwen3-tts-tokenizer-f16.gguf"
: >"$native/models/qwen3-tts-0.6b-f16.gguf"
: >"$native/models/qwen3-tts-1.7b-f16.gguf"
sha='0000000000000000000000000000000000000000000000000000000000000000'
cat >"$native/install.json" <<JSON
{
  "schema": "qwen3-tts-native.install.v1",
  "product": "qwen3-tts-native",
  "repo_commit": "0000000",
  "engine_sha": "$sha",
  "os": "$(go env GOOS)",
  "arch": "$(go env GOARCH)",
  "backend_hint": "metal",
  "tier_default": "0.6b",
  "sample_rate": 24000,
  "protocol": "qwen3-tts-worker/v1",
  "streaming": true,
  "bin": {"worker": "bin/qwen3-tts-worker", "worker_sha256": "$sha"},
  "models": {
    "0.6b": {"quant": "f16",
             "tts": {"path": "models/qwen3-tts-0.6b-f16.gguf", "sha256": "$sha"},
             "tokenizer": {"path": "models/qwen3-tts-tokenizer-f16.gguf", "sha256": "$sha"}},
    "1.7b": {"quant": "f16",
             "tts": {"path": "models/qwen3-tts-1.7b-f16.gguf", "sha256": "$sha"},
             "tokenizer": {"path": "models/qwen3-tts-tokenizer-f16.gguf", "sha256": "$sha"}}
  },
  "presets": "models/presets/presets.json",
  "presets_sha256": "$sha",
  "user_install": ""
}
JSON
capture "$qwenHome" models-status-qwen-both-tiers.json

echo "captured into $here"
