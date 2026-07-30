# ADR 0003: Qwen TTS Subprocess IPC Remains Product Default

## Status

Accepted — **subprocess warm worker remains the product IPC path**; cgo
in-process is deferred until measurement shows material TTFA benefit after
stage-B engine streaming lands.

## Context

Design WI-1a04ee requires a forced decision: after the streaming subprocess
works, either ship cgo/libqwen3tts when IPC is a material fraction of TTFA, or
record bench proof that subprocess framing is negligible and keep it.

Product path today:

- Long-lived `qwen3-tts-worker` child process.
- Control: JSON lines; audio: float32 little-endian PCM frames (native protocol).
- Soft-cancel control messages; progressive sentence feed from the Go pipeline.

Stage-A generation is still whole-utterance at the engine layer; pipeline
chunking improves perceived latency. Full wall / warm TTFA is dominated by
model work (see lab `qwen3-tts-native` `docs/latency/`).

## Decision

1. **Ship subprocess IPC** as the product default for Samantha Qwen TTS.
2. **Do not ship cgo** in this cutover train.
3. Revisit cgo only if, after stage-B progressive PCM from the engine,
   framing/copy overhead is measured as a material share of TTFA (guideline:
   multi-percent of warm TTFA on short phrases) **and** an ABI-stable
   `libqwen3tts` exists in the lab packaging.

## Evidence (order-of-magnitude)

| Source | Observation |
|--------|-------------|
| Lab warm worker latency JSON (`docs/latency/worker_warmish.json` and platform CUDA benches) | Full wall / synth time is hundreds of ms to multi-second; not IPC-bound |
| Protocol | JSON control + binary PCM; no base64 on native path |
| Stage-A engine | Whole-utterance generation; TTFA gated by model, not pipe |

IPC framing and process boundary overhead is therefore **negligible** relative
to generation. cgo would not move conversation feel until the engine streams
PCM earlier.

## Consequences

- Samantha stays free of cgo build complexity for Qwen TTS.
- Lab may still prototype `libqwen3tts` without product coupling.
- Progressive pipeline TTS + soft-cancel remain the user-visible latency levers
  on the product side for this train.

## Alternatives Considered

1. **Ship cgo now without stage-B streaming** — rejected; cost without TTFA win.
2. **Unix socket + shared memory** — not needed while subprocess JSONL/PCM is fine.
