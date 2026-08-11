#!/usr/bin/env just --justfile
# Latency benchmarking and the recorded utterance corpus.
#
# Loaded as a module from the root justfile, so justfile_directory() is the repo
# root (the module's own pwd is .justfiles/ — do not build paths from pwd).

repo := justfile_directory()
manifest := repo / "testdata/corpus/manifest.json"

[private]
default:
    @just --list --justfile {{source_file()}}

[doc('List audio input devices for recording')]
devices:
    @{{repo}}/scripts/record-corpus.sh --devices

[doc('Record corpus utterances (interactive). Override input with AUDIO_DEVICE=:N')]
record:
    @{{repo}}/scripts/record-corpus.sh

[doc('Validate the corpus: manifest schema, files present, all categories covered')]
corpus-check:
    #!/usr/bin/env bash
    set -euo pipefail
    cd "{{repo}}"
    manifest="testdata/corpus/manifest.json"
    fail=0

    schema=$(jq -r '.schema // ""' "$manifest")
    [[ "$schema" == "samantha.corpus.v1" ]] || { echo "✗ bad schema: '$schema'"; fail=1; }

    total=$(jq '.samples | length' "$manifest")
    echo "  samples: $total"

    # Every listed WAV must exist. A manifest referencing a missing file is worse
    # than an empty one: the benchmark would silently score fewer samples.
    while read -r p; do
        [[ -f "testdata/corpus/$p" ]] || { echo "✗ missing file: $p"; fail=1; }
    done < <(jq -r '.samples[].path' "$manifest")

    # Every sample needs an expected transcript or accuracy cannot be scored.
    blank=$(jq '[.samples[] | select((.expect // "") | gsub("^\\s+|\\s+$";"") == "")] | length' "$manifest")
    [[ "$blank" -eq 0 ]] || { echo "✗ $blank sample(s) have an empty expect field"; fail=1; }

    for c in short_command thoughtful_pause noisy_room; do
        n=$(jq --arg c "$c" '[.samples[] | select(.category == $c)] | length' "$manifest")
        echo "  $c: $n"
        if [[ "$n" -eq 0 ]]; then
            echo "✗ category '$c' has no samples"
            fail=1
        fi
    done

    unknown=$(jq -r '[.samples[].category] - ["short_command","thoughtful_pause","noisy_room"] | unique | join(", ")' "$manifest")
    [[ -z "$unknown" ]] || { echo "✗ unknown categories: $unknown"; fail=1; }

    if [[ "$fail" -ne 0 ]]; then
        echo ""
        echo "corpus-check FAILED"
        exit 1
    fi
    echo ""
    echo "✓ corpus valid"

[doc('Print manifest as repeatable --audio-fixture/--expect-text benchmark flags')]
corpus-args:
    @cd "{{repo}}" && jq -r '.samples[] | "--audio-fixture=testdata/corpus/\(.path) --expect-text=\(.expect)"' testdata/corpus/manifest.json | tr '\n' ' '
