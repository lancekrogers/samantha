# Meetings and speaker labels

[Back to README](../README.md)

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

Meeting speaker analysis has two layers (both **on by default**):

1. **Live provisional labels while recording** (**Settings → Meeting → Live
   labels while recording**, key `speaker.meeting.live`) — the same embedding
   path as chat. Finalized STT lines get a colored `speaker-N` glyph (or 🎤
   when unknown). Labels can revise on the latest line as the engine catches
   up; brief empty gaps hold the last good label for a few seconds. A footer
   line marks these as **provisional**.
2. **Offline diarization on stop** (**Settings → Meeting → Speaker
   diarization**) — the review-screen source of truth. Stopping opens review
   immediately (the REC timer freezes at stop) and diarization continues **in
   the background** — in the launcher and in the standalone
   `samantha meeting record` TUI alike; the attributed `speaker-1…N` turns
   fold into the review when it finishes, or a status line reports the
   failure.

Live labels and offline results may disagree; that is expected. The live
scrollback is left as-shown; review uses the offline timeline. The first
session that needs models installs Samantha's managed packs (embedding for
live; pyannote segmentation + NeMo TitaNet for offline), then captures 16 kHz
PCM through a non-blocking subscriber so STT is never blocked.

**Expected speakers** (**Settings → Meeting → Expected speakers**, key
`speaker.meeting.num_speakers`) applies to **offline clustering only**: cycle
**Auto** (0) or a fixed count (**2 / 3 / 4**). Auto can **over-split** long
1:1 interviews; set **2** for two-person calls. These are voice clusters, not
enrolled names or identity claims. Each label keeps a stable color on review
and on live chat/meeting rows (shared palette). Continue from the review
screen to the configured routing flow. Turn either layer off in Settings if
you do not want it.

Background diarization never holds the transcript hostage: in the launcher,
quitting Samantha cancels the enrichment only; in the CLI, leaving review
while analysis is still running prints `Diarizing speakers…` and a further
**Ctrl+C** skips the labels (the transcript and any delivered route are
already on disk). `--json` waits for the labels so the summary keeps its
speaker fields — Ctrl+C skips there too. Re-running diarization on a past
meeting needs retained audio (`speaker.meeting.record_audio` / **Record audio
for analysis**); without it the working PCM is discarded after Finalize.

A campaign/file destination chosen at meeting start is **durable**: the plan
is written into the bundle and the route fires as soon as capture stops —
review does not gate it, and Ctrl+C on the review screen continues instead of
quitting. If the route cannot complete (quit, crash, or failure), the outcome
is recorded on the bundle (`routed` / `route_failed`) and the next launcher
start retries undelivered plans automatically (bounded attempts);
`samantha meeting route <bundle> --to <dest>` remains the manual recovery.
The routed note carries the transcript as it stood at capture end; speaker
labels that finish later live in the local bundle.

## Speaker labels in chat

Labeling **chat** turns is the same live embedding path (**Settings →
Speakers → Speaker labels in chat**), also **on by default**. A voice
conversation prefixes each user bubble with `speaker-1…N` and colors the
bubble border to match, and the footer shows the current speaker. Chat does
not run offline diarization.

The Speakers tab tunes the two levers that decide live label quality (chat
and meeting live share these):

| Row | Key | Effect |
|---|---|---|
| Match threshold | `speaker.live.threshold` | Lower merges similar voices onto one label; higher splits a voice into a fresh `speaker-N` more readily |
| Analysis window | `speaker.live.window_ms` | Shorter labels a turn sooner; longer gives the embedder more voice to work with |

Labels are enrollment-free: the first sight of a voice registers `speaker-N` and
later turns re-match against it. Live analysis only needs the embedding model;
offline meeting diarization additionally needs the pyannote segmentation pack.

In an open conversation, `/speakers on|off|status` toggles labeling for that
session and loads the model on first use — no restart. Settings persists the
choice for future conversations. Live analysis needs microphone capture, so it
is unavailable in `--text` mode.

When labels are active, the **model** also sees speaker attribution on user
turns (`speaker-1: …` in the prompt, or a human name after rename). Rename with:

```text
/speakers name 1 Lance
/speakers name speaker-2 Alex
/speakers names
```

Renames are session-local: they update bubbles, the footer, and subsequent
prompt attribution. They do not change the embedding manager’s stable
`speaker-N` ids. The model cannot rename speakers; only this command does.

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

## Campaign routing (camp CI0009)

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
