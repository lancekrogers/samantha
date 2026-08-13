#!/usr/bin/env bash
# Run the latency benchmark, wrap it with provenance, and diff against the
# committed baseline.
#
# Benchmark numbers are hardware-bound: the same code on a different Mac
# produces different figures. Every artifact therefore records the machine, the
# OS and the samantha commit, and comparisons are only meaningful between runs
# whose provenance matches.
#
# Usage:
#   scripts/bench.sh run                 # measure, print the envelope, write nothing
#   scripts/bench.sh baseline            # measure and (re)write docs/benchmarks/baseline.json
#   scripts/bench.sh diff                # measure and compare against the baseline
#   scripts/bench.sh save L1             # measure and write docs/benchmarks/L1-<date>.json
#
# Requires: jq, a built ./bin/samantha (just build), and a reachable brain provider.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
BENCH_DIR="${REPO_ROOT}/docs/benchmarks"
BASELINE="${BENCH_DIR}/baseline.json"
MANIFEST="${REPO_ROOT}/testdata/corpus/manifest.json"
# Synthetic fixtures: TTS-generated speech used when no human corpus is
# recorded. Deterministic and reproducible from their text, which makes them a
# stable regression instrument; they are NOT a substitute for recorded speech
# when judging endpointing, since they contain no natural mid-sentence pauses.
SYNTH_DIR="${SAMANTHA_BENCH_FIXTURES:-${XDG_CACHE_HOME:-$HOME/.cache}/festival-voice/fixtures/bench}"
SAMANTHA="${SAMANTHA_BIN:-${REPO_ROOT}/bin/samantha}"

# Regression tolerance and the absolute target from the latency spec.
REGRESSION_PCT="${BENCH_REGRESSION_PCT:-10}"
# Iteration 1 is discarded as warmup: ollama model load and kokoro first-synth
# dominate it, and a baseline that bakes in cold start cannot detect regressions.
ITERATIONS="${BENCH_ITERATIONS:-3}"
TARGET_PLAYBACK_START_MS="${BENCH_TARGET_PLAYBACK_START_MS:-1200}"

command -v jq >/dev/null 2>&1 || { echo "error: jq is required" >&2; exit 1; }
[[ -x "$SAMANTHA" ]] || { echo "error: $SAMANTHA not found or not executable (run: just build)" >&2; exit 1; }

mkdir -p "$BENCH_DIR"

# Read one config value. `samantha config <key>` prints "  key = value" with
# ANSI styling; strip both.
cfg_get() {
	"$SAMANTHA" config "$1" 2>/dev/null |
		sed 's/\x1b\[[0-9;]*m//g' |
		awk -F' = ' 'NR==1 {gsub(/^[ \t]+/, "", $2); print $2}'
}

provenance() {
	# Model and persona belong here as much as the machine does: the same code on
	# the same Mac produces entirely different numbers under a 135M model than a
	# 26B one, so a baseline without them cannot be compared against.
	jq -n \
		--arg machine "$(sysctl -n hw.model 2>/dev/null || echo unknown)" \
		--arg cpu "$(sysctl -n machdep.cpu.brand_string 2>/dev/null || echo unknown)" \
		--arg os "$(sw_vers -productName 2>/dev/null || uname -s) $(sw_vers -productVersion 2>/dev/null || uname -r)" \
		--arg commit "$(git -C "$REPO_ROOT" rev-parse --short HEAD 2>/dev/null || echo unknown)" \
		--arg dirty "$(git -C "$REPO_ROOT" diff --quiet 2>/dev/null && git -C "$REPO_ROOT" diff --cached --quiet 2>/dev/null && echo clean || echo dirty)" \
		--arg persona "$(cfg_get active_persona)" \
		--arg provider "${BRAIN_PROVIDER:-$(cfg_get brain_provider)}" \
		--arg model "${OLLAMA_MODEL:-$(cfg_get ollama_model)}" \
		--arg tts "$(cfg_get tts_provider)" \
		--arg fixtures "${FIXTURE_KIND:-unknown}" \
		--arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
		'{machine: $machine, cpu: $cpu, os: $os, samantha_commit: $commit, worktree: $dirty,
		  persona: $persona, brain_provider: $provider, brain_model: $model, tts_provider: $tts,
		  fixtures: $fixtures,
		  recorded_at: $at}'
}

# A comparison across different hardware, model or fixture kind is meaningless.
# Both sides are read from their envelopes: FIXTURE_KIND is set inside measure(),
# which runs in a command substitution, so it cannot be read from a variable here.
warn_if_incomparable() {
	local base="$1" cur="$2" field was now
	for field in machine brain_model fixtures; do
		was="$(jq -r --arg f "$field" '.provenance[$f] // "unknown"' "$base")"
		now="$(jq -r --arg f "$field" '.provenance[$f] // "unknown"' "$cur")"
		if [[ "$was" != "$now" ]]; then
			echo ""
			echo "  ⚠ ${field} mismatch: baseline recorded with '${was}', running with '${now}'"
			echo "    these numbers are not comparable; re-baseline to use the diff"
		fi
	done
}

