# Demos

## tool-calls.gif — main TUI + live tool call

Bubble Tea **Samantha** interface:

1. Launcher → **New conversation**
2. User asks for `list_files`
3. Transcript shows tool start/finish, then Samantha’s answer

Regenerate (repo root, Ollama up, tools-capable model):

```bash
just build
vhs demos/tool-calls.tape
```

Requires `VOICE_TOOLS_ENABLED=true` (set in the tape via isolated HOME config).
