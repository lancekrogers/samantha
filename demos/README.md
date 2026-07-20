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

## meeting.gif — main launcher + Meeting recorder

Full Bubble Tea app (not a CLI stub):

1. **Launcher** — brand plate, status chips, **Meeting** selection  
2. **Title** — meeting name entry  
3. **Recorder** — live voice EQ, scripted speech, typed **note**, **Ctrl+B** ★ bookmark  
4. **Stop** — return to launcher  

`SAMANTHA_DEMO_MEETING=1` scripts STT events (no live mic). Color: `env -u NO_COLOR`
plus `SAMANTHA_COLOR_PROFILE=ansi` so the termcast theme paints bright cyan/amber/magenta.

```bash
just demo-meeting
```

## library.gif — Calibre Library browser / viewer

Full Bubble Tea app with a deterministic fake `calibredb` (no real Calibre
install or library required for recording):

1. **Launcher** — select **Library**  
2. **Browse** — title-ordered catalog loads on open  
3. **Detail** — metadata + description for one book  
4. **Search** — `/` filter (`go`)  
5. **Audiobook** — `a` fills Create audiobook with an EPUB/PDF path  

Fixture: `demos/fixtures/fake-calibredb` (selected via `calibredb_binary` in a
disposable `$HOME` config). Color: `env -u NO_COLOR` + `SAMANTHA_COLOR_PROFILE=ansi`.

```bash
just demo-library
```
