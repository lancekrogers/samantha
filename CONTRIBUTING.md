# Contributing

Thanks for taking the time to improve Samantha.

## Development Setup

1. Install Go 1.26+.
2. Install `just`.
3. Clone the repository.
4. Run `go test ./...`.
5. Run `just build` for the project build workflow.

Voice-mode development requires local microphone and speaker access. Integration tests require Docker or a compatible container runtime.

## Pull Requests

- Keep changes focused and describe the user-visible impact.
- Run `go test ./...` before opening a PR.
- Run `just build` when touching build, install, or runtime startup behavior.
- Include tests for behavior changes where practical.
- Do not include local config, model files, binaries, credentials, or generated review notes.

### Agent / PR reject list (Qwen TTS)

**One-liner:** Product Qwen3-TTS is native-only (`qwen3-tts-worker` + GGUF under
`models_dir/qwen3-tts`); never reintroduce managed Python/uv/torch at inference.

Reject PRs that:

- Embed or ensure a Python `worker.py` / `qwen_worker.py` for product TTS
- Install or pin `uv`, torch, or `qwen-tts` for Samantha voice sessions
- Treat a legacy uv tree under `models_dir/qwen3-tts` as a ready product runtime
- Add a dual “Python until native URL is set” product fallback
- Claim Base-only cutover (0.6B + 1.7B tiers remain in scope for packaging)

Offline HF→GGUF conversion in the **lab** (`qwen3-tts-native`) may use Python;
that must not enter the Samantha process tree during synthesis. See
`docs/adr/0002-no-managed-python-tts-runtime.md` and `docs/qwen3-tts-spike.md`.

## Reporting Issues

When filing a bug, include:

- operating system and architecture
- Go version
- selected brain, STT, and TTS providers
- relevant command output
- whether microphone/speaker permissions were granted

## License

By contributing, you agree that your contributions will be licensed under the Apache License 2.0, and you confirm you have the right to submit them under that license (Apache-2.0 §5).