# B2 guard: a run in which turns failed must never read as a clean pass.
require_clean_run() {
	local file="$1" n
	n="$(jq '[.results[] | select((.errors // []) | length > 0)] | length' "$file")"
	if [[ "$n" -gt 0 ]]; then
		echo "" >&2
		echo "  ✗ ${n} turn(s) reported errors — the measurement is not trustworthy:" >&2
		jq -r '.results[] | select((.errors // []) | length > 0)
			| "      \(.fixture | split("/") | last): \(.errors[0].message)"' "$file" >&2 | sort -u
		return 1
	fi
}

# B2 guard: comparing across different fixture sets silently changes the
# population. A fixture that errors out drops from group_by and can make the
# median improve.
require_same_fixtures() {
	local base="$1" cur="$2" a b
	a="$(jq -r '[.results[] | .fixture | split("/") | last] | unique | join(",")' "$base")"
	b="$(jq -r '[.results[] | .fixture | split("/") | last] | unique | join(",")' "$cur")"
	if [[ "$a" != "$b" ]]; then
		echo "" >&2
		echo "  ✗ fixture set differs from the baseline — medians are not comparable" >&2
		echo "      baseline: ${a}" >&2
		echo "      current:  ${b}" >&2
		return 1
	fi
}

# The fixed sentences behind the synthetic fixtures. Changing this list changes
# the instrument, so treat it as you would a committed baseline.
# Deliberately elicit SHORT replies. The gated metric is the stt stage, so reply
# length contributes nothing but noise — and a long first sentence synthesizes
# at roughly 1x realtime, which can trip the pipeline's 8s playback-stall
# watchdog and abort the turn.
# Trailing silence appended to every synthetic fixture, in seconds. Must exceed
# the largest vad_silence_duration being compared, or endpointing never fires
# from silence and the measurement is blind to it.
SYNTH_TRAILING_SILENCE="${SYNTH_TRAILING_SILENCE:-1.5}"

SYNTH_TEXTS=(
	"What is the capital of France? Answer in one word."
	"What is two plus two? Answer with just the number."
	"Say hello in exactly three words."
)

# Generate the synthetic fixture set if missing. Uses the project's own TTS, so
# it needs no external tooling and no recording session.
ensure_synth_fixtures() {
	mkdir -p "$SYNTH_DIR"
	local i=0 missing=0
	for _ in "${SYNTH_TEXTS[@]}"; do
		i=$((i + 1))
		[[ -s "${SYNTH_DIR}/synth-$(printf '%02d' "$i").wav" ]] || missing=1
	done
	[[ "$missing" -eq 1 ]] || return 0

	echo "  generating synthetic fixtures in ${SYNTH_DIR} ..." >&2
	local tmpdir gtts out
	tmpdir="$(mktemp -d)"
	gtts="${tmpdir}/golden-tts"
	(cd "$REPO_ROOT" && go build -o "$gtts" ./cmd/golden-tts) || {
		rm -rf "$tmpdir"; echo "error: could not build cmd/golden-tts" >&2; return 1; }
	i=0
	for text in "${SYNTH_TEXTS[@]}"; do
		i=$((i + 1))
		out="${SYNTH_DIR}/synth-$(printf '%02d' "$i").wav"
		if ! "$gtts" -text "$text" -out "${tmpdir}/raw.wav" >/dev/null 2>&1 || [[ ! -s "${tmpdir}/raw.wav" ]]; then
			rm -rf "$tmpdir"
			echo "error: failed to synthesize fixture ${i} (is the TTS model installed?)" >&2
			return 1
		fi
		# Append trailing silence. Without it the WAV ends the instant speech
		# does, so the source hits EOF and emits Final before the VAD silence
		# window can elapse — which makes any endpointing change invisible.
		# The pad must exceed the largest silence window under comparison.
		if ! ffmpeg -hide_banner -loglevel error -i "${tmpdir}/raw.wav" \
			-af "apad=pad_dur=${SYNTH_TRAILING_SILENCE}" -c:a pcm_s16le -y "$out" >/dev/null 2>&1; then
			rm -rf "$tmpdir"
			echo "error: could not pad fixture ${i} with trailing silence (ffmpeg required)" >&2
			return 1
		fi
	done
	rm -rf "$tmpdir"
}

