#!/usr/bin/env bash
# Record utterances into testdata/corpus/ and append them to manifest.json.
#
# The corpus feeds two things: the committed latency baseline, and the
# batch-vs-streaming STT accuracy comparison. Both read the same manifest, so it
# is recorded once.
#
# Usage:
#   scripts/record-corpus.sh              # interactive loop
#   scripts/record-corpus.sh --devices    # list audio input devices and exit
#   AUDIO_DEVICE=:2 scripts/record-corpus.sh
#
# Requires: ffmpeg, jq (ffmpeg is already required by scripts/fetch-meeting-fixture.sh)
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CORPUS_DIR="${REPO_ROOT}/testdata/corpus"
MANIFEST="${CORPUS_DIR}/manifest.json"
SAMPLE_RATE=16000

for tool in ffmpeg jq; do
	command -v "$tool" >/dev/null 2>&1 || { echo "error: $tool is required but not installed" >&2; exit 1; }
done

list_devices() {
	echo "Audio input devices (use the [N] index as AUDIO_DEVICE=:N):"
	echo ""
	# avfoundation prints its device list to stderr and exits non-zero by design.
	ffmpeg -hide_banner -f avfoundation -list_devices true -i "" 2>&1 |
		sed -n '/AVFoundation audio devices/,$p' | sed 's/^/  /'
}

if [[ "${1:-}" == "--devices" ]]; then
	list_devices
	exit 0
fi

DEVICE="${AUDIO_DEVICE:-:0}"

if [[ ! -f "$MANIFEST" ]]; then
	echo "error: $MANIFEST not found" >&2
	exit 1
fi

cat <<'INTRO'
────────────────────────────────────────────────────────────────────────
 Corpus recorder
────────────────────────────────────────────────────────────────────────
 One sample per round: pick a category, type what you are going to say,
 then record it. Say it exactly as typed - the text becomes the expected
 transcript that accuracy is scored against.

 Aim for roughly 30 samples with all three categories represented.

 The thoughtful_pause samples matter most. They need a real mid-sentence
 pause - the kind where you stop to think for a beat and then carry on.
 Those are the samples that reveal whether a shorter silence window cuts
 people off. A corpus of only crisp commands will make any reduction look
 safe when it is not.
────────────────────────────────────────────────────────────────────────
INTRO
echo ""
echo "Input device: ${DEVICE}  (override with AUDIO_DEVICE, list with --devices)"
echo ""

next_index() {
	local cat="$1" n
	n=$(jq --arg c "$cat" '[.samples[] | select(.category == $c)] | length' "$MANIFEST")
	printf "%02d" "$((n + 1))"
}

append_sample() {
	local path="$1" expect="$2" cat="$3" notes="$4" tmp
	tmp="$(mktemp)"
	jq --arg p "$path" --arg e "$expect" --arg c "$cat" --arg n "$notes" \
		'.samples += [{path: $p, expect: $e, category: $c, notes: $n}]' \
		"$MANIFEST" >"$tmp"
	mv "$tmp" "$MANIFEST"
}

while true; do
	echo "Category:"
	echo "  1) short_command     crisp, quickly ended"
	echo "  2) thoughtful_pause  contains a real mid-sentence pause"
	echo "  3) noisy_room        realistic background noise"
	read -r -p "  choose [1-3, or q to finish]: " choice
	case "$choice" in
		1) CATEGORY="short_command" ;;
		2) CATEGORY="thoughtful_pause" ;;
		3) CATEGORY="noisy_room" ;;
		q|Q) break ;;
		*) echo "  (pick 1, 2, 3 or q)"; echo ""; continue ;;
	esac

	read -r -p "What will you say? " EXPECT
	if [[ -z "${EXPECT// }" ]]; then
		echo "  (empty - skipping)"; echo ""; continue
	fi
	read -r -p "Notes (optional, e.g. 'paused after \"because\"'): " NOTES

	IDX="$(next_index "$CATEGORY")"
	NAME="${CATEGORY}-${IDX}.wav"
	OUT="${CORPUS_DIR}/${NAME}"

	echo ""
	echo "  Recording -> ${NAME}"
	echo "  Press q (then Enter) to stop."
	echo ""
	# -y overwrite, mono, 16 kHz, signed 16-bit PCM: the format the STT path expects.
	ffmpeg -hide_banner -loglevel error -f avfoundation -i "$DEVICE" \
		-ac 1 -ar "$SAMPLE_RATE" -c:a pcm_s16le -y "$OUT" || true

	if [[ ! -s "$OUT" ]]; then
		echo "  ✗ nothing recorded (check AUDIO_DEVICE and microphone permission) - not added"
		echo ""
		continue
	fi

	DURATION="$(ffprobe -v error -show_entries format=duration -of csv=p=0 "$OUT" 2>/dev/null || echo "?")"
	append_sample "$NAME" "$EXPECT" "$CATEGORY" "$NOTES"
	echo "  ✓ saved ${NAME} (${DURATION}s) and added to manifest.json"
	echo ""
done

echo ""
echo "Corpus summary:"
jq -r '.samples | group_by(.category)[] | "  \(.[0].category): \(length)"' "$MANIFEST"
TOTAL=$(jq '.samples | length' "$MANIFEST")
echo "  total: ${TOTAL}"
echo ""
echo "Validate with: just bench corpus-check"
