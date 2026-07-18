# Demos

## tool-calls.gif

Live Samantha conversation (text mode) where Ollama **actually calls** `list_files`.

Requires:

- `just build` → `./bin/samantha`
- Ollama running with a tools-capable model (`ollama_model`)
- `VOICE_TOOLS_ENABLED=true`

Regenerate:

```bash
just build
VOICE_TOOLS_ENABLED=true vhs demos/tool-calls.tape
```

This is **not** a unit-test recording. You should see lines like:

```text
🔧 list_files (.)
✓ list_files → …
```
