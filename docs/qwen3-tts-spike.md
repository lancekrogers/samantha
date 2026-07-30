# Qwen3-TTS product path (native-only)

Samantha supports Qwen3-TTS as an optional local TTS provider while keeping
Kokoro as the default and fallback. **Product inference is native-only**: a
`qwen3-tts-worker` binary plus GGUF models under `models_dir/qwen3-tts`. There
is no managed Python / uv / torch runtime on the product path after cutover
(festival SN0001 phase 008).

## Native package setup

Settings → TTS → Qwen3-TTS (or CLI below) installs a release tarball when
`qwen_tts_native_url` (or `SAMANTHA_QWEN_NATIVE_URL`) is set:

- layout under `models_dir/qwen3-tts/` (`bin/qwen3-tts-worker`, `models/*.gguf`, presets);
- multi-tier packages may include 0.6B and 1.7B; product default tier is `0.6b`;
- optional `qwen_tts_native_sha256` / `SAMANTHA_QWEN_NATIVE_SHA256` for verify.

Build and package the worker in the lab repo `qwen3-tts-native` (Metal / CUDA /
CPU). Samantha only downloads, inspects, and runs the installed package.

Equivalent CLI after `tts_provider: qwen3-tts`:

```text
# configure qwen_tts_native_url to a release tarball for your OS/arch
samantha models ensure --tts
samantha models status --tts
samantha doctor
samantha voices
```

No system Python or `qwen-tts` PyPI package is required for synthesis.

### Legacy Python trees

Older Samantha installs may still have a uv/Python tree under
`models_dir/qwen3-tts` (`worker/qwen_worker.py`, `runtime/`, `bin/uv`). That
tree is **not** used for inference. `doctor` reports it as an error with
remediation; product ensure installs the native package instead. To remove the
old tree after native is installed, quarantine via the helpers in
`internal/qwen` (`DetectLegacyPython` / `QuarantineLegacyPython`) or delete
the quarantine path manually.

## Preset voices

CustomVoice-class presets (nine model-native speakers) ship with the native
package:

```text
Vivian  Serena  Uncle_Fu  Dylan  Eric  Ryan  Aiden  Ono_Anna  Sohee
```

Settings → Voice lists presets when the native package is installed.
Settings → Language exposes the supported language list. Preview and normal
synthesis send the selected speaker and language to the native worker JSONL
protocol. Progressive sentence TTS is handled in the Go pipeline (chunk →
synth queue); stage-A engine generation remains whole-utterance per sentence.

Batch rendering records native model identity (tier + worker), mode, language,
and speaker in its synthesis identity and manifest.

## Worker lifecycle

Samantha starts one native `qwen3-tts-worker` process and keeps it warm.
Go and the worker speak a versioned JSONL control channel with float32 PCM
frames. Soft-cancel is supported on the native path for barge-in / progressive
pipeline interruption.

Context cancellation and timeouts terminate a wedged process group but leave
the provider usable; the next request starts a fresh worker. Unexpected
protocol failure receives one supervised restart and one retry before the
configured Kokoro one-sentence fallback (conversation / remote). Preview,
speaker tests, and batch narration remain fail-closed.

## Configuration

Product default (empty binary and model → managed selection → native package):

```yaml
tts_provider: qwen3-tts
# leave qwen_tts_binary and qwen_tts_model empty
qwen_tts_model_tier: 0.6b   # or 1.7b when present in the package
qwen_tts_voice: Vivian
qwen_tts_language: Auto
qwen_tts_native_url: https://example.invalid/qwen3-tts-native-….tar.gz
```

### Explicit external worker (lab / advanced)

```yaml
tts_provider: qwen3-tts
qwen_tts_binary: /path/to/qwen3-tts-worker
qwen_tts_model: /path/to/native/model-dir
```

One-shot `qwen3-tts-cli` remains available for debug; it is not the product
warm path.

## Lab repository

Engine packaging, platform smoke, and CUDA validation live in
`qwen3-tts-native` (standalone lab; not a Go library for Samantha). See that
repo’s `docs/PLATFORMS.md` and `docs/DISTRIBUTION.md`.
