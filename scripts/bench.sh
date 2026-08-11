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
SAMANTHA="${SAMANTHA_BIN:-${REPO_ROOT}/bin/samantha}"

# Regression tolerance and the absolute target from the latency spec.
REGRESSION_PCT="${BENCH_REGRESSION_PCT:-25}"
# Iteration 1 is discarded as warmup: ollama model load and kokoro first-synth
# dominate it, and a baseline that bakes in cold start cannot detect regressions.
ITERATIONS="${BENCH_ITERATIONS:-3}"
TARGET_PLAYBACK_START_MS="${BENCH_TARGET_PLAYBACK_START_MS:-1200}"

command -v jq >/dev/null 2>&1 || { echo "error: jq is required" >&2; exit 1; }
[[ -x "$SAMANTHA" ]] || { echo "error: $SAMANTHA not found or not executable (run: just build)" >&2; exit 1; }

mkdir -p "$BENCH_DIR"

# The metric the latency track is judged on: time from turn start to audio
# actually starting to play. Nanoseconds in the raw output.
METRIC="PlaybackStartElapsed"

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
		--arg dirty "$(git -C "$REPO_ROOT" diff --quiet 2>/dev/null && echo clean || echo dirty)" \
		--arg persona "$(cfg_get active_persona)" \
		--arg provider "${BRAIN_PROVIDER:-$(cfg_get brain_provider)}" \
		--arg model "${OLLAMA_MODEL:-$(cfg_get ollama_model)}" \
		--arg tts "$(cfg_get tts_provider)" \
		--arg at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
		'{machine: $machine, cpu: $cpu, os: $os, samantha_commit: $commit, worktree: $dirty,
		  persona: $persona, brain_provider: $provider, brain_model: $model, tts_provider: $tts,
		  recorded_at: $at}'
}

# A comparison across different hardware OR a different model is meaningless.
warn_if_incomparable() {
	local file="$1" field label current
	for pair in "machine:$(sysctl -n hw.model 2>/dev/null || echo unknown)" \
	            "brain_model:${OLLAMA_MODEL:-$(cfg_get ollama_model)}"; do
		field="${pair%%:*}"; current="${pair#*:}"
		label="$(jq -r --arg f "$field" '.provenance[$f] // "unknown"' "$file")"
		if [[ "$label" != "$current" ]]; then
			echo ""
			echo "  ⚠ ${field} mismatch: baseline recorded with '${label}', running with '${current}'"
			echo "    these numbers are not comparable; re-baseline to use the diff"
		fi
	done
}

# Corpus fixtures are optional: the text-prompt half of the benchmark runs
# without them, so a baseline can exist before the corpus is recorded.
corpus_args() {
	[[ -f "$MANIFEST" ]] || return 0
	local n
	n=$(jq '.samples | length' "$MANIFEST")
	[[ "$n" -gt 0 ]] || return 0
	jq -r --arg root "$REPO_ROOT" \
		'.samples[] | "--audio-fixture=\($root)/testdata/corpus/\(.path)", "--expect-text=\(.expect)"' "$MANIFEST"
}

measure() {
	local raw envelope
	raw="$(mktemp)"
	local args=()
	while IFS= read -r a; do [[ -n "$a" ]] && args+=("$a"); done < <(corpus_args)

	echo "  running benchmark ($([[ ${#args[@]} -gt 0 ]] && echo "prompts + ${#args[@]} corpus args" || echo "prompts only, corpus not recorded"))..." >&2
	# A threshold violation is reported in the JSON and via exit status; capture
	# the results either way and let the diff decide.
	"$SAMANTHA" benchmark --iterations "$ITERATIONS" --json "$raw" "${args[@]+"${args[@]}"}" >/dev/null 2>&1 || true

	[[ -s "$raw" ]] || { echo "error: benchmark produced no JSON (is a brain provider reachable?)" >&2; exit 1; }

	envelope="$(mktemp)"
	jq -n \
		--argjson provenance "$(provenance)" \
		--argjson results "$(cat "$raw")" \
		--argjson target "$TARGET_PLAYBACK_START_MS" \
		--argjson tolerance "$REGRESSION_PCT" \
		'{schema: "samantha.benchmark.v1",
		  provenance: $provenance,
		  thresholds: {playback_start_p50_ms: $target, regression_tolerance_pct: $tolerance},
		  results: $results}' >"$envelope"
	rm -f "$raw"
	echo "$envelope"
}

