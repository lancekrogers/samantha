#!/usr/bin/env bash
# Capture `models clean --unused --dry-run --json` from the real binary.
#
# This fixture is the wire contract between samantha and the Obey Voice Mac
# app's Clean sheet: the app's decoding tests read a copy of it. It is
# captured, never hand-written — a hand-written fixture proves the app can
# decode a payload samantha does not actually emit.
#
# Usage: cmd/samantha/cmd/testdata/capture-models-clean-fixture.sh
#
# The install root and the models dir are both under a throwaway HOME, so the
# real ~/.obey and ~/.cache/festival-voice are never touched. The captured
# paths are then normalized to a canonical home so the fixture is stable across
# machines; plan_id hashes models-dir-relative paths, so it survives that
# rewrite unchanged.
set -euo pipefail

here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo="$(cd "$here/../../../.." && pwd)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

bin="$work/samantha"
(cd "$repo" && go build -o "$bin" ./cmd/samantha)

export HOME="$work/home"
root="$HOME/.obey/agents/voice/festival-voice"
models="$HOME/.cache/festival-voice/models"
mkdir -p "$root" "$models"

# The 2026-08-17 machine: global TTS is kokoro, STT runs offline while a
# streaming model stays configured, and personas route speech elsewhere.
cat >"$root/config.yaml" <<'YAML'
tts_provider: kokoro
stt_provider: sherpa
stt_mode: offline
sherpa_streaming_model: en-2023-06-26
whisper_model: small
vad_enabled: true
active_persona: ada
speaker:
  enabled: true
  live:
    enabled: true
YAML

# Two personas, one pinned to the native Qwen3-TTS package the global config
# does not select — the exact shape that made 6 GB look "unused".
mkdir -p "$root/personas/ada" "$root/personas/veronica"
cat >"$root/personas/ada/persona.yaml" <<'YAML'
schema: festival-voice.persona.v1
id: ada
display_name: Ada
tts:
  provider: kokoro
  voice: af_sky
prompts:
  persona: ada
YAML
cat >"$root/personas/veronica/persona.yaml" <<'YAML'
schema: festival-voice.persona.v1
id: veronica
display_name: Veronica
tts:
  provider: qwen3-tts
  voice: Vivian
  tier: 0.6b
prompts:
  persona: veronica
YAML

# Installed assets: the native qwen package a persona speaks through, the VAD
# model, the configured streaming model the offline mode does not load, and the
# speaker embedding model.
mkdir -p "$models/qwen3-tts/bin" "$models/qwen3-tts/models" \
	"$models/sherpa-onnx-streaming-zipformer-en-2023-06-26" "$models/speaker"
printf 'worker\n' >"$models/qwen3-tts/bin/qwen3-tts-worker"
printf 'gguf\n' >"$models/qwen3-tts/models/qwen3-tts-0.6b-f16.gguf"
printf 'onnx\n' >"$models/sherpa-onnx-streaming-zipformer-en-2023-06-26/encoder-epoch-99-avg-1-chunk-16-left-128.int8.onnx"
printf 'tokens\n' >"$models/sherpa-onnx-streaming-zipformer-en-2023-06-26/tokens.txt"
printf 'onnx\n' >"$models/silero_vad.onnx"
printf 'onnx\n' >"$models/speaker/nemo_en_titanet_small.onnx"

# Removable: an interrupted download, an interrupted extraction, and a voice
# pack no config key or persona names any more.
mkdir -p "$models/.extract-9f2a" "$models/kokoro-v0.19"
printf 'partial\n' >"$models/.archive-8c1d.tar.bz2.part"
printf 'partial\n' >"$models/.extract-9f2a/model.onnx"
printf 'old\n' >"$models/kokoro-v0.19/model.onnx"
printf 'old\n' >"$models/kokoro-v0.19/voices.bin"

canonicalCache='/Users/lance/.cache/festival-voice'
"$bin" models clean --unused --dry-run --json |
	sed -e "s#$HOME/.cache/festival-voice#$canonicalCache#g" >"$here/models-clean-dry-run.json"

echo "captured into $here/models-clean-dry-run.json"
