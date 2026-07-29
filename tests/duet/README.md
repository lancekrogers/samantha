# duet — multi-instance conversation harness

Runs a scripted conversation between two (or more) real samantha instances in
tmux and leaves a self-contained artifact directory for review. This is the
feedback loop for the two-agent voice experience (WI-dc9e33): every
conversation-quality question becomes "write a scenario, run it, read one
directory." Full design: `workflow/design/samantha-bugfix-2026-07-24/harness-design.md`
in the campaign repo.

## Run

```bash
just build
just duet crosstalk            # tests/duet/scenarios/crosstalk.yaml
just duet bargein-typed        # needs TTS assets; first run may download
go run ./tests/duet -scenario tests/duet/scenarios/crosstalk.yaml -keep
```

Requires `tmux` and (for the stock scenarios) a live ollama at
`localhost:11434` with the scenario's models pulled. `-keep` leaves the tmux
session alive for hands-on inspection (`tmux attach -t duet-<ts>`).

## What a run produces

```
runs/crosstalk-<ts>/
  scenario.yaml       verbatim copy
  timeline.jsonl      every trigger/bridge decision + causal tap seq
  report.md           summary: turns, degraded, errors, leakage, exchanges
  metrics.json        machine-readable rollup (what `expect:` evaluates)
  <instance>/         transcript.jsonl · native-diagnostics.log · pane.txt · audio/
```

Exit code is non-zero when any `expect:` fails — scenarios double as
regression tests (`bargein-typed` fails on pre-B2-fix builds by design).

## Scenarios are the whole customization surface

Personas, prompts (inline or file), models, voices, launch flags, triggers
(`at:` / `after_reply:` / `while_speaking:`), bridge policy, and assertions
are all YAML — see the stock scenarios and `scenario.go` for the schema.
Unknown keys are load errors on purpose.

Instances run with their own disposable `$HOME`; the model cache
(`XDG_CACHE_HOME`) and `PATH` pass through so runtime assets aren't
re-downloaded per run. Audio capture uses `--debug-audio`; the conversation
record uses `--transcript-log` (both real samantha flags).

## Notes

- `bridge.mode: text` relays finalized replies between instances by typing
  them into the peer's composer — deterministic, no audio routing. A repeated
  near-identical reply halts that direction (`loop_detected`) instead of
  burning the scenario duration.
- Voice barge-in (B3) timing is tested at the pipeline layer in
  `tests/voiceflow`; `bridge.mode: audio` (loopback devices) is deliberately
  deferred to v2.
