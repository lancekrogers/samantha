# Qwen3-TTS native provider spike

This branch adds an optional `qwen3-tts` provider alongside Kokoro. Kokoro
remains Samantha's default and fallback; selecting Qwen is an explicit local
configuration choice.

Samantha does not ship Python, model weights, or a Qwen runtime. The provider
starts an externally installed native worker once per synthesis request, using
the small file-based contract currently exposed by `qwen3-tts.cpp`:

```text
qwen3-tts-cli -m <model-directory> -t <text> -o <temporary-wav>
```

The WAV is read into float32 samples and emitted in chunks through the existing
`audio.PCMStream`. The child process is owned by a context with a configured
timeout, so cancellation kills the native process and closes the stream with a
useful error.

Configuration:

```yaml
tts_provider: kokoro          # unchanged default
qwen_tts_binary: qwen3-tts-cli
qwen_tts_model: /path/to/qwen3-tts.cpp/models
qwen_tts_timeout: 120
```

This is intentionally a provider seam, not a complete Qwen product feature.
The current upstream CLI is file-oriented and loads model/runtime state for each
request. Cold-start cost can be substantial, especially on CPU, so the default
120-second timeout is intentionally conservative and can be tuned with
`qwen_tts_timeout` for longer render segments. The provider places each worker
in its own process group on Unix so cancellation also cleans up forked helpers.

The native CLI currently does not expose Samantha's static voice or speed
controls. Qwen requests therefore use the model-native default voice/speed;
batch render rejects explicit `--voice`/`--speed` overrides and keeps those
unused fields out of manifests and resume identities. This spike does not add a
persistent worker, streaming-token protocol, static voice picker, or cloning
UI. Those can be added behind the same provider boundary after the native
worker contract and latency are validated. Voice cloning is therefore a
follow-up integration, not a reason to replace Kokoro.
