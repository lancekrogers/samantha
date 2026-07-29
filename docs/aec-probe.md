# AEC probe: measuring echo cancellation on real hardware

`samantha aec-probe` plays a known signal through the real playback path,
records what the microphone actually hears, and reports how much of it the echo
canceller removed.

## Why this is not a unit test

`internal/audio/voice_frontend_erle_test.go` scores the canceller against a
synthetic echo: one delayed, scaled copy of the far-end, perfectly linear, in a
silent room. That is enough to catch an unaligned reference — it is what caught
the 24 kHz tap-budget regression — but it cannot tell you whether the filter
works, because real rooms differ from that model in ways that all reduce
cancellation:

- **Reverberation.** Real echo is a room impulse response, not one tap. Anything
  arriving later than the filter's tap window is uncancellable.
- **Speaker nonlinearity.** NLMS is a linear filter. Laptop speakers distort at
  volume; a distorted echo has components no linear filter can subtract.
- **Clock drift.** Playback and capture are independent devices. The reference
  FIFO does not resample to track them, so alignment walks over a long utterance.
- **The delay estimate is inferred, not measured.** `referenceDelaySamples`
  computes latency from `(Periods-1) * PeriodSizeInFrames` with an assumed period
  count. This probe is the first thing that checks it against a real speaker.

## Running it

One run per device class. Each takes about 20 seconds.

```bash
just aec-probe LABEL=macbook-builtin
```

or directly:

```bash
samantha aec-probe --label macbook-builtin
```

### Before you start

- **Volume at a normal listening level**, or louder. Too quiet is the most common
  bad run — see below.
- **Nobody talking**, no music, no fans if avoidable. Both phases assume the mic
  hears only the speaker.
- **Do not wear headphones** unless headphones *are* the device class you are
  measuring. With headphones on, there is no echo path and the run measures
  nothing.

### The device classes worth covering

| Label | What it tells you |
|---|---|
| `macbook-builtin` | The default path. Most users, and the CarPlay analogue. |
| `airpods` | Bluetooth latency is expected to exceed the tap window entirely. |
| `usb-mic` | Mic far from speaker: long delay, weaker echo. |
| `studio-display` | Multi-channel output, different negotiated rate. |

Switch the system input/output devices between runs, or set `input_device` /
`output_device` in config.

## Reading the result

```
  delay estimated by player : 682 samples (42.63 ms)
  delay measured from chirp : 940 samples (58.75 ms, confidence 0.87)
  residual for the filter   : 258 samples (16.12 ms) against a 48.00 ms tap window
  echo cancellation (ERLE)  : +8.40 dB
  mic level                 : rms 0.0512, peak 0.310
```

- **residual** is `measured − estimated`, and it must be **positive and smaller
  than the tap window**. Positive means the estimate under-counts, which the
  filter can absorb with taps. Negative means it over-counts, which no number of
  taps can fix.
- **ERLE** is how much quieter the front-end's output is than its input. This is
  measured end-of-chain, so the noise suppressor and AGC are inside the number —
  which is the honest framing, because that is what VAD sees.
- **confidence** below 0.3 means the delay peak is not trustworthy.

### What counts as a good outcome

Roughly, and pending the first real data set:

- **≥ 6 dB** — the hand-rolled canceller is viable on this device. Tune from here.
- **2–6 dB** — marginal. Barge-in will be twitchy; needs investigation.
- **< 2 dB** — not working on this device. If that holds across device classes,
  it is the signal to adopt WebRTC AEC3 rather than keep tuning.

### When a run is not trustworthy

The probe refuses to let a bad run look like a result. Any warning means fix the
condition and run again rather than writing the number down.

The one to watch for, because it looks like success:

```
ERLE of +21.8dB at only 0.0080 echo RMS is the noise suppressor gating
near-silence, not echo cancellation
```

A quiet microphone makes the noise suppressor gate everything, so ERLE reads
superb while the canceller did nothing. **Raise the volume until `echo_rms` is
above 0.02.** This was the first smoke run's result, and it is exactly the
mistake this probe exists to prevent.

Others:

| Warning | Fix |
|---|---|
| `player never published a reference delay` | The playback device never opened. Check output device selection. |
| `no playback reference reached the canceller` | Front-end never got audio; check the run actually played. |
| `microphone heard almost nothing` | Volume up, or mic closer. |
| `microphone clipped` | Volume down. A clipped path is nonlinear and cannot judge a linear filter. |
| `delay correlation is weak` | Usually volume, or too much room noise. |
| `residual delay exceeds the tap window` | Real finding, not operator error — this device needs more taps or a different approach. Record it. |

## What each run leaves behind

```
aec-probe/<label>-<timestamp>/
  metrics.json    every number, plus warnings
  reference.wav   what the canceller was given as far-end
  mic-in.wav      what the microphone heard
  mic-out.wav     what the front-end passed to VAD
```

**Listen to `mic-in.wav` and `mic-out.wav`.** The dB figure is a summary; your
ears will tell you whether the echo is gone, whether it is smeared into
reverb-like residue, or whether the suppressor simply muted everything.

## After the session

Record the numbers per device in the explore work item
(`workflow/explore/samantha-aec-capture-quality`, `WI-2fe1df`) and continue to
step 2 (DELAY) with real per-device delays in hand.
