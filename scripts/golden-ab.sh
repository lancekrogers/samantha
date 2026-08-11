#!/usr/bin/env bash
# Generate a golden-tts A/B pair for a naturalness change, per festival D006.
#
# "Before" is the text the OLD code sent to TTS; "after" is what the NEW code
# sends. Both are synthesized through the same Kokoro pack so the only variable
# is the text transform under test.
#
# Usage:
#   scripts/golden-ab.sh <item-id> <before-text> <after-text> [claim]
#
# Writes docs/audio/golden/<item-id>/{before.wav,after.wav,provenance.json}.
#
# GOLDEN_BEFORE_REF=<git-ref> synthesizes the before side from a temporary
# worktree at that ref instead of the working tree. Set it whenever the change
# under test lives *inside* the synthesiser rather than in the text handed to it
# — otherwise the before side gets the fix too and both files come out
# byte-identical, which looks like a passing A/B and proves nothing.
#
# This script measures duration and loudness; it cannot judge how something
# sounds. provenance.json therefore carries a `listen_check` block that stays
# "pending" until a human plays the pair and records the verdict.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$REPO_ROOT"

if [[ $# -lt 3 ]]; then
  echo "usage: $0 <item-id> <before-text> <after-text> [claim]" >&2
  exit 2
fi

ITEM_ID="$1"
BEFORE_TEXT="$2"
AFTER_TEXT="$3"
CLAIM="${4:-}"

VOICE="${GOLDEN_VOICE:-af_heart}"
SPEED="${GOLDEN_SPEED:-0.95}"
OUT_DIR="docs/audio/golden/${ITEM_ID}"

# Identical text is only meaningless when both sides run the same code. With
# GOLDEN_BEFORE_REF the code differs, so holding the text fixed is the whole
# point: it isolates the synthesiser change.
if [[ "$BEFORE_TEXT" == "$AFTER_TEXT" && -z "${GOLDEN_BEFORE_REF:-}" ]]; then
  echo "error: before and after text are identical and both sides run the same code —" >&2
  echo "       this pair would prove nothing. Vary the text, or set GOLDEN_BEFORE_REF." >&2
  exit 1
fi

mkdir -p "$OUT_DIR"

SYNTH="$REPO_ROOT/scripts/golden-synth.sh"

synth() {
  GOLDEN_VOICE="$VOICE" GOLDEN_SPEED="$SPEED" bash "$SYNTH" "$1" "$2"
}

# Synthesize from a checkout of an older ref, so the before side is produced by
# code that predates the change under test. The synth script is taken from the
# working tree but RUN inside the worktree, so it is the Go code that changes,
# not the harness.
# Cleanup runs on EXIT, not on a function's RETURN: `set -e` aborting the shell
# from inside a function skips a RETURN trap entirely, and `go run` failing (bad
# voice, missing Kokoro pack, cold model cache) is the expected failure mode here.
# A leaked worktree stays registered in .git/worktrees until someone prunes it.
TMP_PARENT=""
WORKTREE=""
cleanup() {
  [[ -n "$WORKTREE" ]] && git worktree remove --force "$WORKTREE" >/dev/null 2>&1 || true
  [[ -n "$TMP_PARENT" ]] && rm -rf "$TMP_PARENT"
  return 0
}
trap cleanup EXIT

synth_at_ref() {
  local ref="$1" text="$2" out="$3"
  TMP_PARENT="$(mktemp -d)"
  WORKTREE="$TMP_PARENT/before-tree"
  # stderr is NOT suppressed: this is the command most likely to fail (bad ref,
  # or a worktree leaked by an earlier run), and swallowing it leaves the user
  # with a silent non-zero exit.
  git worktree add --detach "$WORKTREE" "$ref" >/dev/null
  (
    cd "$WORKTREE" && GOLDEN_VOICE="$VOICE" GOLDEN_SPEED="$SPEED" \
      bash "$SYNTH" "$text" "$REPO_ROOT/$out"
  )
}

BEFORE_REF="${GOLDEN_BEFORE_REF:-}"
BEFORE_SHA="working-tree"
if [[ -n "$BEFORE_REF" ]]; then
  # Resolve to a SHA at capture time. Recording the literal ref would be worse
  # than useless: "HEAD" re-resolves to the post-change commit once this branch
  # lands, so the provenance would actively misdescribe the before side.
  BEFORE_SHA="$(git rev-parse "$BEFORE_REF")"
  echo "==> synthesizing ${ITEM_ID} before (from ${BEFORE_REF} = ${BEFORE_SHA:0:12})"
  synth_at_ref "$BEFORE_REF" "$BEFORE_TEXT" "$OUT_DIR/before.wav"
else
  echo "==> synthesizing ${ITEM_ID} before"
  synth "$BEFORE_TEXT" "$OUT_DIR/before.wav"
fi
echo "==> synthesizing ${ITEM_ID} after"
synth "$AFTER_TEXT" "$OUT_DIR/after.wav"

# Leave no directory that looks like evidence but is not: on any refusal below,
# clear the half-written pair rather than leaving orphan WAVs with no provenance.
discard_pair() {
  rm -f "$OUT_DIR"/before.wav "$OUT_DIR"/after.wav "$OUT_DIR"/*.meta.json
}

# A pair whose two sides are byte-identical is not evidence. This is the failure
# mode GOLDEN_BEFORE_REF exists to prevent, so refuse to write provenance for it.
if cmp -s "$OUT_DIR/before.wav" "$OUT_DIR/after.wav"; then
  echo "error: before.wav and after.wav are byte-identical." >&2
  echo "       The change under test did not affect the audio. If the transform" >&2
  echo "       runs inside the synthesiser, re-run with GOLDEN_BEFORE_REF=<pre-change-ref>." >&2
  discard_pair
  exit 1
fi

# The header claims both sides ran through the same Kokoro pack. Under
# GOLDEN_BEFORE_REF the before side is a different checkout, which can pin a
# different pack — so check the claim instead of asserting it in prose.
BEFORE_PACK="$(jq -r '.kokoro_pack // "unknown"' "$OUT_DIR/before.meta.json")"
AFTER_PACK="$(jq -r '.kokoro_pack // "unknown"' "$OUT_DIR/after.meta.json")"
BEFORE_MODEL_SHA="$(jq -r '.model_sha256 // ""' "$OUT_DIR/before.meta.json")"
AFTER_MODEL_SHA="$(jq -r '.model_sha256 // ""' "$OUT_DIR/after.meta.json")"
if [[ "$BEFORE_PACK" != "$AFTER_PACK" || "$BEFORE_MODEL_SHA" != "$AFTER_MODEL_SHA" ]]; then
  echo "error: the two sides used different Kokoro weights, so the pair does not" >&2
  echo "       isolate the change under test:" >&2
  echo "         before: pack=$BEFORE_PACK sha=${BEFORE_MODEL_SHA:0:12}" >&2
  echo "         after:  pack=$AFTER_PACK sha=${AFTER_MODEL_SHA:0:12}" >&2
  discard_pair
  exit 1
fi

# Objective measurements. These do not establish that the change sounds better;
# they establish that it changed the audio at all, which is the precondition for
# a listen check being worth a human's time.
measure() {
  python3 - "$1" <<'PY'
import json, math, struct, sys, wave

with wave.open(sys.argv[1], "rb") as w:
    frames = w.readframes(w.getnframes())
    rate, width, n = w.getframerate(), w.getsampwidth(), w.getnframes()

if width != 2:
    raise SystemExit(f"expected 16-bit PCM, got {width*8}-bit")

samples = struct.unpack("<%dh" % (len(frames) // 2), frames)
scale = 32768.0
peak = max((abs(s) for s in samples), default=0) / scale
rms = math.sqrt(sum((s / scale) ** 2 for s in samples) / len(samples)) if samples else 0.0

print(json.dumps({
    "duration_s": round(n / rate, 3),
    "sample_rate": rate,
    "num_samples": n,
    "peak": round(peak, 4),
    "rms": round(rms, 4),
    "rms_dbfs": round(20 * math.log10(rms), 2) if rms > 0 else None,
}))
PY
}

BEFORE_M="$(measure "$OUT_DIR/before.wav")"
AFTER_M="$(measure "$OUT_DIR/after.wav")"

COMMIT="$(git rev-parse HEAD)"
DIRTY="$(git status --porcelain --untracked-files=no | head -c1)"
[[ -n "$DIRTY" ]] && COMMIT="${COMMIT}-dirty"

jq -n \
  --arg item "$ITEM_ID" \
  --arg claim "$CLAIM" \
  --arg before_text "$BEFORE_TEXT" \
  --arg after_text "$AFTER_TEXT" \
  --arg voice "$VOICE" \
  --arg speed "$SPEED" \
  --arg commit "$COMMIT" \
  --arg pack "$BEFORE_PACK" \
  --arg model_sha "$BEFORE_MODEL_SHA" \
  --arg before_ref "$BEFORE_SHA" \
  --arg machine "$(uname -m)" \
  --arg os "$(uname -sr)" \
  --argjson before_m "$BEFORE_M" \
  --argjson after_m "$AFTER_M" \
  '{
    item: $item,
    claim: $claim,
    before: {text: $before_text, audio: "before.wav", measured: $before_m},
    after:  {text: $after_text,  audio: "after.wav",  measured: $after_m},
    tts: {provider: "kokoro", voice: $voice, speed: ($speed|tonumber), pack: $pack, model_sha256: $model_sha},
    provenance: {samantha_commit: $commit, before_synthesized_at: $before_ref, machine: $machine, os: $os},
    listen_check: {
      status: "pending",
      instructions: "Play before.wav then after.wav. Record verdict and who listened.",
      verdict: null,
      listener: null
    }
  }' > "$OUT_DIR/provenance.json"

rm -f "$OUT_DIR"/*.meta.json

echo
echo "wrote $OUT_DIR/{before.wav,after.wav,provenance.json}"
jq -r '"  before: \(.before.measured.duration_s)s  rms \(.before.measured.rms_dbfs) dBFS\n  after:  \(.after.measured.duration_s)s  rms \(.after.measured.rms_dbfs) dBFS"' "$OUT_DIR/provenance.json"
echo
echo "listen check is PENDING — play both files and record the verdict in provenance.json"
