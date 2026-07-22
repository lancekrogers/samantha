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

## persona-switch.gif — Settings → Persona switcher

Full Bubble Tea Settings flow for multi-persona voice agents:

1. **Launcher → Settings** — first tab is **Persona**
2. **List** — Samantha (active ✓) and Festival with provider · voice detail
3. **Switch** — select Festival; checkmark and status message update
4. **Esc** — return to launcher with the active persona applied

Uses a disposable `$HOME` with two pre-seeded `personas/<id>/persona.yaml`
profiles (no live mic or model download).

```bash
just demo-persona-switch
```

## qwen-voices.gif — managed Qwen setup and voice selection

Full Bubble Tea Settings flow for the managed Qwen provider:

1. **Settings → TTS** — the pinned CustomVoice 0.6B installation is ready while
   Kokoro remains active
2. **Select Qwen3-TTS** — mode, default voice, and language are persisted
3. **Settings → Voice** — all nine model-native Qwen speakers are visible
4. **Select Ryan** — the active check and launcher badge update to Qwen,
   CustomVoice, and Ryan

The tape creates a disposable completion-marker/file-layout fixture consumed by
the real production `Inspect` path. It does not mock the TUI or download the
multi-GB model; inference remains covered by provider fixtures and opt-in
real-model testing.

```bash
just demo-qwen-voices
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

## meeting-route-speaker.gif — route picker + meeting diarization

Full Bubble Tea app demonstrating the meeting notes routing UX:

1. **Settings → Meeting** — refresh destinations (`camp list --json` + config)  
2. **Settings → Meeting** — speaker diarization + note routing  
3. **Meeting start** — title (1/2) → destination pick (2/2) with discovered campaigns  
4. **Recorder** — brief demo STT, then stop auto-routes to the chosen campaign  

Fixture: `demos/fixtures/camp` (selected via `PATH` ahead of a real camp).  
Color: `env -u NO_COLOR` + `SAMANTHA_COLOR_PROFILE=ansi`.

```bash
just demo-meeting-route-speaker
```

## meeting-speakers.gif — multi-voice meeting + diarization status

Scripts a multi-person product marketing conversation and shows speaker
analysis status moving **queued → running → complete**, with labeled turns
(`[speaker-1] …`, `[speaker-2] …`).

No live mic. Uses `SAMANTHA_DEMO_MEETING_SPEAKERS=1`.

```bash
just demo-meeting-speakers
```

Related: real multi-voice audio fixture + integration suite:

```bash
just fetch-meeting-fixture   # yt-dlp YouTube meeting clip → 16 kHz WAV
just speakerflow             # tests + refreshes this GIF when vhs is installed
just test full               # unit + integration + voiceflow + speakerflow + audio-crackle
just all                     # clean + build + test full
samantha meeting analyze tests/fixtures/meetings/product-marketing-meeting-90s.wav
```

`just speakerflow` auto-fetches the fixture if missing and, when `vhs` is on
PATH, re-records `demos/meeting-speakers.gif`. Set `SPEAKERFLOW_SKIP_VHS=1` to
skip the GIF step (useful in CI).
