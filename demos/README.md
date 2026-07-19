# Demos

## tool-calls.gif — main Samantha TUI + live tool call

Full Bubble Tea UI (not `--no-tui`):

1. **Launcher** — New conversation  
2. **Chat** — `Samantha · Chat · Activity`  
3. **Tools** — `list_files` start/finish lines in the transcript  
4. Samantha’s reply after the tool result  

Recorded from the real Bubble Tea binary in a VHS PTY, using the same dark
terminal palette and compact geometry as termcast. The checked-in GIF is the
palette-optimized 960px version; there is no fake shell transcript or static
mock screen.

```bash
just demo
```

The recipe builds `./bin/samantha`, runs `vhs demos/tool-calls.tape` against a
disposable `$HOME`, then applies the termcast-style 20fps/960px palette pass.

Still frames: `demos/frames/launcher.png`, `demos/frames/chat-tools.png`