# Expand the synthetic set into full-turn benchmark arguments.
synth_args() {
	ensure_synth_fixtures || return 1
	local i=0
	for text in "${SYNTH_TEXTS[@]}"; do
		i=$((i + 1))
		printf '%s\n' "--full-turn-fixture=${SYNTH_DIR}/synth-$(printf '%02d' "$i").wav" "--expect-text=${text}"
	done
}

# Recorded corpus wins when present: it is real speech with real pauses.
corpus_args() {
	[[ -f "$MANIFEST" ]] || return 0
	local n
	n=$(jq '.samples | length' "$MANIFEST")
	[[ "$n" -gt 0 ]] || return 0
	# --full-turn-fixture, not --audio-fixture: the latter stops at the final
	# transcript and never reaches brain or TTS, so it cannot measure a turn.
	jq -r --arg root "$REPO_ROOT" \
		'.samples[] | "--full-turn-fixture=\($root)/testdata/corpus/\(.path)", "--expect-text=\(.expect)"' "$MANIFEST"
}

# M5 guard: a run whose model or machine is unknown cannot be compared against
# anything, which is the whole point of recording provenance.
require_comparable_provenance() {
	local file="$1" v
	for field in machine brain_model; do
		v="$(jq -r --arg f "$field" '.provenance[$f] // ""' "$file")"
		if [[ -z "$v" || "$v" == "unknown" ]]; then
			echo "error: provenance.${field} is empty - set it (e.g. OLLAMA_MODEL=...) before recording an artifact" >&2
			return 1
		fi
	done
}

