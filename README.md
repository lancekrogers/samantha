# Samantha

Ultra-low-latency voice assistant for AI coding.

Talk to Claude while you code — Samantha captures your voice, streams it through Claude, and speaks the response back in real time with sentence-level TTS pipelining.

## Architecture

Concurrent goroutine pipeline targeting <2s end-to-end latency:

```
Mic → VAD → STT → Claude (streaming) → Sentence Chunker → TTS → Speaker
```

- **STT**: Whisper via sherpa-onnx (local, no API)
- **TTS**: Kokoro via sherpa-onnx (local, 82M params, multiple voices)
- **VAD**: Silero (speech endpoint detection ~300ms vs 3s silence timeout)
- **Brain**: Claude via claude-code-go (streaming, sentence-level chunking)
- **Audio**: miniaudio (capture + playback)

## Install

```bash
just install global    # Build, sign (macOS), install to $GOBIN
just install current   # Install last build from bin/
```

Requires Go 1.26+ and [just](https://github.com/casey/just). Model files are downloaded automatically on first run.

## Usage

```bash
samantha              # Full voice mode
samantha --text       # Text input, voice output
samantha --no-voice   # Voice input, text output
```

### Commands

```bash
samantha config                     # View all config
samantha config tts_voice af_bella      # Set a config value
samantha voices                     # List available TTS voices
samantha providers                  # Show TTS/STT providers
samantha test                       # Test mic and speaker
```

## Configuration

Config lives at `~/.obey/agents/voice/samantha/config.yaml`. All values have sensible defaults and can be overridden via environment variables.

| Key | Default | Description |
|-----|---------|-------------|
| `tts_provider` | `kokoro` | TTS backend (kokoro, edge, fish) |
| `tts_voice` | `af_heart` | Voice name |
| `speech_speed` | `0.95` | Playback speed |
| `stt_provider` | `sherpa` | STT backend (sherpa, google) |
| `whisper_model` | `small` | Whisper model size |
| `vad_enabled` | `true` | Voice activity detection |
| `vad_silence_duration` | `0.5` | Seconds of silence to end speech |
| `claude_model` | `claude-sonnet-4-5-20250514` | Default Claude model |
| `models_dir` | `~/.cache/samantha/models` | Model download directory |

## Development

Requires [just](https://github.com/casey/just) for task running.

```bash
just              # Show all commands
just quick        # Fast dev build to bin/
just run          # Build and run
just talk         # Full voice mode
just lint         # Format + vet
just deps         # Update and tidy deps
```

### Testing

```bash
just test all               # Unit + integration tests (dashboard)
just test unit              # Unit tests only (dashboard)
just test pkg config        # Test a specific package
just test integration       # Integration tests (requires Docker)
just test integration-verbose  # Integration tests with full output
```

### Building

```bash
just build default-build    # Vet + compile (dashboard)
just build release          # Quick binary build
just build full             # Clean → build → test → integration
just build clean            # Remove artifacts
```

### Voice

```bash
just voice test        # Test mic and speaker
just voice voices      # List TTS voices
just voice providers   # Show provider status
```
