# Samantha

Samantha is a low-latency voice assistant.

It captures speech, transcribes it locally, streams the prompt through an AI coding backend, chunks the response into sentences, and speaks those sentences as soon as they are ready.

![Samantha conversation TUI](docs/images/tui-samantha.gif)

## Features

- Local speech-to-text with sherpa-onnx Whisper by default.
- Optional streaming STT through sherpa-onnx Zipformer and utterance-final STT through whisper.cpp.
- Local text-to-speech with Kokoro through sherpa-onnx.
- Optional native Qwen3-TTS (`qwen3-tts-worker` + GGUF package) with CustomVoice-class presets (Kokoro remains the default).
- Claude CLI and Ollama brain providers.
- Voice activity detection with Silero.
- Streaming playback, barge-in handling, and session resume.
- Local benchmark command for prompt and STT fixture measurements.
- Batch narration: render text, Markdown, HTML, URL articles, and EPUB to WAV (and optional MP3/M4B/...) with a resumable manifest — scriptable, no microphone.

## Architecture

Concurrent goroutine pipeline targeting <2s end-to-end latency:

```text
Mic -> VAD -> STT -> Brain -> Sentence Chunker -> TTS -> Speaker
```

Implemented providers:

| Layer | Providers |
|-------|-----------|
| Brain | `claude`, `ollama` |
| STT | `sherpa`, `sherpa-streaming`, `sherpa-offline`, `whispercpp` |
| TTS | `kokoro`, optional `qwen3-tts` |
| VAD | Silero through sherpa-onnx |
| Audio | miniaudio through malgo |

Runtime model files are downloaded on first use and stored under `models_dir`.
The optional native Qwen3-TTS package (`qwen3-tts-worker` + GGUF) installs under
`models_dir/qwen3-tts` from TUI Settings or `models ensure --tts`, using the
published platform release by default — no product Python/uv runtime.

## Requirements

