# Samantha docs

The [root README](../README.md) is the first-visit page: what Samantha is, how to install it, and how to talk to it.

Everything else lives here.

## Guides

| Page | Topic |
|------|--------|
| [Install](install.md) | Requirements, Homebrew, Linux archive, source builds |
| [Usage](usage.md) | Local voice, remote access, commands, TUI, personas |
| [Narration](narration.md) | `render`, audiobooks, Calibre, narrate pipeline |
| [Meetings](meetings.md) | Meeting recorder, speaker labels, campaign routing |
| [Configuration](configuration.md) | `config.yaml` keys, Agent Skills |
| [Development](development.md) | Build, tests, model assets, benchmarks |

## Reference

| Page | Topic |
|------|--------|
| [Serve protocol](serve-protocol.md) | Remote serve HTTPS + WebSocket contract |
| [Qwen3-TTS spike](qwen3-tts-spike.md) | Native Qwen3-TTS product path (worker + GGUF) |
| [AEC probe](aec-probe.md) | AEC / voice-frontend probe notes |
| [ADRs](adr/) | Architecture decision records |
| [Benchmarks](benchmarks/README.md) | Committed latency measurements |
| [Prompts](prompts/P3-README.md) | Prompt fixtures |

## Schemas

- [persona.schema.json](schemas/persona.schema.json)
- [prompt.schema.json](schemas/prompt.schema.json)
