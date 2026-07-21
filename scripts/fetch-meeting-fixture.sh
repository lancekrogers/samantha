#!/usr/bin/env bash
# Download a short multi-speaker meeting clip for diarization integration tests.
#
# Source: Product Marketing Meeting (weekly) 2021-06-28
#   https://www.youtube.com/watch?v=lBVtvOpU80Q
#
# Requires: yt-dlp, ffmpeg
set -euo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
OUT_DIR="${ROOT}/tests/fixtures/meetings"
STEM="product-marketing-meeting-90s"
OUT_WAV="${OUT_DIR}/${STEM}.wav"
URL="${MEETING_FIXTURE_URL:-https://www.youtube.com/watch?v=lBVtvOpU80Q}"
SECTION="${MEETING_FIXTURE_SECTION:-*0:00-1:30}"

mkdir -p "$OUT_DIR"

if [[ -f "$OUT_WAV" && "${FORCE_FETCH:-}" != "1" ]]; then
	echo "fixture already present: $OUT_WAV"
	ls -lh "$OUT_WAV"
	exit 0
fi

command -v yt-dlp >/dev/null || { echo "yt-dlp required" >&2; exit 1; }
command -v ffmpeg >/dev/null || { echo "ffmpeg required" >&2; exit 1; }

tmp="$(mktemp -d "${TMPDIR:-/tmp}/samantha-meeting-fixture.XXXXXX")"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT

echo "Fetching ${URL} section ${SECTION} → 16 kHz mono WAV…"
yt-dlp -f 'bestaudio/best' \
	--download-sections "$SECTION" \
	-x --audio-format wav \
	--postprocessor-args "ffmpeg:-ac 1 -ar 16000" \
	-o "${tmp}/${STEM}.%(ext)s" \
	"$URL"

# yt-dlp may leave a .wav with a slightly different name after postprocess.
found="$(find "$tmp" -type f \( -name '*.wav' -o -name '*.wave' \) | head -1)"
if [[ -z "$found" ]]; then
	echo "download produced no wav in $tmp:" >&2
	ls -la "$tmp" >&2
	exit 1
fi

# Normalize again in case the section extract kept a non-16k rate.
ffmpeg -y -loglevel error -i "$found" -ac 1 -ar 16000 "$OUT_WAV"
ls -lh "$OUT_WAV"
file "$OUT_WAV"
echo "Wrote $OUT_WAV"
echo "Source: $URL"
echo "Note: fixture is gitignored — re-run this script on each machine."
