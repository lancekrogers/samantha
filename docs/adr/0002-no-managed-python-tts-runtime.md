# ADR 0002: No Managed Python TTS Runtime

## Status

Accepted — product Qwen3-TTS path is **native-only** (cutover, festival SN0001 phase 008).

## Context

Samantha briefly shipped a managed Python CustomVoice path (uv-pinned Python,
`qwen-tts`, embedded `worker.py`, JSONL + base64 PCM). That path was a detour
while the native `qwen3-tts-worker` + GGUF package matured. Festival hard rules
forbid Python at inference and forbid expanding that surface.

## Decision

1. **Product inference** uses only the native package under
   `models_dir/qwen3-tts` (`qwen3-tts-worker` + GGUF) or an explicit external
   native worker/CLI. Empty `qwen_tts_binary` / `qwen_tts_model` means
   “managed selection” → native assets, **not** Python.
2. **Ensure** installs only the native tarball (`qwen_tts_native_url` /
   `SAMANTHA_QWEN_NATIVE_URL`). There is no uv/torch ensure and no embedded
   worker script in the product binary.
3. **Old trees:** if a pre-cutover uv/Python directory remains under
   `models_dir/qwen3-tts`, delete it by hand and re-ensure. No product
   migrator, dual path, or quarantine CLI is maintained (solo/dev installs).
4. **Offline convert** (HF → GGUF) may still use Python in the lab repo
   (`qwen3-tts-native`); that is CI/dev only, never the Samantha process tree
   during voice sessions.

## Consequences

- Settings, `models ensure --tts`, doctor, and `NewQwen3TTS` reject or ignore
  Python product installs.
- Agents and PRs must not reintroduce managed Python TTS (see CONTRIBUTING).
- Progressive TTS, soft-cancel, and latency work stay on the native protocol.

## Alternatives Considered

1. **Keep Python as fallback when native URL unset** — rejected; strands users
   on the slow path and re-expands the surface cutover deletes.
2. **Dual path indefinitely** — rejected; doubles support and violates
   festival hard rules.
3. **cgo in-process only** — orthogonal; IPC vs cgo is a separate measurement
   decision (see `docs/adr/0003-qwen-tts-subprocess-ipc.md`).

## Evidence

- `internal/qwen`: native Ensure/Inspect; legacy Detect/Quarantine only.
- `internal/tts.NewQwen3TTS`: managed selection requires native package.
- `docs/qwen3-tts-spike.md`: native-only product documentation.
