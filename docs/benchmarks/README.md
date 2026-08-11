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

## What is measured

`PlaybackStartElapsed` p50 — turn start to audio actually beginning to play.
Target: **under 1.2 s now, 800 ms goal**. Regressions over **10 %** fail.

The benchmark runs a fixed prompt set built into the binary, so runs are
comparable without pinning prompts here. When `testdata/corpus/` has recordings,
they are added as `--audio-fixture` / `--expect-text` arguments automatically,
which extends coverage from model latency to the *full* turn including capture,
VAD and STT.

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
works, insert a deliberate delay in the turn path — the brain-to-TTS boundary is
the clearest spot — and run `just bench diff`. It must fail. Restore the code
afterwards.

This is worth re-running after any change to the benchmark itself, because the
failure mode is silent: a gate that stopped comparing anything still passes.