# Warm samples only: iteration 1 is warmup. Falls back to all samples when a
# run had a single iteration, so a one-off `run` still reports something.
warm_samples() {
	jq --arg m "$METRIC" '
		[.results[] | select(.mode != "stt")] as $all
		| ([$all[] | select(.iteration > 1)] | if length > 0 then . else $all end)
		| [.[] | .metrics[$m] // empty | select(. > 0) | . / 1000000]
		| sort' "$1"
}

# p50 of the metric across warm text-mode results, in milliseconds.
p50_ms() {
	warm_samples "$1" | jq 'if length == 0 then null else .[(length - 1) / 2 | floor] end'
}

# Spread of the warm samples, as a percentage of the median. This is the noise
# floor: a regression tolerance tighter than it produces false failures.
spread_pct() {
	warm_samples "$1" | jq '
		if length < 2 then null
		else (.[(length - 1) / 2 | floor]) as $med
		| ((.[-1] - .[0]) / $med * 100 | round)
		end'
}

case "${1:-diff}" in
run)
	env_file="$(measure)"
	jq '.' "$env_file"
	echo "  p50 ${METRIC}: $(p50_ms "$env_file") ms" >&2
	rm -f "$env_file"
	;;

baseline)
	env_file="$(measure)"
	p50="$(p50_ms "$env_file")"
	[[ "$p50" != "null" ]] || { echo "error: no ${METRIC} samples - refusing to write a baseline with no data" >&2; exit 1; }
	mv "$env_file" "$BASELINE"
	echo ""
	echo "  ✓ wrote $(realpath --relative-to="$REPO_ROOT" "$BASELINE" 2>/dev/null || echo "$BASELINE")"
	echo "    p50 ${METRIC}: ${p50} ms  (aspirational target < ${TARGET_PLAYBACK_START_MS} ms)"
	echo "    warm-sample spread: $(spread_pct "$BASELINE")% of median  (n=$(warm_samples "$BASELINE" | jq length))"
	echo "    model: $(jq -r .provenance.brain_model "$BASELINE")  machine: $(jq -r .provenance.machine "$BASELINE")"
	echo "    this run defines the reference for every later comparison"
	;;

save)
	label="${2:?usage: bench.sh save <label>   e.g. bench.sh save L1}"
	env_file="$(measure)"
	out="${BENCH_DIR}/${label}-$(date -u +%Y-%m-%d).json"
	mv "$env_file" "$out"
	echo "  ✓ wrote ${out}"
	echo "    p50 ${METRIC}: $(p50_ms "$out") ms"
	;;

diff)
	[[ -f "$BASELINE" ]] || { echo "error: no baseline at $BASELINE (run: just bench baseline)" >&2; exit 1; }
	env_file="$(measure)"
	now="$(p50_ms "$env_file")"
	base="$(p50_ms "$BASELINE")"
	rm -f "$env_file"

	[[ "$now" != "null" && "$base" != "null" ]] || { echo "error: missing ${METRIC} samples on one side" >&2; exit 1; }

	warn_if_incomparable "$BASELINE"

	printf "\n  baseline p50: %.1f ms\n  current  p50: %.1f ms\n" "$base" "$now"
	delta_pct="$(jq -n --argjson n "$now" --argjson b "$base" '(($n - $b) / $b) * 100')"
	printf "  delta:        %+.1f%%  (fail above +%s%%)\n\n" "$delta_pct" "$REGRESSION_PCT"

	if jq -e -n --argjson d "$delta_pct" --argjson t "$REGRESSION_PCT" '$d > $t' >/dev/null; then
		echo "  ✗ REGRESSION: p50 ${METRIC} is more than ${REGRESSION_PCT}% slower than baseline"
		exit 1
	fi
	if jq -e -n --argjson n "$now" --argjson t "$TARGET_PLAYBACK_START_MS" '$n > $t' >/dev/null; then
		echo "  ⚠ above the ${TARGET_PLAYBACK_START_MS} ms target (not a regression against baseline)"
	fi
	echo "  ✓ no regression"
	;;

*)
	echo "usage: bench.sh {run|baseline|diff|save <label>}" >&2
	exit 2
	;;
esac
