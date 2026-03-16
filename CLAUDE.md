# Samantha — Ultra-Low-Latency Voice Assistant for Claude Code

## Overview

Go rewrite of samantha-cli. Voice interface to Claude Code with <2s perceived latency. Uses sherpa-onnx-go for local STT (whisper) + TTS (kokoro) + VAD (silero), and claude-code-go for streaming Claude responses via Max subscription.

## Architecture

```
Voice → Mic Capture → VAD → Whisper STT → Claude (streaming) → Sentence Chunker → Kokoro TTS → Audio Playback
```

All pipeline stages run as concurrent goroutines connected by channels. TTS generates sentence N+1 while playing sentence N.

### Package Layout

- `cmd/samantha/` — CLI entry point (cobra)
- `internal/app/` — Application orchestrator
- `internal/audio/` — Mic capture, VAD, audio playback
- `internal/stt/` — STT provider interface + sherpa-onnx whisper
- `internal/tts/` — TTS provider interface + sherpa-onnx kokoro
- `internal/brain/` — Claude integration, sentence chunker, personality
- `internal/pipeline/` — Concurrent pipeline orchestration
- `internal/config/` — Viper config, model download
- `internal/events/` — Typed event bus
- `internal/session/` — Conversation history persistence
- `internal/ui/` — Terminal UI, mic animation

## Key Dependencies

- `github.com/lancekrogers/claude-code-go` — Claude SDK (streaming, Max billing)
- `github.com/k2-fsa/sherpa-onnx-go` — STT + TTS + VAD
- `github.com/spf13/cobra` — CLI
- `github.com/spf13/viper` — Config
- Audio I/O: malgo or portaudio + oto

## Build & Run

```bash
just build          # Build binary to bin/samantha
just test           # Run tests
just lint           # go vet
just talk           # Full voice mode
just text           # Text input + voice output
just voice test     # Test mic and speaker
```

## Config

Reads `~/.samantha/config.yaml` (backward-compatible with Python version). Key settings:

- `tts_provider`: kokoro (default)
- `tts_voice`: af_heart (default)
- `stt_provider`: sherpa (whisper)
- `whisper_model`: small
- `vad_silence_duration`: 0.5
- `claude_model`: claude-sonnet-4-5-20250514

## Go Conventions

Follow root CLAUDE.md standards:
- Always pass `context.Context` as first param for I/O
- Small interfaces (3-5 methods max)
- Dependency injection, no global state
- Files <500 lines, functions <50 lines
- Error wrapping with context
- Table-driven tests

## Models

Auto-downloaded to `~/.cache/samantha/models/` on first run:
- Whisper small (~250MB)
- Kokoro v1.0 (~310MB) + voices (~27MB)
- Silero VAD (~2MB)

## Festival

Linked to festival `samantha-go-SG0001`. Use `fest next` for current task.
