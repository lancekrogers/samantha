# Development

[Back to README](../README.md)

```bash
just              # Show available commands
just build        # Vet and compile using the build dashboard
just run -- --text
just talk         # Full voice mode
just lint         # go fmt and go vet
just deps         # Update and tidy dependencies
```

The build dashboard is wired through `internal/buildutil` and the project keeps using that workflow.

## Testing

```bash
just test unit                 # Unit tests
just test pkg config           # Test a specific internal package
just test integration          # Container integration tests
just test integration-verbose  # Integration tests with full output
just test audio-crackle        # Playback layout + crackle software regressions (CI-safe)
just test audio-hardware       # Opt-in: real speakers, Studio Display etc.
go test ./...                  # Plain Go test fallback
```

Integration tests expect `bin/linux/samantha` to exist. The build dashboard creates it for the integration workflow.

Playback crackle (Studio Display mono-client class) is guarded by `pkg/voiceagent/audio`
layout + crackle tests in normal `go test -race ./...`. After any change under
`pkg/voiceagent/audio`, also run `just test audio-hardware` on an affected machine and
confirm `--debug-audio` metadata reports `channels: 2` (not mono).

### Voice smoke tests (opt-in, require local models)

The STT provider loops (`pkg/voiceagent/stt`) are covered by deterministic unit tests
that use fakes, so they run without model files. Real end-to-end voice behavior
depends on local STT/VAD/TTS models and is therefore opt-in. When the models are
installed (`samantha models ensure`, once available), run the smoke plan:

| Scenario | Expectation |
|----------|-------------|
| Short utterance (`hello samantha`) | final transcript within ~2s; finalizes on the source's EOF/silence, not a phrase timeout |
| Long utterance | partial/final transcript; caps at the max-utterance length |
| Silence only | times out with no final transcript |
| Finite fixture EOF | terminates promptly on the explicit final frame, no hang |

```bash
# Deterministic, no models needed:
go test ./pkg/voiceagent/stt ./pkg/voiceagent/endpoint ./pkg/voiceagent/audio

# Pipeline flow with stubbed stages (no models, no network):
go test -tags integration ./tests/voiceflow      # turn state machine + barge-in

# Real-provider smoke (needs models + whisper.cpp binary for that provider):
just qwen-live                                   # real native Qwen voices + cancel/restart WAVs
samantha listen                                  # manual: speak a short command
```

### Latency benchmarks (protect the sub-2s goal)

The `samantha benchmark` command measures the perceived-latency milestones that
protect the <2s end-to-end goal and emits them as both a summary and (with
`--json`) a stable `TurnMetrics` record per turn: STT final, first model chunk,
first segment, first audio ready, playback start, playback complete, and — on a
barged-in turn — interruption latency. Threshold flags fail the run when a
milestone regresses, so the benchmark can gate CI or a local check:

```bash
# Prompt latency with budgets (any breach exits non-zero):
samantha benchmark --prompt "hello" \
  --max-total 2s --max-first-model-chunk 500ms --max-playback-start 800ms

# STT fixture latency + transcript accuracy (stops at the final transcript):
samantha benchmark --audio-fixture utterance.wav --expect-text "hello there" \
  --max-stt-final 2s --min-transcript-score 0.8

# Full voice turn from a recording — capture, VAD, STT, brain, TTS, playback:
samantha benchmark --full-turn-fixture utterance.wav --expect-text "hello there" \
  --max-playback-start 1200ms

# Machine-readable output for tracking regressions over time:
samantha benchmark --prompt "hello" --json bench.json
```

The three modes measure different paths, and only one of them measures the voice
pipeline end to end:

| Flag | Path | Use it for |
|---|---|---|
| `--prompt` (default) | brain → TTS, **non-streaming** — the whole reply is synthesized as one segment | Text-path regressions |
| `--audio-fixture` | capture → VAD → STT, stops at the final transcript | STT latency and accuracy |
| `--full-turn-fixture` | the real `RunTurn` path, end to end | Anything touching endpointing, sentence chunking, or time-to-first-audio |

`--full-turn-fixture` is the one to use when comparing VAD, STT-provider or
first-sentence changes: it is the only mode where `FirstSegmentElapsed` reflects
real sentence chunking rather than end-of-generation.

Interruption latency is reported only when a turn is interrupted; all milestones
are always present in the `--json` output.

Committed before/after artifacts live in [benchmarks/](benchmarks/README.md).

## Model assets and readiness

Local model assets are described by an asset manifest and managed by three
commands:

```bash
samantha models status        # read-only: which assets are installed vs missing
samantha models status --json # machine-readable for scripts
samantha models ensure        # download any missing assets (atomic + verified)
samantha doctor               # diagnose config, assets, and external binaries
```

`models status` and `doctor` are read-only and safe offline. Doctor validates
the selected Claude/Grok CLI or Ollama configuration without making a network
request, and exits non-zero on setup errors (a missing model asset remains a
warning that points you to `models ensure`). Downloads are reliable by
construction: each file is written to
a temp file, size/checksum-verified when known, and atomically renamed; archives
are extracted into a temp directory, verified, then promoted — so an interrupted
or corrupt download never lands a partial asset, and **re-running
`models ensure` cleanly recovers**.

### Cleaning unused assets

`models clean` never removes anything it has not shown you, and never treats an
asset your configuration or any persona references as unused — the required set
is the union of the global config, every persona profile, and every config key
that names an asset (including ones the active mode does not load, such as
`sherpa_streaming_model` while `stt_mode: offline`). If that set cannot be
resolved — an unreadable persona, an unsupported model name — the command exits
non-zero and deletes nothing.

```bash
samantha models clean --unused --dry-run          # list candidates + "Kept (N)" with reasons
samantha models clean --unused --dry-run --json > plan.json
samantha models clean --unused --yes --plan plan.json --json
```

| Flag | Meaning |
| --- | --- |
| `--unused` | Required. Only unused-asset cleanup is supported. |
| `--dry-run` | List what would be removed and what is kept, with reasons. Deletes nothing. |
| `--yes` | Delete. Exactly one of `--dry-run` or `--yes` is required. At a terminal it prints the list and asks for confirmation. |
| `--plan <file\|->` | Apply exactly the reviewed plan: the whole `--dry-run --json` document (a bare `plan_id` is refused — it does not name the install it came from). Required with `--yes` whenever there is no terminal to confirm at, and always with `--yes --json`. |
| `--json` | Machine-readable output (`schema_version: 2`). |

The dry-run JSON carries `candidates[]` (with `size_bytes`, `category:
junk|asset`, `kind`), `protected[]` (each kept path with the persona or config
key that keeps it), `total_bytes`, and `plan_id`. An apply recomputes the
candidate set and refuses when it no longer matches the plan — or when the plan
was captured against a different `models_dir` — printing
`{"error":"plan_changed","plan_id":…,"current_plan_id":…}` and deleting
nothing; re-run the dry run and review the new list. A successful apply reports
`deleted[]`, `skipped[]` (with a reason) and `bytes_freed`. Every other `--json`
failure arrives on stdout as `{"error":"required_assets|plan_invalid|delete_failed",
"message":…}`, so a front end never has to parse a banner off stderr.

Automated tests cover download/extraction reliability with fake HTTP servers (no
network). To verify the **real** assets manually:

```bash
samantha models status        # confirm what's missing
samantha models ensure        # download from the real release URLs
samantha doctor               # confirm everything reports OK
```

## Voice utilities

```bash
just voice test
just voice voices
just voice providers
```
