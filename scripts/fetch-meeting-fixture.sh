#!/usr/bin/env bash
# Download a short multi-speaker meeting clip for diarization integration tests.
#
# Source: Product Marketing Meeting (weekly) 2021-06-28
#   https://www.youtube.com/watch?v=lBVtvOpU80Q
#
# Only the first ~90 seconds are downloaded (not the full ~43 min video).
# The WAV is stored in a *shared user cache* so every git worktree reuses it:
#
#   ${SAMANTHA_FIXTURE_CACHE:-${XDG_CACHE_HOME:-$HOME/.cache}/samantha/fixtures/meetings}
#
# Requires: yt-dlp, ffmpeg
set -euo pipefail

STEM="product-marketing-meeting-90s"
URL="${MEETING_FIXTURE_URL:-https://www.youtube.com/watch?v=lBVtvOpU80Q}"
# First 90s only — full video is ~43 minutes and unnecessary for the suite.
SECTION="${MEETING_FIXTURE_SECTION:-*0:00-1:30}"

if [[ -n "${SAMANTHA_FIXTURE_CACHE:-}" ]]; then
	CACHE_DIR="${SAMANTHA_FIXTURE_CACHE}"
elif [[ -n "${XDG_CACHE_HOME:-}" ]]; then
	CACHE_DIR="${XDG_CACHE_HOME}/samantha/fixtures/meetings"
else
	CACHE_DIR="${HOME}/.cache/samantha/fixtures/meetings"
fi

OUT_WAV="${CACHE_DIR}/${STEM}.wav"
mkdir -p "$CACHE_DIR"

if [[ -f "$OUT_WAV" && "${FORCE_FETCH:-}" != "1" ]]; then
	# Reuse shared cache across worktrees / test runs.
	sz="$(wc -c <"$OUT_WAV" | tr -d ' ')"
	if [[ "$sz" -gt 100000 ]]; then
		echo "fixture already cached (shared): $OUT_WAV"
		ls -lh "$OUT_WAV"
		echo "hint: FORCE_FETCH=1 just fetch-meeting-fixture  # re-download"
		exit 0
	fi
	echo "cached file looks truncated (${sz} bytes) — re-fetching…"
fi

command -v yt-dlp >/dev/null || { echo "yt-dlp required" >&2; exit 1; }
command -v ffmpeg >/dev/null || { echo "ffmpeg required" >&2; exit 1; }

tmp="$(mktemp -d "${TMPDIR:-/tmp}/samantha-meeting-fixture.XXXXXX")"
cleanup() { rm -rf "$tmp"; }
trap cleanup EXIT

echo "Fetching ${URL} section ${SECTION} → 16 kHz mono WAV (shared cache)…"
echo "cache: $OUT_WAV"
yt-dlp -f 'bestaudio/best' \
	--download-sections "$SECTION" \
	-x --audio-format wav \
	--postprocessor-args "ffmpeg:-ac 1 -ar 16000" \
	-o "${tmp}/${STEM}.%(ext)s" \
	"$URL"

found="$(find "$tmp" -type f \( -name '*.wav' -o -name '*.wave' \) | head -1)"
if [[ -z "$found" ]]; then
	echo "download produced no wav in $tmp:" >&2
	ls -la "$tmp" >&2
	exit 1
fi

# Atomic write into the shared cache.
tmp_out="${OUT_WAV}.tmp.$$"
ffmpeg -y -loglevel error -i "$found" -ac 1 -ar 16000 "$tmp_out"
mv -f "$tmp_out" "$OUT_WAV"
ls -lh "$OUT_WAV"
file "$OUT_WAV"
echo "Wrote shared fixture: $OUT_WAV"
echo "Source: $URL (section ${SECTION} only — not the full video)"
echo "All worktrees reuse this path; set SAMANTHA_FIXTURE_CACHE to override."
