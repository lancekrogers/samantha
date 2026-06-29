# Samantha

Samantha is a low-latency voice assistant for AI coding.

It captures speech, transcribes it locally, streams the prompt through an AI coding backend, chunks the response into sentences, and speaks those sentences as soon as they are ready.

## Features

- Local speech-to-text with sherpa-onnx streaming Zipformer by default.
- Optional utterance-final STT through sherpa-onnx Whisper or whisper.cpp.
- Local text-to-speech with Kokoro through sherpa-onnx.
- Claude CLI and Ollama brain providers.
- Voice activity detection with Silero.
- Streaming playback, barge-in handling, and session resume.
- Local benchmark command for prompt and STT fixture measurements.

## Architecture

Concurrent goroutine pipeline targeting <2s end-to-end latency:

```text
Mic -> VAD -> STT -> Brain -> Sentence Chunker -> TTS -> Speaker
```

Implemented providers:

| Layer | Providers |
|-------|-----------|
| Brain | `claude`, `ollama` |
| STT | `sherpa`, `sherpa-offline`, `whispercpp` |
| TTS | `kokoro` |
| VAD | Silero through sherpa-onnx |
| Audio | miniaudio through malgo |

Runtime model files are downloaded on first use and stored under `models_dir`.

## Requirements

- Go 1.26+
- [just](https://github.com/casey/just)
- A working microphone and speaker for voice mode
- Claude CLI on `PATH` when `brain_provider=claude`
- Ollama running locally when `brain_provider=ollama`
- Docker or a compatible container runtime for integration tests

macOS users may need to grant microphone permission to the terminal app used to run Samantha.

## Install

```bash
just install    # Build, sign on macOS when possible, and install to $GOBIN
```

For development builds:

```bash
just build
just run -- --text
```

## Usage

```bash
samantha              # Launch the TUI, then start full voice mode
samantha --no-tui     # Start directly without the launcher
samantha --text       # Text input, voice output
samantha --no-voice   # Voice input, text output
```

### Commands

```bash
samantha config                         # View all config
samantha config tts_voice af_bella      # Set a config value
samantha voices                         # List available Kokoro voices
samantha voices --locale en-US          # Filter voices by locale
samantha providers                      # Show brain, TTS, and STT providers
samantha test                           # Test microphone and speaker
samantha benchmark --prompt "hello"     # Run a local benchmark
samantha resume <session-id>            # Resume a saved session
samantha continue                       # Continue the most recent session
```

## Configuration

Config lives at `~/.obey/agents/voice/samantha/config.yaml`. Values can also be overridden with environment variables where listed.

| Key | Default | Environment | Description |
|-----|---------|-------------|-------------|
| `brain_provider` | `claude` | `BRAIN_PROVIDER` | Brain backend: `claude` or `ollama` |
| `ollama_model` | empty | `OLLAMA_MODEL` | Ollama model name |
| `ollama_host` | `http://localhost:11434` | `OLLAMA_HOST` | Ollama server URL |
| `voice_tools_enabled` | `false` | `VOICE_TOOLS_ENABLED` | Enable voice-triggered tool calls |
| `tts_provider` | `kokoro` | `TTS_PROVIDER` | TTS backend |
| `tts_voice` | `af_heart` | `TTS_VOICE` | Kokoro voice name |
| `speech_speed` | `0.95` | | Playback speed |
| `stt_provider` | `sherpa` | `STT_PROVIDER` | STT backend: `sherpa`, `sherpa-offline`, or `whispercpp` |
| `sherpa_streaming_model` | `en-2023-06-26` | `SHERPA_STREAMING_MODEL` | sherpa-onnx streaming model |
| `whisper_model` | `small` | `WHISPER_MODEL` | sherpa-onnx Whisper model size |
| `whisper_quantized` | `true` | | Prefer quantized Whisper models |
| `whispercpp_binary` | `whisper-cli` | `WHISPERCPP_BINARY` | whisper.cpp CLI executable |
| `whispercpp_model` | `base.en` | `WHISPERCPP_MODEL` | Downloadable whisper.cpp model name |
| `whispercpp_model_path` | `~/.cache/samantha/models/whispercpp/ggml-base.en.bin` | `WHISPERCPP_MODEL_PATH` | whisper.cpp model path |
| `vad_enabled` | `true` | | Enable voice activity detection |
| `vad_silence_duration` | `0.5` | | Seconds of silence before ending speech |
| `agent_name` | `Samantha` | | Display name |
| `models_dir` | `~/.cache/samantha/models` | `MODELS_DIR` | Model download directory |
| `language` | `en-US` | | Recognition language |
| `max_history` | `10` | | Saved conversation history length |
| `listen_timeout` | `10` | | Listen timeout in seconds |
| `phrase_time_limit` | `30` | | Maximum phrase length in seconds |

Legacy Claude and Fish Audio keys still exist in config for compatibility, but the implemented providers are the ones listed above.

## Development

```bash
just              # Show available commands
just build        # Vet and compile using the build dashboard
just run -- --text
just talk         # Full voice mode
just lint         # go fmt and go vet
just deps         # Update and tidy dependencies
```

The build dashboard is wired through `internal/buildutil` and the project keeps using that workflow.

### Testing

```bash
just test unit                 # Unit tests
just test pkg config           # Test a specific internal package
just test integration          # Container integration tests
just test integration-verbose  # Integration tests with full output
go test ./...                  # Plain Go test fallback
```

Integration tests expect `bin/linux/samantha` to exist. The build dashboard creates it for the integration workflow.

### Voice Utilities

```bash
just voice test
just voice voices
just voice providers
```

## License

Samantha is released under the MIT License. See [LICENSE](LICENSE).
