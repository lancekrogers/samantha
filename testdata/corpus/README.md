# Utterance corpus

Recorded speech used by two measurements that must agree on the same input:

1. **Latency baseline** (`just bench`) — full-turn timings including capture,
   VAD and STT, not just the model.
2. **STT accuracy comparison** — batch vs streaming recognisers scored against
   the expected transcripts here.

Recorded once, read by both. 16 kHz mono 16-bit PCM WAV — the format the STT
path already expects.

## Recording

```bash
just bench devices        # find your input, if the default is wrong
just bench record         # interactive; one sample per round
just bench corpus-check   # validate before relying on it
```

Say each line exactly as you typed it — that text becomes the expected
transcript accuracy is scored against. Aim for roughly 30 samples.

## Categories, and why the mix matters

| Category | What it is |
|---|---|
| `short_command` | Crisp, quickly-ended requests. Establishes the fast-path floor. |
| `thoughtful_pause` | Longer speech with a **genuine mid-sentence pause** — you stop to think for a beat, then continue. |
| `noisy_room` | Realistic background noise. Guards against tuning that only holds in silence. |

**`thoughtful_pause` is the load-bearing category.** The first latency change in
this track shortens the VAD silence window, and the risk is that it endpoints
early and cuts people off mid-thought. A corpus of only crisp commands cannot
detect that — nothing in it has a pause to truncate — so it would report the
change as safe regardless of whether it is.

Worth knowing: the benchmark alone can never police this. A truncated utterance
produces a *faster* turn, so the metric moves the right way while the behaviour
gets worse. That is why the corpus carries this category, and why the change it
gates also has a human dogfooding gate behind it.

## Manifest

`manifest.json`, schema `samantha.corpus.v1`:

```json
{
  "samples": [
    { "path": "thoughtful_pause-01.wav",
      "expect": "I was thinking we could, hm, maybe start with the config",
      "category": "thoughtful_pause",
      "notes": "paused after 'could'" }
  ]
}
```

`notes` is free text and worth using — "paused ~1s after 'because'" is exactly
the detail you want when a sample later behaves oddly.

Validated by `just bench corpus-check` and by `tests/corpus`, which skips while
the corpus is empty and enforces strictly once it is not.

## Feeding it to the benchmark

`just bench corpus-args` expands the manifest into the repeatable
`--audio-fixture` / `--expect-text` flags `samantha benchmark` already accepts,
so nothing in the Go benchmark plumbing needed changing to consume it.

## Git

The WAVs are committed. ~30 short utterances at 16 kHz mono is a few MB, which
is fine; keep samples to a sentence or two rather than paragraphs.
