#!/usr/bin/env bash
# Synthesize one golden sample using the Go code in the CURRENT directory.
#
#   scripts/golden-synth.sh <text> <out.wav>
#
# Text may be split into segments with "||". Each segment is synthesized as its
# own call and the PCM is concatenated, which is exactly what the pipeline does:
# every chunk is an independent synthesis call and the player concatenates raw
# buffers. That makes chunk-boundary changes audible — synthesizing a whole reply
# as one blob would hide the very thing a chunking A/B is testing.
#
# It deliberately does NOT cd anywhere. golden-ab.sh invokes it from inside a git
# worktree to produce the "before" side with an older checkout's synthesiser.
set -euo pipefail

if [[ $# -ne 2 ]]; then
  echo "usage: $0 <text> <out.wav>" >&2
  exit 2
fi

TEXT="$1"
OUT="$2"
VOICE="${GOLDEN_VOICE:-af_heart}"
SPEED="${GOLDEN_SPEED:-0.95}"

mkdir -p "$(dirname "$OUT")"

synth_one() {
  go run ./cmd/golden-tts \
    -text "$1" -voice "$VOICE" -speed "$SPEED" \
    -out "$2" -meta "${2%.wav}.meta.json" >/dev/null
}

if [[ "$TEXT" != *"||"* ]]; then
  synth_one "$TEXT" "$OUT"
  exit 0
fi

seg_dir="$(mktemp -d)"
trap 'rm -rf "$seg_dir"' EXIT

segs=()
i=0
while IFS= read -r seg; do
  [[ -z "${seg// /}" ]] && continue
  i=$((i + 1))
  synth_one "$seg" "$seg_dir/seg-$i.wav"
  segs+=("$seg_dir/seg-$i.wav")
  # The trailing newline matters: without it `read` discards the final segment,
  # silently dropping the last sentence of every pair.
done < <(printf '%s\n' "$TEXT" | sed 's/||/\n/g')

if [[ ${#segs[@]} -eq 0 ]]; then
  echo "error: no non-empty segments in text" >&2
  exit 1
fi

python3 - "$OUT" "${segs[@]}" <<'PY'
import sys, wave

out, parts = sys.argv[1], sys.argv[2:]
frames, params = [], None
for p in parts:
    with wave.open(p, "rb") as w:
        if params is None:
            params = w.getparams()
        elif (w.getnchannels(), w.getsampwidth(), w.getframerate()) != (
            params.nchannels, params.sampwidth, params.framerate):
            raise SystemExit(f"segment {p} has a different WAV format; refusing to concatenate")
        frames.append(w.readframes(w.getnframes()))

with wave.open(out, "wb") as w:
    w.setnchannels(params.nchannels)
    w.setsampwidth(params.sampwidth)
    w.setframerate(params.framerate)
    w.writeframes(b"".join(frames))
PY

cp "$seg_dir/seg-1.meta.json" "${OUT%.wav}.meta.json"
