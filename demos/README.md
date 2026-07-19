# Demos

## tool-calls.gif — main Samantha TUI + live tool call

Full Bubble Tea UI (not `--no-tui`):

1. **Launcher** — New conversation  
2. **Chat** — `Samantha · Chat · Activity`  
3. **Tools** — `list_files` start/finish lines in the transcript  
4. Samantha’s reply after the tool result  

```bash
just build
vhs demos/tool-calls.tape
```

Still frames: `demos/frames/launcher.png`, `demos/frames/chat-tools.png`