measure() {
	local raw envelope
	raw="$(mktemp)"
	envelope="$(mktemp)"
	trap 'rm -f "$raw"' RETURN
	local args=() kind="recorded corpus"
	while IFS= read -r a; do [[ -n "$a" ]] && args+=("$a"); done < <(corpus_args)
	if [[ ${#args[@]} -eq 0 ]]; then
		kind="synthetic fixtures"
		while IFS= read -r a; do [[ -n "$a" ]] && args+=("$a"); done < <(synth_args)
	fi
	[[ ${#args[@]} -gt 0 ]] || { echo "error: no fixtures available" >&2; exit 1; }

	FIXTURE_KIND="$kind"
	echo "  running full-turn benchmark (${kind}, $(( ${#args[@]} / 2 )) utterances)..." >&2
	# A threshold violation is reported in the JSON and via exit status; capture
	# the results either way and let the diff decide.
	"$SAMANTHA" benchmark --iterations "$ITERATIONS" --json "$raw" "${args[@]+"${args[@]}"}" >/dev/null 2>&1 || true

	[[ -s "$raw" ]] || { echo "error: benchmark produced no JSON (is a brain provider reachable?)" >&2; exit 1; }

	jq -n \
		--argjson provenance "$(provenance)" \
		--argjson results "$(cat "$raw")" \
		--argjson target "$TARGET_PLAYBACK_START_MS" \
		--argjson tolerance "$REGRESSION_PCT" \
		'{schema: "samantha.benchmark.v1",
		  provenance: $provenance,
		  thresholds: {playback_start_p50_ms: $target, regression_tolerance_pct: $tolerance},
		  results: $results}' >"$envelope"
	echo "$envelope"
}

# Per-fixture stage medians over warm samples (iteration 1 is warmup).
#
# Pooling different utterances into one median mixes populations: a 1.5s clip
# and a 4s clip have genuinely different stage timings, so a pooled p50 moves
# when the fixture mix changes rather than when performance does. Everything
# below is therefore keyed by fixture.
#
# Stages, and why they are separated:
#   stt     STTFinalElapsed — endpointing + recognition. Measured at +/-0.5%
#           within a fixture, and it is what the VAD-window and STT-provider
#           changes actually move. This is the gated metric.
#   model   first model chunk after the transcript. Swings 1.5s-24s run to run
#           on a large local model; nothing in this track changes it, so gating
#           on it would only produce false failures. Reported, never gated.
#   synth   first segment -> first audio. First-sentence synthesis, which the
#           chunking and first-sentence-length changes move. Reported.
stage_table() {
	jq -r '
		# True median: with the default 3 iterations each fixture has 2 warm
		# samples, and taking the lower element would silently report the
		# faster of two runs as if it were typical.
		def med: sort | if length == 0 then null
			elif length % 2 == 1 then .[(length - 1) / 2]
			else (.[length / 2 - 1] + .[length / 2]) / 2 end;
		[.results[] | select(.iteration > 1) | select(.metrics.STTFinalElapsed > 0)]
		| group_by(.fixture)
		| map({
			fixture: (.[0].fixture | split("/") | last),
			n: length,
			stt:   ([.[] | .metrics.STTFinalElapsed / 1000000] | med),
			model: ([.[] | (.metrics.FirstModelChunkElapsed - .metrics.STTFinalElapsed) / 1000000] | med),
			synth: ([.[] | (.metrics.FirstAudioReadyElapsed - .metrics.FirstSegmentElapsed) / 1000000] | med)
		})
		| .[] | "\(.fixture)\t\(.n)\t\(.stt)\t\(.model)\t\(.synth)"' "$1"
}

print_stages() {
	printf "\n  %-28s %3s %10s %10s %10s\n" "fixture" "n" "stt(ms)" "model(ms)" "synth(ms)"
	stage_table "$1" | while IFS=$'\t' read -r f n stt model synth; do
		printf "  %-28s %3s %10.0f %10.0f %10.0f\n" "$f" "$n" "$stt" "$model" "$synth"
	done
}

# Gated metric: median stt stage across fixtures. Reported for a quick headline.
stt_median() {
	stage_table "$1" | awk -F'\t' '{print $3}' | sort -n |
		awk '{a[NR]=$1} END {
			if (NR == 0) { print "null" }
			else if (NR % 2) { print a[(NR + 1) / 2] }
			else { print (a[NR / 2] + a[NR / 2 + 1]) / 2 }
		}'
}

case "${1:-diff}" in
run)
	env_file="$(measure)"
	jq '.' "$env_file"
	print_stages "$env_file" >&2
	rm -f "$env_file"
	;;

baseline)
	# M6: the reference numbers must be attributable to committed code.
	if [[ "$(git -C "$REPO_ROOT" status --porcelain 2>/dev/null | grep -vc '^??')" != "0" && -z "${BENCH_ALLOW_DIRTY:-}" ]]; then
		echo "error: worktree is dirty — a baseline recorded from uncommitted code cannot be reproduced" >&2
		echo "       commit first, or set BENCH_ALLOW_DIRTY=1 to override deliberately" >&2
		exit 1
	fi
	env_file="$(measure)"
	trap 'rm -f "$env_file"' EXIT
	require_clean_run "$env_file" || exit 1
	require_comparable_provenance "$env_file" || exit 1
	med="$(stt_median "$env_file")"
	[[ "$med" != "null" ]] || { echo "error: no completed turns - refusing to write a baseline with no data" >&2; exit 1; }
	trap - EXIT
	mv "$env_file" "$BASELINE"
	print_stages "$BASELINE"
	echo ""
	echo "  ✓ wrote docs/benchmarks/baseline.json"
	echo "    gated metric: median stt stage ${med} ms"
	echo "    model / machine: $(jq -r .provenance.brain_model "$BASELINE") / $(jq -r .provenance.machine "$BASELINE")"
	echo "    fixtures: $(jq -r .provenance.fixtures "$BASELINE")"
	echo "    this run defines the reference for every later comparison"
	;;

save)
	label="${2:?usage: bench.sh save <label>   e.g. bench.sh save L1}"
	env_file="$(measure)"
	require_clean_run "$env_file" || { rm -f "$env_file"; exit 1; }
	require_comparable_provenance "$env_file" || { rm -f "$env_file"; exit 1; }
	out="${BENCH_DIR}/${label}-$(date -u +%Y-%m-%d).json"
	mv "$env_file" "$out"
	print_stages "$out"
	echo ""
	echo "  ✓ wrote ${out}"
	echo "    gated metric: median stt stage $(stt_median "$out") ms"
	;;

diff)
	[[ -f "$BASELINE" ]] || { echo "error: no baseline at $BASELINE (run: just bench baseline)" >&2; exit 1; }
	env_file="$(measure)"
	trap 'rm -f "$env_file"' EXIT
	require_clean_run "$env_file" || exit 1
	require_same_fixtures "$BASELINE" "$env_file" || exit 1

	now="$(stt_median "$env_file")"
	base="$(stt_median "$BASELINE")"
	[[ "$now" != "null" && "$base" != "null" ]] || { echo "error: no completed turns on one side" >&2; exit 1; }

	warn_if_incomparable "$BASELINE" "$env_file"

	echo ""
	echo "  baseline:"
	print_stages "$BASELINE"
	echo ""
	echo "  current:"
	print_stages "$env_file"

	delta_pct="$(jq -n --argjson n "$now" --argjson b "$base" '(($n - $b) / $b) * 100')"
	printf "\n  gated metric (median stt stage): %.0f ms -> %.0f ms   %+.1f%%  (fail above +%s%%)\n\n" \
		"$base" "$now" "$delta_pct" "$REGRESSION_PCT"

	if jq -e -n --argjson d "$delta_pct" --argjson t "$REGRESSION_PCT" '$d > $t' >/dev/null; then
		echo "  ✗ REGRESSION: stt stage is more than ${REGRESSION_PCT}% slower than baseline"
		exit 1
	fi
	echo "  ✓ no regression"
	;;

*)
	echo "usage: bench.sh {run|baseline|diff|save <label>}" >&2
	exit 2
	;;
esac
