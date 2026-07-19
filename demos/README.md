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

## voice-meter.gif — compact voice EQ (listen / hear / speak)

Full Bubble Tea conversation UI with a calm equalizer strip (not flame art):

1. **Listening** — soft pulse + thin waveform floor  
2. **Hearing** — level-reactive EQ driven by mic energy  
3. **Speaking** — EQ driven by playback energy  

`SAMANTHA_DEMO_VOICE_ANIM=1` scripts the same bus events production turns use
(so the GIF does not depend on a live mic). The binary and PTY are still real.
The tape unsets `NO_COLOR` and forces `SAMANTHA_COLOR_PROFILE=ansi` so VHS
paints bright theme colors.

```bash
just demo-voice-meter
```
