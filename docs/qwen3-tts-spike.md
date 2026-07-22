# Qwen3-TTS managed provider

Samantha supports Qwen3-TTS as an optional local provider while keeping Kokoro
as the default and fallback. The normal product path is fully managed: select
Qwen3-TTS in TUI Settings and Samantha installs the runtime and recommended
CustomVoice model below `models_dir/qwen3-tts`.

## Managed setup

Settings installs pinned components:

- uv `0.11.30`, isolated below the Samantha model directory;
- uv-managed Python `3.12`;
- official `qwen-tts==0.1.1`;
- `Qwen/Qwen3-TTS-12Hz-0.6B-CustomVoice` at revision
  `85e237c12c027371202489a0ec509ded67b5e4b5`;
- Samantha's versioned worker adapter.

uv is installed with its official versioned installer in unmanaged mode, so it
does not modify shell profiles. uv's Python, package cache, worker, model, and
installation marker all remain under Samantha's configured model directory.
The Hugging Face snapshot is public and revision-pinned.

The equivalent CLI setup, after configuring `tts_provider: qwen3-tts`, is:

```text
samantha models ensure --tts
samantha models status --tts
samantha doctor
samantha voices
```

No system Python or manually installed Qwen executable is required.

## Preset voices

The managed CustomVoice model exposes its nine model-native speakers:

```text
Vivian  Serena  Uncle_Fu  Dylan  Eric  Ryan  Aiden  Ono_Anna  Sohee
```

Settings → Voice lists only voices belonging to the active provider. Preview
and normal synthesis send the selected Qwen speaker and language to the
official `generate_custom_voice` API. The pinned 0.6B tier does not advertise
instruction control. Batch rendering
also records the model revision, worker version, mode, language, and speaker in
its synthesis identity and manifest.

## Worker lifecycle

Samantha starts one isolated Python worker and loads the selected model once.
The worker and Go provider communicate over a versioned JSON-lines protocol.
Each request writes a validated WAV into a Samantha-owned temporary directory;
Go validates its sample rate, duration, and content before streaming PCM into
the existing playback pipeline.

Context cancellation and timeouts terminate a wedged worker process group.
Worker stdout is reserved for protocol messages and stderr is bounded before it
is attached to provider errors. A runtime failure remains eligible for the
configured one-sentence Kokoro fallback.

## External-worker compatibility

Advanced users may set both fields below to keep using the earlier
qwen3-tts.cpp-compatible contract:

```yaml
tts_provider: qwen3-tts
qwen_tts_binary: qwen3-tts-cli
qwen_tts_model: /path/to/native/model
```

That adapter invokes:

```text
qwen3-tts-cli -m <model-directory> -t <text> -o <temporary-wav>
```

Because that contract exposes no verified speaker or language flags, it remains
limited to the external model's default voice. Named CustomVoice speakers are a
feature of Samantha's managed official worker.

## Current mode boundary

This release installs and exposes CustomVoice preset speakers. The provider
contract already carries VoiceDesign and approved-clone fields, but those modes
remain unavailable until their separate model installers and consent-aware TUI
flows land. Reference-audio validation and consent gates remain in place.
