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

## Reporting Issues

When filing a bug, include:

- operating system and architecture
- Go version
- selected brain, STT, and TTS providers
- relevant command output
- whether microphone/speaker permissions were granted

## License

By contributing, you agree that your contributions will be licensed under the Apache License 2.0, and you confirm you have the right to submit them under that license (Apache-2.0 §5).
