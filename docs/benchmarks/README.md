# Latency benchmarks

Committed measurements of how long a voice turn actually takes, plus the recipe
that diffs a new run against them.

The rule this directory exists to enforce: **no latency change merges on
assertion.** A claim that something got faster is checked by a committed
before/after artifact, not by a reviewer's impression.

## Commands

```bash
just bench baseline   # measure and (re)write baseline.json — defines the reference
just bench diff       # measure and compare; fails on >10% regression
just bench run        # measure and print, writing nothing
just bench save L1    # measure and write L1-<date>.json (per-item delta artifact)
```

## What is measured, and what is gated

Runs drive whole voice turns from recorded audio (`--full-turn-fixture`), so the
measurement covers capture → VAD → STT → brain → TTS → playback.

Timings are reported **per fixture, per stage** — never pooled. Pooling
different utterances into one median mixes populations: a 1.5 s clip and a 4 s
clip have genuinely different stage timings, so a pooled figure moves when the
fixture mix changes rather than when performance does.

| Stage | What it is | Gated? |
|---|---|---|
| `stt` | Endpointing + recognition | **Yes.** Measured at ±0.6 % run to run |
| `model` | First model chunk after the transcript | No — swings 1.5 s–24 s on a large local model, and nothing in this track changes it. Gating it would produce only false failures |
| `synth` | First segment → first audio (first-sentence synthesis) | No, reported |

The gate is the **median stt stage across fixtures**, with a 10 % tolerance
against a measured 0.6 % noise floor — a ~16× margin.

That choice is deliberate: the VAD-window and STT-provider changes move the stt
stage directly, while end-to-end `PlaybackStartElapsed` is dominated by model
think time. An end-to-end gate measured 307 % spread and could not have detected
a 300 ms improvement; the stage gate detects a 500 ms one at +17 %.

**Fixtures.** A recorded corpus in `testdata/corpus/` is used when present. When
it is absent, the harness generates deterministic synthetic speech from a fixed
sentence list using the project's own TTS, so a baseline never blocks on a
recording session. Synthetic fixtures are reproducible and good for regression
detection, but they contain no natural mid-sentence pauses — they cannot judge
endpointing behaviour. The fixture kind is recorded in provenance and a diff
warns when it changes.

## Provenance is not optional

Every artifact records machine, CPU, OS, samantha commit, worktree cleanliness,
persona, brain provider, **brain model**, and TTS provider.

Hardware is the obvious one. The **model matters just as much**: the same code on
the same Mac produces entirely different numbers under a 135 M model than a 26 B
one. A baseline that does not say which model produced it cannot be compared
against, so `just bench diff` warns loudly when either the machine or the model
differs from the baseline's.

## Files

| File | What |
|---|---|
| `baseline.json` | The canonical reference. Overwritten only by a deliberate `just bench baseline`. |
| `<item>-<date>.json` | Per-item delta artifacts, e.g. `L1-2026-08-12.json`. The item id joins back to the requirement it closes. |

## Verifying the gate still bites

A regression gate that has never caught anything is decorative. To confirm it
works, insert a deliberate delay where the gated stage is measured — a
`time.Sleep` immediately before `metrics.sttFinal = time.Now()` in
`transcribeTurn` — and run `just bench diff`. It must fail. Restore the code
afterwards.

Verified 2026-08-11 with a 500 ms injection. Every fixture moved by almost
exactly the injected amount (+487 ms, +554 ms, +495 ms), the gated median went
2837 ms → 3332 ms (**+17.4 %**), and the recipe exited non-zero:

```
  gated metric (median stt stage): 2837 ms -> 3332 ms   +17.4%  (fail above +10%)
  ✗ REGRESSION: stt stage is more than 10% slower than baseline
```

This is worth re-running after any change to the benchmark itself, because the
failure mode is silent: a gate that stopped comparing anything still passes.