- Go 1.26+
- [just](https://github.com/casey/just)
- A C compiler for source builds (`gcc` or `clang`; Samantha uses CGO)
- A working microphone and speaker for voice mode
- Claude CLI on `PATH` when `brain_provider=claude`
- Ollama running locally when `brain_provider=ollama`
- Docker or a compatible container runtime for integration tests

macOS users may need to grant microphone permission to the terminal app used to run Samantha.

## Install

### Linux amd64 archive

Tagged releases include a relocatable `samantha-linux-amd64.tar.gz` for
x86_64 glibc Linux. It contains the Samantha executable and its sherpa-onnx /
onnxruntime shared libraries; keep the executable and `lib/` directory
together.

```bash
tar -xzf samantha-linux-amd64.tar.gz
mkdir -p ~/.local/opt ~/.local/bin
mv samantha-linux-amd64 ~/.local/opt/samantha
ln -s ~/.local/opt/samantha/samantha ~/.local/bin/samantha
samantha --version
```

Ensure `~/.local/bin` is on `PATH`. Models are downloaded separately on first
setup:

```bash
samantha models ensure
samantha doctor --voice-devices
```

The release archive targets glibc Linux with Ubuntu 24.04 as its build and
compatibility floor. Source builds are also validated on Arch/EndeavourOS;
musl systems such as Alpine require a separate native build strategy.

### Homebrew (macOS)

```bash
brew install --HEAD lancekrogers/tap/samantha
```

Builds from source and bundles the sherpa-onnx/onnxruntime native libraries so
the binary is self-contained. `--HEAD` tracks the latest `main`; once a version
is tagged it installs without it. Grant your terminal microphone access under
System Settings → Privacy & Security → Microphone.

### From source

```bash
just install    # Build, sign on macOS when possible, and install to $GOBIN
```

For development builds:

```bash
just build
just run -- --text
```

On Linux, `just install` is a source/developer install and retains the native
libraries supplied through the Go module cache. Use the release archive when
you need a relocatable installation that remains valid after cleaning that
cache.

## Usage

### Local voice (TUI) — mic and speakers on this machine

```bash
samantha              # Launcher → full conversation TUI with voice
samantha --no-tui     # Start conversation directly (no launcher)
samantha --text       # Text input, voice output
samantha --no-voice   # Voice input, text output
```

### Remote access (phone / another device)

Any device on the same Tailscale tailnet can use remote voice. Open the Samantha
TUI and choose **Use on another device**. Pick **Tailscale** or **Same Wi‑Fi**,
then open the link on any phone, tablet, or laptop. The TUI shows pairing and
client setup when the mic needs trusted HTTPS, and stops when you leave
that screen.

The equivalent CLI path remains available for headless use:

```bash
# Easiest remote access over Tailscale (MagicDNS URL; real cert when available):
samantha serve --tailscale

# LAN (self-signed TLS; iOS Safari mic needs a real cert):
samantha serve

# Ops
samantha serve --revoke-tokens   # Invalidate the bearer; next serve mints a new one
samantha connect <host:port> --token <token>   # Debug text client
```

If a trusted cert is not available yet, remote access still starts in
**limited** mode: text works on every device; most desktop browsers can use
voice after one warning; some mobile browsers need trusted HTTPS. The TUI/CLI
print a **Client setup** link (`https://login.tailscale.com/admin/dns` →
enable **HTTPS Certificates**), then restart. Same flow for LAN or Tailscale.

On the client: open the printed URL → enter the pairing code → **Start** →
**Hold to Talk** (or type). Protocol for custom clients:
[docs/serve-protocol.md](docs/serve-protocol.md).

| Path | Command | Where audio lives |
|------|---------|-------------------|
| Full local voice | `samantha` (TUI) | This machine’s mic + speakers |
| Remote keyboard only | Termius/SSH → `samantha` | Still this machine |
| Remote voice (LAN/tailnet) | TUI → **Use on another device**, or `samantha serve` / `--tailscale` | Client mic + speakers via WebSocket |

### Commands

```bash
samantha config                         # View all config
samantha config tts_voice af_bella      # Set a config value
samantha config migrate --dry-run       # Preview explicit STT config migration
samantha config migrate --write         # Apply STT config migration with backup
samantha persona list                   # List voice agent personas
samantha persona show                   # Show the active persona profile
samantha persona use samantha           # Switch active persona (persists)
samantha voices                         # List available Kokoro voices
samantha voices --locale en-US          # Filter voices by locale
samantha providers                      # Show brain, TTS, and STT providers
samantha test                           # Test microphone and speaker
samantha benchmark --prompt "hello"     # Run a local benchmark
samantha resume <session-id>            # Resume a saved session
samantha continue                       # Continue the most recent session
samantha doctor                         # Diagnose config, assets, and binaries (read-only)
samantha models status                  # Which model assets are installed vs missing
samantha models clean --unused --yes    # Delete model assets not required now
samantha prompts list                   # List embedded and user prompt documents
samantha prompts show persona           # Show an assembled prompt document
samantha render notes.txt --out a.wav   # Batch-render a document to audio
samantha library list                  # Browse Calibre library (opt-in)
samantha library search "cryptography" # Search Calibre library (opt-in)
samantha library show 42               # Show one book's metadata
samantha serve --tailscale              # Remote voice for Tailscale clients
samantha serve --revoke-tokens          # Rotate serve bearer token
```

### TUI controls

The launcher offers the most recent conversation first, a scrollable recent
session list, an explicit new-conversation action, and a managed Tailscale
remote server screen. The remote screen exposes the URL and single-use pairing code,
supports copy/restart controls, and owns server shutdown. During a conversation,
the transcript follows new messages until you scroll away from the tail. Chat
and the activity timeline are separate full-width views, so the transcript does
not lose space in wide terminals. The composer supports wrapped, multiline
drafts and compacts to one row in short terminal splits. Type `/` to open the
command palette, use the arrow keys to select a match, and press `Enter` to run
the highlighted command (or `Tab` to complete it into the composer first).
`/help` lists every available command. `/settings` opens the TUI settings
screen and returns to the conversation when you press `Esc` or `q`. Slash
commands are local — they do not cancel speech recognition or block the chat.

| Key | Action |
|-----|--------|
| `Enter` | Send the current draft |
| `Ctrl+J` | Insert a newline in the draft |
| Mouse wheel / trackpad, `Page Up` / `Page Down` | Scroll the transcript or focused activity feed |
| `Ctrl+T` | Focus/unfocus the activity timeline |
| `Ctrl+G` | Pause/resume voice input (capture may stay armed; listening stops) |
| `Ctrl+O` | Mute/unmute spoken responses (also stops current playback) |
| `Home` / `End` | Jump to the start/end of the focused feed (on Chat, only when the composer is empty) |
| `Ctrl+Home` / `Ctrl+End` | Always jump to the start/end of the focused feed |

Scrolling with the wheel requires Samantha to claim the mouse, which means the
terminal no longer handles click-drag selection itself. To select and copy
transcript text, hold **option** (iTerm2), **fn** (Terminal.app), or **shift**
(kitty, GNOME Terminal, Windows Terminal) while dragging. The claim is held
only while you are in the conversation — the launcher, settings, and meeting
screens keep unmodified selection. If you copy from the transcript often, set
`tui_mouse_enabled: false` to give unmodified selection back everywhere;
`Page Up` / `Page Down` still scroll.

`/vim` enables modal composer editing (`/vim off` disables it). The input label
and footer change with the active mode. In NORMAL mode, use `i`/`a`/`I`/`A` to
enter INSERT, `h`/`j`/`k`/`l` and `w`/`b` to move, `x`/`D`/`dd` to delete,
`o`/`O` to open lines, `u` to undo, and `Enter` to send. `Esc` returns from
INSERT to NORMAL.

Microphone and speaker devices can be selected from the **Input** and
**Output** sections in TUI Settings. The **TTS** section selects the active
text-to-speech provider and shows its configured model context. Kokoro exposes
the static voice picker. Selecting an uninstalled **Qwen3-TTS** row installs the
native multi-tier package (worker binary + GGUF under `models_dir/qwen3-tts`;
uses the published platform release by default). After setup, the **Voice** section
lists Qwen's nine CustomVoice-class preset speakers; press `p` to preview and
`Enter` to select one. The **Language** section selects Qwen's synthesis language
(use **Auto** unless a book or conversation needs an explicit language). Returning
from Settings replaces the provider used by subsequent utterances in an
already-running conversation; no Samantha restart is required. The launcher
and conversation header show the active TTS provider/model/mode/voice badge.
An empty device config value follows the current operating-system default.

The first Qwen installation is a large download. It is isolated below
`models_dir/qwen3-tts`, can be inspected with `samantha models status --tts`,
and can also be installed non-interactively after selecting Qwen with
`samantha models ensure --tts`. Samantha keeps the model loaded in one native
warm worker for the lifetime of the provider. Advanced users can set both
`qwen_tts_binary` and `qwen_tts_model` to point at an explicit worker/model dir.

### Personas (voice agents)

A persona is a complete voice agent: its system prompt, brain provider/model, and
TTS provider/voice all belong to the persona rather than to global config. TUI
**Settings** writes global defaults only; to change one agent, edit it under
**Personas** (or its `personas/<id>/persona.yaml`). Any field a persona leaves
empty inherits the app default — an empty `brain:` uses the app-level
`brain_provider`/model, and an empty `tts:` uses the app-level TTS keys.

Starting a new conversation opens a persona picker: the active persona is
pre-selected, and `+ Create persona…` clones the current global stack into a new
profile. A conversation binds its identity — name, prompt, brain, and voice — at
start, so editing a persona or switching the active one never affects a session
already in flight.

The persona system-prompt editor has a vim mode: `esc` for NORMAL; `i`/`a`/`o`
to insert; `h`/`j`/`k`/`l`/`0`/`$`/`w`/`b`/`gg`/`G` to move; `x`/`D`/`dd` to
delete; `:w` accepts the prompt and advances to Model & voice; `:wq` (or
normal-mode `enter`) saves the whole form; `:q` cancels. `ctrl+j`, `alt+s`,
and `f2` also save the form from any step.

### Turn recovery and token usage

A hard tool or brain failure always ends with a spoken recovery reply ("I hit an
error while working on that…") while the error detail goes to the activity feed;
the turn is reported as `completed (degraded)` rather than dropped silently.

For Ollama, Activity records per-request `prefill N tok · gen M tok`. Prefill
tracks the size of your new turn, not the whole transcript, and is bounded by
`ollama_num_ctx` (default `8192`); `ollama_keep_alive` (default `10m`) keeps the
model resident between turns.

### Batch narration (audiobooks)

`samantha render` turns documents into audio files and a manifest without the
live voice pipeline (no microphone). It reads text, Markdown, HTML, URL articles,
or EPUB, segments the text, synthesizes with the configured TTS, and always
writes WAV (the source of truth).

```bash
# Single file (format auto-detected from the extension; --stdin reads text):
samantha render article.md --out out/article.wav
cat notes.txt | samantha render --stdin --out out/notes.wav
samantha render https://example.com/post --out out/post.wav   # URL article

# Sectioned multi-file: one WAV per heading/section + a manifest.
# Works for Markdown, HTML, URL, and EPUB (EPUB requires --out-dir):
samantha render article.md --out-dir out/article
samantha render book.epub --out-dir out/book

# Optional compressed output via an external encoder (default ffmpeg); WAV is
# still written. A missing encoder fails before any synthesis:
samantha render book.epub --out-dir out/book --audio-format mp3

# Resume a long render: unchanged chapters/sections are skipped, changed/failed
# ones rebuild. --json prints completed/skipped/failed counts and exits non-zero
# if any unit failed, so scripts can branch:
samantha render book.epub --out-dir out/book --resume --json | jq '.failed'

# Optional planning controls (defaults preserve prior behavior):
samantha render article.md --out-dir out/article \
  --max-segment-chars 1200 --pause-heading 750ms --pause-paragraph 400ms \
  --code-blocks skip
```

### Audiobook creation

`samantha audiobook create` is a task-oriented wrapper over the same render
runtime for EPUB books and digital PDFs: one WAV per chapter (EPUB spine) or
page (PDF) plus a manifest under `--out-dir` (required). It accepts render's
pass-through flags (`--resume`, `--voice`, `--speed`, `--audio-format`,
`--language`, `--encoder`, `--json`, `--manifest`, `--overwrite`). Qwen accepts
`--voice` and `--language`; its pinned CustomVoice model does not support
`--speed`. Use `samantha render` for
markdown, HTML, URL (including sectioned `--out-dir`), and text sources.

Before synthesis, build a reviewable production plan. This writes the
extracted sections, a YAML source of truth, and a Markdown preview; it does
not load TTS or create audio:

```bash
samantha audiobook plan book.epub --out-dir out/book
samantha audiobook review out/book/production-plan.yaml
# Apply explicit human decisions when needed:
samantha audiobook review out/book/production-plan.yaml --exclude contents --exclude body --reason "navigation/front matter"
```

The plan classifies likely navigation, index, front matter, main content,
reference, and back matter. Ambiguous sections remain `review` until a human
decides. Rendering from the approved plan will be added in a follow-up slice;
the current `create` command remains the direct raw-spine compatibility path.

```bash
samantha audiobook create book.epub --out-dir out/book
samantha audiobook create book.epub --out-dir out/book --voice Ryan --language English
samantha audiobook create book.epub --out-dir out/book --audio-format m4b --resume --json
# From Calibre library (requires calibre_enabled=true and Calibre installed):
samantha config calibre_enabled true
samantha library list
samantha library search "cryptography"
samantha library show 42
samantha audiobook create --from-library "Crypto 101" --out-dir out/crypto
```

### Calibre library (optional)

[Calibre](https://calibre-ebook.com) is free software that organizes ebooks on
your computer (EPUB, PDF, MOBI, …). You do **not** need it for voice chat.
Samantha can browse a Calibre library so you can pick books for audiobooks or
ask what titles you own.

**Typical setup (most users):**

1. Install Calibre and open it once so it creates your library:
   - macOS: `brew install --cask calibre`
   - Arch Linux: `sudo pacman -S calibre`
   - Debian/Ubuntu: `sudo apt install calibre`
   - Windows or other platforms: use the installer from calibre-ebook.com
2. Enable the integration: `samantha config calibre_enabled true`
   (or open **Library** in the TUI and press **e**).
3. Samantha finds `calibredb` on `PATH`, or in the macOS app bundle
   (`/Applications/calibre.app/Contents/MacOS/`), or `/opt/calibre` on Linux.
   Your default Calibre library is used when `calibre_library_path` is empty.

**If something is non-default:**

| Situation | Config |
|-----------|--------|
| Library not at Calibre’s default path | `calibre_library_path` |
| `calibredb` not found automatically | `calibredb_binary` (full path) |
| Prefer PDF over EPUB when both exist | `calibre_prefer_format pdf` |

`samantha doctor` reports `calibre-binary` as a **Warn** when missing (never a
hard failure). Voice and other features keep working.

In the TUI, open **Library** from the launcher to browse the catalog, search,
and view book details. From a book press **enter** or **a** to send an
EPUB/PDF path into **Create audiobook**; MOBI/AZW-family books are converted
to a cached EPUB with Calibre's `ebook-convert`. The audiobook screen's **Pick
from library** opens with a browsable catalog, and `/` switches to search.
Direct audiobook rendering still consumes EPUB/PDF after any library
conversion.

### Narrate pipeline (prompt-controlled)

```bash
samantha narrate plan article.md --out narration.plan.yaml
samantha narrate prepare narration.plan.yaml --resume
samantha narrate render narration.plan.yaml --resume
samantha narrate plan book.pdf --out out/book.plan.yaml   # requires pdftotext (Poppler)
```

Digital PDFs also work with direct render / audiobook create:

```bash
samantha render book.pdf --out-dir out/book
samantha audiobook create book.pdf --out-dir out/book
```

### Meeting recording

`samantha meeting record` listens continuously (STT only — no Brain, no TTS)
and creates one timestamped `.meeting` bundle directory. Its canonical
`meeting.md` and internal structured event stream are each synced so a crash
never loses what was already captured.

From the main `samantha` launcher choose **Record meeting**, or run
`samantha meeting record` on a TTY (not `--json` / `--no-tui`) for the
full-screen recorder:

| Control | Action |
|---------|--------|
| Type + **Enter** | Save a note at the current timestamp |
| **Ctrl+B** | Mark this moment ★ important (optional caption from the note field) |
| **Ctrl+C** / **Ctrl+Q** | Stop recording |
| Spoken stop phrase | "stop recording" / "end meeting" / "stop listening" (exact utterance; not written to the meeting) |

Spoken stop phrases end the session like Ctrl+C and are **not** appended to the
meeting transcript. Meeting bundles and internal directories are created mode
`0700`; documents and machine data are `0600` (owner-only).

Meeting speaker diarization is **on by default** (**Settings → Meeting →
Speaker diarization**). The first meeting that needs models installs Samantha's
managed pyannote segmentation and NeMo TitaNet packs, then captures 16 kHz PCM
through a non-blocking subscriber while STT continues normally. When recording
stops, the recorder shows **Stopping** then **Diarizing** (the REC timer freezes
at stop) before opening a review screen with anonymous `speaker-1…N` labels
beside attributed turns.

**Expected speakers** (**Settings → Meeting → Expected speakers**, key
`speaker.meeting.num_speakers`): cycle **Auto** (0) or a fixed count (**2 / 3 / 4**).
Auto lets the clusterer choose how many voices to invent and can **over-split**
long 1:1 interviews; set **2** for two-person calls. These are voice clusters, not
enrolled names or identity claims. Each speaker label keeps a stable, distinct
color in attributed turns and on the review screen; live conversation bubbles
and the current-speaker footer use the same palette. Continue from the review
screen to the configured routing flow. Turn the feature off in Settings if you
do not want offline diarization.

During **Diarizing**, **Ctrl+C** again abandons speaker analysis and opens the
review screen with the transcript intact (analysis status cancelled). Native
model work may still finish in the background after abandon; the TUI does not
wait on it. Re-running diarization on a past meeting needs retained audio
(`speaker.meeting.record_audio` / **Record audio for analysis**); without it the
working PCM is discarded after Finalize.

### Speaker labels in chat

Meeting diarization is offline — it runs when a recording stops, so live meeting
lines carry no labels. Labeling **chat** turns is a separate, live path
(**Settings → Speakers → Speaker labels in chat**), also **on by default**. A
voice conversation prefixes each user bubble with `speaker-1…N` and colors the
bubble border to match, and the footer shows the current speaker.

The same tab tunes the two levers that decide label quality:

| Row | Key | Effect |
|---|---|---|
| Match threshold | `speaker.live.threshold` | Lower merges similar voices onto one label; higher splits a voice into a fresh `speaker-N` more readily |
| Analysis window | `speaker.live.window_ms` | Shorter labels a turn sooner; longer gives the embedder more voice to work with |

Labels are enrollment-free: the first sight of a voice registers `speaker-N` and
later turns re-match against it. Only the embedding model is needed, so chat
labels do not require the pyannote segmentation model that meetings use.

In an open conversation, `/speakers on|off|status` toggles labeling for that
session and loads the model on first use — no restart. Settings persists the
choice for future conversations. Live analysis needs microphone capture, so it
is unavailable in `--text` mode.

The meetings directory contains one visible item per recording:

```text
weekly-planning-20260722-090000.meeting/
  meeting.md
  audio.wav                 # optional
  .samantha/
    events.jsonl
    speaker-analysis.json
```

The attributed transcript is added to `meeting.md`, and routed meeting notes
prefer those attributed utterances. Enable **Record audio for analysis** only
when a private `audio.wav` is also desired. Model or analysis failures are
shown and logged without discarding or stopping the meeting transcript.

JSONL events include `offset_ms` from meeting start for alignment:

```json
{"type":"utterance","ts":"...","offset_ms":12340,"text":"next agenda item"}
{"type":"note","ts":"...","offset_ms":15000,"text":"follow up with finance"}
{"type":"bookmark","ts":"...","offset_ms":18200,"label":"important","text":"budget decision"}
{"type":"speaker_analysis","ts":"...","offset_ms":20000,"status":"complete","speaker_count":2,"artifact":"...speaker-analysis.json"}
{"type":"speaker_utterance","ts":"...","offset_ms":12340,"id":"utterance-1","text":"next agenda item","label":"speaker-1","start_ms":11000,"end_ms":12340,"state":"stable","timing":"estimated"}
```

```bash
samantha meeting record
samantha meeting record --description "Weekly planning sync"
samantha meeting record --description "Standup" --out-dir ~/notes/meetings --json
samantha meeting record --description "CI log" --no-tui
samantha meeting analyze meeting.wav --speakers 2
```

Bundles default to `~/.obey/agents/voice/festival-voice/meetings/<slug>-<timestamp>.meeting/`.
Meeting routing accepts a `.meeting` bundle or its canonical `meeting.md`.
`--json` also emits one JSON line per utterance plus a final summary on stdout.

### Campaign routing (camp CI0009)

When a meeting is routed to a **campaign** destination, Samantha defaults to
`capture: meeting` and runs:

```bash
camp idea notes import-meeting <bundle> --title "…" --summary-file … --json
```

inside that campaign’s root so the note lands under
`.campaign/intents/notes/meetings/` with a `.transcripts/` sidecar — **not** as
a lifecycle intent in Inbox/Ready/Active.

Legacy modes (opt-in in `meeting.route.destinations[].capture`):

| `capture` | Behavior |
|-----------|----------|
| `meeting` (default) | `import-meeting` → `notes/meetings/` |
| `intent` | `camp idea add` lifecycle intent (old, misfiling risk) |
| `note` | `camp idea add --note` |

Requires a camp build with CI0009 (`import-meeting`, #537/#539) on `PATH`.

## Configuration

Config lives at `~/.obey/agents/voice/festival-voice/config.yaml`. Values can also be overridden with environment variables where listed.

Persona **profiles** live under `~/.obey/agents/voice/festival-voice/personas/<id>/persona.yaml`.
On load, the active profile overlays `agent_name`, the persona prompt name, and per-persona TTS
(`tts.provider` + `tts.voice` → `tts_provider` and either `tts_voice` or `qwen_tts_voice`).
Each persona can use any supported TTS backend and any voice for that backend.
Prompt bodies stay in `prompts/` (see `samantha prompts`).

| Key | Default | Environment | Description |
|-----|---------|-------------|-------------|
| `active_persona` | `samantha` | `ACTIVE_PERSONA` | Persona profile id under `personas/<id>/` |
| `agent_name` | `Samantha` | | Display name (overlaid from active persona) |
| `persona` | `samantha` | `PERSONA` | Prompt document name for kind=persona (overlaid from active persona) |
| `compact_prompt` | *(embedded default)* | `COMPACT_PROMPT` | Prompt document name for kind=compact — the `/compact` summarize-turn instruction |
| `brain_provider` | `ollama` | `BRAIN_PROVIDER` | Brain backend: `ollama`, `claude`, or `grok` |
| `ollama_model` | empty | `OLLAMA_MODEL` | Ollama model name |
| `ollama_embedding_model` | `nomic-embed-text` | `OLLAMA_EMBEDDING_MODEL` | Ollama embedding model used to match each user prompt to relevant Agent Skills. Set empty to disable semantic routing and retain model-driven `read_skill` fallback. |
| `skills_similarity_threshold` | `0.55` | `SKILLS_SIMILARITY_THRESHOLD` | Minimum cosine similarity for automatic skill activation. Tune when using an embedding model with a different score distribution. |
| `ollama_host` | `http://localhost:11434` | `OLLAMA_HOST` | Ollama server URL |
| `ollama_num_ctx` | `8192` | `OLLAMA_NUM_CTX` | Context window requested on every chat call. `0` uses the server model default, which silently truncates long prompts from the top (system prompt first). |
| `ollama_keep_alive` | `10m` | `OLLAMA_KEEP_ALIVE` | How long Ollama keeps the model resident between turns (Go duration; empty = server default). |
| `claude_max_session_tokens` | `0` | `CLAUDE_MAX_SESSION_TOKENS` | Opt-in fuse: caps the prompt the `claude` CLI replays on `--resume`. Past it the brain starts a fresh CLI session from recent history. `0` (default) trusts the CLI session and resumes forever — recommended for interactive use; set a cap for unattended/serve setups. |
| `claude_session_warn_tokens` | `60000` | `CLAUDE_SESSION_WARN_TOKENS` | Replayed-prompt size at which a visible warning appears (log + activity feed), once per CLI session. Nothing is dropped. `0` disables the warning. |

> **Upgrading:** installs that saved settings while `claude_max_session_tokens` defaulted to `60000` still carry that value in `config.yaml` and keep the silent session drops. Delete the key (or set `0`) to adopt the warn-only default — `samantha doctor` flags this.
| `voice_tools_enabled` | `true` | `VOICE_TOOLS_ENABLED` | Local tools (`list_files` / `read_file` / `write_file` / `run_command` / `web_search` / `fetch_url` for Ollama). Default on; set `false` to disable. Switching brain provider to Ollama also re-enables when the key was dumped false. Remote `samantha serve` still uses `remote_tools_enabled` (default off). |
| `tool_command_timeout` | `30` (clamped 1–120) | `TOOL_COMMAND_TIMEOUT` | Maximum seconds for one local `run_command` invocation. The whole brain turn has its own timeout. |
| `remote_tools_enabled` | `false` | | Allow network-triggered turns from `samantha serve` to invoke tools; keep off unless remote clients are trusted. |
| `calibre_enabled` | `false` | `CALIBRE_ENABLED` | Opt in to Calibre library browse/search, TUI Library + picker, and `--from-library` |
| `calibre_library_path` | empty | `CALIBRE_LIBRARY_PATH` | Calibre library path (empty uses Calibre's default library) |
| `calibredb_binary` | empty | `CALIBREDB_BINARY` | `calibredb` path; empty uses PATH then macOS app bundle / `/opt/calibre` |
| `calibre_convert_binary` | empty | `CALIBRE_CONVERT_BINARY` | `ebook-convert` path used to convert MOBI/AZW-family books to EPUB |
| `calibre_prefer_format` | `epub` | `CALIBRE_PREFER_FORMAT` | Preferred book format when resolving (`epub`, `pdf`, then convertible MOBI/AZW-family formats) |
| `tts_provider` | `kokoro` | `TTS_PROVIDER` | TTS backend |
| `voice_fallback_provider` | `kokoro` | `VOICE_FALLBACK_PROVIDER` | One-sentence runtime fallback after the selected provider fails; set empty/disabled to turn it off |
| `tts_voice` | `af_heart` | `TTS_VOICE` | Kokoro voice name |
| `speech_speed` | `0.95` | | Playback speed |
| `qwen_tts_binary` | empty | `QWEN_TTS_BINARY` | Empty uses the native package worker under `models_dir/qwen3-tts`; set with `qwen_tts_model` for an explicit external worker |
| `qwen_tts_model` | empty | `QWEN_TTS_MODEL` | Empty uses the native package model dir; otherwise an external worker model directory |
| `qwen_tts_model_tier` | `0.6b` | `QWEN_TTS_MODEL_TIER` | Preferred native tier (`0.6b` or `1.7b` when present) |
| `qwen_tts_native_url` | platform release | `QWEN_TTS_NATIVE_URL` / `SAMANTHA_QWEN_NATIVE_URL` | Empty uses the published `qwen3-tts-native` release for this OS/arch; override for custom builds |
| `qwen_tts_native_sha256` | platform release | `QWEN_TTS_NATIVE_SHA256` / `SAMANTHA_QWEN_NATIVE_SHA256` | Archive digest matching the default or configured URL |
| `qwen_tts_timeout` | `120` | `QWEN_TTS_TIMEOUT` | Per-request native/external worker timeout in seconds |
| `qwen_tts_mode` | empty | `QWEN_TTS_MODE` | Product setup resolves empty to `customvoice` |
| `qwen_tts_voice` | empty | `QWEN_TTS_VOICE` | CustomVoice-class speaker; setup resolves empty to `Vivian` |
| `qwen_tts_language` | empty | `QWEN_TTS_LANGUAGE` | Synthesis language; setup resolves empty to `Auto` |
| `qwen_tts_instruction` | empty | `QWEN_TTS_INSTRUCTION` | Reserved for an installable instruction-capable Qwen model tier |
| `qwen_tts_reference_audio` | empty | `QWEN_TTS_REFERENCE_AUDIO` | Authorized local reference WAV for an approved clone workflow |
| `qwen_tts_reference_text` | empty | `QWEN_TTS_REFERENCE_TEXT` | Transcript required by the approved clone workflow |
| `qwen_tts_consent` | `false` | `QWEN_TTS_CONSENT` | Explicit consent/authorization gate for reference voice use |
| `output_device` | empty | `OUTPUT_DEVICE` | Playback device name; empty follows the system default |
| `stt_provider` | `sherpa` | `STT_PROVIDER` | STT backend: `sherpa`, `sherpa-streaming`, `sherpa-offline`, or `whispercpp` |
| `input_device` | empty | `INPUT_DEVICE` | Capture device name; empty follows the system default |
| `stt_mode` | empty | `STT_MODE` | STT mode for the preferred provider+mode schema: `offline` or `streaming` for `sherpa`, `cli` for `whispercpp` |
| `sherpa_streaming_model` | `en-2023-06-26` | `SHERPA_STREAMING_MODEL` | sherpa-onnx streaming model |
| `whisper_model` | `small` | `WHISPER_MODEL` | sherpa-onnx Whisper model size |
| `whisper_quantized` | `true` | | Prefer quantized Whisper models |
| `whispercpp_binary` | `whisper-cli` | `WHISPERCPP_BINARY` | whisper.cpp CLI executable |
| `whispercpp_model` | `base.en` | `WHISPERCPP_MODEL` | Downloadable whisper.cpp model name |
| `whispercpp_model_path` | `~/.cache/festival-voice/models/whispercpp/ggml-base.en.bin` | `WHISPERCPP_MODEL_PATH` | whisper.cpp model path |
| `vad_enabled` | `true` | | Enable voice activity detection |
| `vad_silence_duration` | `0.8` | | Seconds of silence before ending speech (raise to stop being cut off) |
| `vad_threshold` | `0.6` | `VAD_THRESHOLD` | Speech-detection confidence (raise to ignore background noise) |
| `vad_min_speech_duration` | `0.25` | `VAD_MIN_SPEECH_DURATION` | Minimum speech length in seconds (raise to ignore brief noises) |
| `voice_frontend_enabled` | `false` | `VOICE_FRONTEND_ENABLED` | Local AEC/NS/AGC on mic input (off by default: the noise suppressor currently over-suppresses normal-volume speech; enable only with barge-in) |
| `tui_mouse_enabled` | `true` | `TUI_MOUSE_ENABLED` | Let the wheel/trackpad scroll the transcript. Claiming the mouse means click-drag selection needs a modifier (option/fn/shift); set to `false` for unmodified selection. Read at startup — restart Samantha to apply |
| `agent_name` | `Samantha` | | Display name |
| `persona` | `samantha` | `PERSONA` | Prompt document name for the interactive persona |
| `prompts_dir` | empty | `PROMPTS_DIR` | Prompt document directory; defaults to `~/.obey/agents/voice/festival-voice/prompts` when unset |
| `skills_enabled` | `true` | `SKILLS_ENABLED` | Agent Skills (`SKILL.md`) discovery for Ollama. Default on; set `false` to disable. Discovers project/workspace/user skill frontmatter into the system prompt; not a pre-activation sandbox. Claude/Grok already discover skills via their CLIs. |
| `skills_dir` | empty | `SKILLS_DIR` | Extra Samantha skills root (after project/workspace/user harness dirs); defaults to `~/.obey/agents/voice/festival-voice/skills` when unset. |
| `models_dir` | `~/.cache/festival-voice/models` | `MODELS_DIR` | Model download directory |
| `language` | `en-US` | | Recognition language |
| `max_history` | `10` | | Saved conversation history length |
| `listen_timeout` | `10` | | Listen timeout in seconds |
| `phrase_time_limit` | `30` | | Maximum phrase length in seconds |

### Agent Skills (Ollama)

With `skills_enabled=true` (the product default unless explicitly disabled), the
Ollama provider discovers Agent Skills via the
cross-client **`.agents/skills`** convention
([agentskills.io](https://agentskills.io/client-implementation/adding-skills-support))
— **project/workspace then user** — plus Samantha's own skills root:

1. `<cwd>/.agents/skills/*/SKILL.md` — project skills (shared with Codex, VS Code, camp, …)
2. `<nearest ancestor>/.agents/skills/*/SKILL.md` — workspace/project-root skills
3. `~/.agents/skills/*/SKILL.md` — user skills
4. `skills_dir` (default `~/.obey/agents/voice/festival-voice/skills`) — Samantha-only

Ollama does **not** scan `.claude/skills`; the Claude Code provider owns that
path, and dual-scanning would duplicate skills when both trees are projected.
Duplicate skill names resolve with **project first**. Ollama advertises each
skill's name and description in the system prompt and offers a `read_skill` tool
to load full instructions on demand (progressive disclosure).

Samantha also performs harness-side semantic activation. At startup it batches
each skill's name and description through `ollama_embedding_model` and caches
the vectors. Before every turn it embeds the user prompt, selects the closest
skills above the relevance threshold, and injects only those full `SKILL.md`
bodies into an `<activated_skills>` block. This keeps Tier 1 discovery compact
while making Tier 2 activation reliable for local models. Pull the default
embedding model once with `ollama pull nomic-embed-text`. If the embedding model
is unavailable, Samantha logs a warning and falls back to the catalog plus
model-driven or explicit `read_skill`; the conversation still runs.

Optional frontmatter `allowed-tools` (Agent Skills experimental field) is
retained as catalog metadata but does not restrict Samantha's Ollama runtime.
Skills add instructions rather than capabilities, all implemented tools remain
available after a skill is loaded, and the model may load multiple skills in one
turn. The global safety gates still apply: `voice_tools_enabled` /
`remote_tools_enabled` must allow tools before any tool call can run.

Claude and Grok pick up skills via their own CLIs. Remote `samantha serve` still
gates all tools (including `read_skill`) behind `remote_tools_enabled`.

The TUI Settings screen exposes local tools under **Tools**, and Agent Skills
when the brain provider is Ollama. Changes are persisted immediately and take
effect when the conversation runtime is re-entered or restarted.

```text
# project (cwd where samantha was started)
./.agents/skills/hello/SKILL.md

# workspace/project root (nearest ancestor)
<workspace>/.agents/skills/campaign-context/SKILL.md

# user
~/.agents/skills/hello/SKILL.md

# Samantha config root
~/.obey/agents/voice/festival-voice/skills/hello/SKILL.md
```

```markdown
---
name: hello
description: Greet the user warmly.
---

# Hello skill
Say hello to the user.
```

The preferred STT schema is `stt_provider` + `stt_mode` (e.g. `stt_provider: sherpa` with `stt_mode: streaming`). The legacy compound aliases (`sherpa-streaming`, `sherpa-offline`) still work with `stt_mode` unset and are never rewritten; combining a compound alias with a conflicting `stt_mode` is a config error.

Use `samantha config migrate --dry-run` to preview the explicit
`stt_provider`/`stt_mode` values that would preserve the current STT behavior.
Dry runs report the config path and proposed values without writing files. Use
`samantha config migrate --write` to apply the migration; it creates a
timestamped `.bak` file before replacing an existing config. The write path
updates YAML via `yaml.v3`, so comments and unrelated keys are preserved where
possible, but scalar formatting around touched keys may be normalized.

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
just test audio-crackle        # Playback layout + crackle software regressions (CI-safe)
just test audio-hardware       # Opt-in: real speakers, Studio Display etc.
go test ./...                  # Plain Go test fallback
```

Integration tests expect `bin/linux/samantha` to exist. The build dashboard creates it for the integration workflow.

Playback crackle (Studio Display mono-client class) is guarded by `internal/audio`
layout + crackle tests in normal `go test -race ./...`. After any change under
`internal/audio`, also run `just test audio-hardware` on an affected machine and
confirm `--debug-audio` metadata reports `channels: 2` (not mono).

#### Voice smoke tests (opt-in, require local models)

The STT provider loops (`internal/stt`) are covered by deterministic unit tests
that use fakes, so they run without model files. Real end-to-end voice behavior
depends on local STT/VAD/TTS models and is therefore opt-in. When the models are
installed (`samantha models ensure`, once available), run the smoke plan:

| Scenario | Expectation |
|----------|-------------|
| Short utterance (`hello samantha`) | final transcript within ~2s; finalizes on the source's EOF/silence, not a phrase timeout |
| Long utterance | partial/final transcript; caps at the max-utterance length |
| Silence only | times out with no final transcript |
| Finite fixture EOF | terminates promptly on the explicit final frame, no hang |

```bash
# Deterministic, no models needed:
go test ./internal/stt ./internal/endpoint ./internal/audio

# Real-provider smoke (needs models + whisper.cpp binary for that provider):
go test -tags integration ./tests/voiceflow      # fixture-driven pipeline flow
just qwen-live                                   # real native Qwen voices + cancel/restart WAVs
samantha listen                                  # manual: speak a short command
```

#### Latency benchmarks (protect the sub-2s goal)

The `samantha benchmark` command measures the perceived-latency milestones that
protect the <2s end-to-end goal and emits them as both a summary and (with
`--json`) a stable `TurnMetrics` record per turn: STT final, first model chunk,
first segment, first audio ready, playback start, playback complete, and — on a
barged-in turn — interruption latency. Threshold flags fail the run when a
milestone regresses, so the benchmark can gate CI or a local check:

```bash
# Prompt latency with budgets (any breach exits non-zero):
samantha benchmark --prompt "hello" \
  --max-total 2s --max-first-model-chunk 500ms --max-playback-start 800ms

# STT fixture latency + transcript accuracy:
samantha benchmark --mode stt --max-stt-final 2s --min-transcript-score 0.8

# Machine-readable output for tracking regressions over time:
samantha benchmark --prompt "hello" --json bench.json
```

Interruption latency is reported only when a turn is interrupted; all milestones
are always present in the `--json` output.

### Model Assets And Readiness

Local model assets are described by an asset manifest and managed by three
commands:

```bash
samantha models status        # read-only: which assets are installed vs missing
samantha models status --json # machine-readable for scripts
samantha models ensure        # download any missing assets (atomic + verified)
samantha doctor               # diagnose config, assets, and external binaries
```

`models status` and `doctor` are read-only and safe offline. Doctor validates
the selected Claude/Grok CLI or Ollama configuration without making a network
request, and exits non-zero on setup errors (a missing model asset remains a
warning that points you to `models ensure`). Downloads are reliable by
construction: each file is written to
a temp file, size/checksum-verified when known, and atomically renamed; archives
are extracted into a temp directory, verified, then promoted — so an interrupted
or corrupt download never lands a partial asset, and **re-running
`models ensure` cleanly recovers**.

Automated tests cover download/extraction reliability with fake HTTP servers (no
network). To verify the **real** assets manually:

```bash
samantha models status        # confirm what's missing
samantha models ensure        # download from the real release URLs
samantha doctor               # confirm everything reports OK
```

### Voice Utilities

```bash
just voice test
just voice voices
just voice providers
```

## Documentation

| Doc | Topic |
|-----|--------|
| [docs/serve-protocol.md](docs/serve-protocol.md) | Remote serve HTTPS + WebSocket contract for clients |
| [docs/qwen3-tts-spike.md](docs/qwen3-tts-spike.md) | Native Qwen3-TTS product path (worker + GGUF) |
| [docs/aec-probe.md](docs/aec-probe.md) | AEC / voice-frontend probe notes |
| [docs/adr/](docs/adr/) | Architecture decision records |

## License

Samantha is released under the Apache License 2.0. Copyright 2026 Obedience Corp. See [LICENSE](LICENSE) and [NOTICE](NOTICE).

---

Built by [Obedience Corp](https://obediencecorp.com) · [GitHub](https://github.com/Obedience-Corp)
