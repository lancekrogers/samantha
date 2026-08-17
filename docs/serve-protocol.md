# Samantha serve protocol

Wire contract for remote clients (embedded voice page, `samantha connect`,
future Swift iPad/iPhone apps). Host implementation: `internal/netapi`.

## Roles

| Surface | Role |
|---------|------|
| `samantha` (TUI) | **Local** full voice agent, plus a launcher entry that manages the Tailscale remote daemon |
| `samantha serve` | **Remote** daemon — no host mic loop; clients drive turns |
| Termius → TUI | Remote *control* only; audio still on the Mac |
| Browser / native app | Remote *voice* client over HTTPS + WebSocket |

## Base URL

```text
https://<host>:<port>/          # embedded voice page (public HTML/JS)
https://<host>:<port>/v1/...    # API (authenticated unless noted)
```

Default port: `7262`.

### Tailscale one-shot

Interactive: launch `samantha`, then choose **Use on another device**. The
TUI displays the MagicDNS URL and pairing code and stops the child server when
the user leaves the screen. This is remote voice for **any** tailnet client
(phone, tablet, laptop browser, `samantha connect`) — not iPad-only. SSH/Termius
into the host is a separate path (local TUI audio still plays on the Mac).

Headless/CLI:

```bash
samantha serve --tailscale
```

Binds the node’s Tailscale IPv4, prefers a cert via `tailscale cert` under
`~/.obey/agents/voice/festival-voice/serve/tls/`, mutes the host speaker by default,
and prints the MagicDNS URL (e.g. `https://mac.tailnet.ts.net:7262/`).

If `tailscale cert` fails (or LAN self-signed is used), serve stays up in
**limited client access** mode and prints stable product labels the TUI parses:

| Label | Meaning |
|-------|---------|
| `Network: tailscale` / `Network: lan` | How clients should reach the host |
| `Client access: full` | Trusted cert — mic works in any browser |
| `Client access: limited` | Warning path — desktop OK; some mobile browsers block mic |
| `Client setup: https://login.tailscale.com/admin/dns` | Free HTTPS Certificates toggle (Tailscale) |

Constants live in `internal/netapi/clientmode.go` and are shared by serve + TUI
so the scrape contract cannot drift.

Self-signed leaves are minted/rewritten with MagicDNS and/or the bind IP as
SANs so the printed URL passes hostname checks on LAN and Tailscale. Primary
UX is “any device on this network,” not a single OS.

Requires for `--tailscale`: Tailscale CLI logged in and MagicDNS on.

`--tailscale` also binds `127.0.0.1` alongside the tailnet address, so a
same-machine client (the Mac app, `samantha connect`) keeps a route to the
agent while the tailnet is exposed. The tailnet address stays primary — it is
what `url`, mDNS, and the QR pairing payload advertise.

### Ready banner (`--banner-json`)

With `--banner-json`, stdout carries one JSON object per line and all human
output moves to stderr. The first line is `ready`, written once the listener is
bound:

```json
{"event":"ready","protocol_version":3,"url":"https://mac.tailnet.ts.net:7262","port":7262,"fingerprint":"9f3c…","token":"…","mdns":false,"tailscale":true,"pid":41233,"binds":["100.64.0.7:7262","127.0.0.1:7262"],"client_setup_url":"https://login.tailscale.com/admin/dns"}
```

| Field | Meaning |
|-------|---------|
| `url` | What **remote** clients should open (MagicDNS host in tailscale mode) |
| `port` | Listening port |
| `fingerprint` | SHA-256 of the leaf cert DER, hex — pin this |
| `token` | Bearer token for `/v1/*` |
| `mdns` / `tailscale` | Discovery + network mode |
| `pid` | Serve process id |
| `binds` | Every bound `host:port`, **primary first**. Always present. Dial the loopback entry when one exists rather than resolving `url` |
| `client_setup_url` | Present **only** in limited client access. Its presence means limited; its absence means full |

LAN mode with a trusted or self-signed cert omits `client_setup_url` entirely:

```json
{"event":"ready","protocol_version":3,"url":"https://192.168.1.24:7262","port":7262,"fingerprint":"9f3c…","token":"…","mdns":true,"tailscale":false,"pid":41233,"binds":["192.168.1.24:7262","127.0.0.1:7262"]}
```

A `pairing_code` line (`{"event":"pairing_code","code":"…","expires_at":"…"}`)
follows whenever serve mints a code.

## Auth

| Mechanism | Where |
|-----------|--------|
| `Authorization: Bearer <token>` | All `/v1/*` except pair + static page |
| `?token=` query | **Only** `GET /v1/stream` (browser WebSocket cannot set headers) |
| Pairing code | `POST /v1/pair` (public, rate-limited) |

Primary token file: `~/.obey/agents/voice/festival-voice/serve/token` (0600).  
Per-device tokens (D2): `…/serve/tokens/<id>.json` (0600 each).  
Revoke all: `samantha serve --revoke-tokens` (primary + all devices).  
Revoke one device: `DELETE /v1/devices/{id}`.

### Pairing

1. Serve prints a 6-digit code (single-use, ~10 minutes).
2. Client:

```http
POST /v1/pair
Content-Type: application/json

{"code":"482193","device_name":"Lance’s iPhone"}
```

`device_name` is optional. When present, serve mints a **per-device** token
(PROTOCOL_DELTAS D2). When omitted, the **primary** shared token is returned
(back-compat for older clients / Mac supervisor).

3. Response (device pair):

```json
{"token":"<hex>","fingerprint":"<sha256 of leaf cert DER>",
 "device_id":"<id>","device_name":"Lance’s iPhone"}
```

Response (legacy / no device_name):

```json
{"token":"<hex>","fingerprint":"<sha256 of leaf cert DER>"}
```

Store token (Keychain / localStorage). Pin `fingerprint` for TOFU if desired.

### Devices (D2)

```http
GET /v1/devices
Authorization: Bearer <any-valid-token>
```

```json
{"devices":[
  {"id":"…","device_name":"Lance’s iPhone",
   "created_at":"…","last_seen":"…"}
]}
```

```http
DELETE /v1/devices/{id}
Authorization: Bearer <any-valid-token>
```

```json
{"deleted":"<id>"}
```

Deleting a device invalidates that bearer only and closes its WebSocket
streams. Other devices and the primary token remain active.

### Sessions

```http
DELETE /v1/sessions/{id}
Authorization: Bearer <any-valid-token>
```

```json
{"deleted":"<id>"}
```

Registered only when serve was built with a `DeleteSession` implementation
(the same gate `/v1/personas` and `/v1/meeting/*` use) — an older or limited
serve answers `404` rather than pretending. Errors:

| Status | Body | When |
|--------|------|------|
| `400` | `{"error":"missing session id"}` | empty `{id}` |
| `404` | `{"error":"…"}` | no session with that id (or the route is unregistered) |
| `409` | `{"error":"session is active"}` | `id` is the session the pipeline is currently writing into |

The **409** case is the live-session guard: deleting the file a running serve
rewrites on every turn would just make it reappear, so the route refuses
instead. The CLI (`samantha sessions rm`) has no way to see another process's
live session id, so it never tries to guess — this route is the one place
that can and does refuse.

## REST

| Method | Path | Auth | Purpose |
|--------|------|------|---------|
| `GET` | `/v1/status` | yes | `turn_active`, `providers`, `uptime_seconds`, `fingerprint` |
| `GET` | `/v1/sessions` | yes | Session summaries |
| `GET` | `/v1/personas` | yes | Persona list for `set_persona` (see below) |
| `POST` | `/v1/sessions/{id}/resume` | yes | Load history into the live pipeline |
| `DELETE` | `/v1/sessions/{id}` | yes | Delete a saved conversation; `409` if it is the live session |
| `POST` | `/v1/pair` | no | Exchange pairing code for token (optional `device_name`) |
| `GET` | `/v1/devices` | yes | List paired devices (D2) |
| `DELETE` | `/v1/devices/{id}` | yes | Revoke one device token + streams (D2) |
| `POST` | `/v1/intent` | yes | Capture intent (D3; file sink) |
| `GET` | `/v1/intent/targets` | yes | Intent routing targets (D3) |

## Meetings

> **Capability gating.** The `/v1/meeting` surface exists only when serve is
> built with meeting capture; otherwise every `/v1/meeting*` path is a 404 and
> `GET /v1/status` reports `meetings: false`. Capture (D6) requires
> `meetings == true` and `protocol_version >= 2`; **history** (D7 — `GET
> /v1/meetings`, bundle-id resolution, the `note` control action, `route_plan`
> at start) requires `protocol_version >= 3`. Read the version rather than
> probing: an older serve degrades cleanly instead of 404-guessing.

Phone-first meeting capture. The phone records with its own mic and ships
audio to the Mac in **sequenced 5-second segments** of `pcm_s16le` 16 kHz mono
(~160 KB each); STT, diarization, and summary all run post-stop on the Mac.
Nothing here is real-time, so reliability beats latency.

This surface is deliberately separate from `/v1/stream`: meeting capture never
claims the exclusive remote mic, never enqueues Dispatcher turns, and never
touches the audio queues.

| Method | Path | Auth | Purpose |
|--------|------|------|---------|
| `POST` | `/v1/meeting/start` | yes | Create the `.meeting` bundle; `409` if one is already recording |
| `PUT` | `/v1/meeting/{id}/segments/{seq}` | yes | Raw PCM body; `204` = persisted (the ack) |
| `POST` | `/v1/meeting/{id}/control` | yes | Append a control event to the bundle |
| `POST` | `/v1/meeting/{id}/stop` | yes | Verify contiguity, finalize audio, start the pipeline |
| `GET` | `/v1/meeting/{id}` | yes | Poll state / results |
| `POST` | `/v1/meeting/{id}/route` | yes | Route the finished note via `camp idea notes import-meeting` |
| `GET` | `/v1/meeting/{id}/document` | yes | The finished `meeting.md` |
| `GET` | `/v1/meetings` | yes | Meeting history from disk (D7) |

```http
POST /v1/meeting/start
{"title":"Standup","campaign":"mytools","source":"watch",
 "route_plan":{"destination_id":"camp:mytools","body":"full"}}
```

```json
{"meeting_id":"…","segment_seconds":5,"outbox_cap_segments":120}
```

`source` names the capture surface — `ios` (default when absent), `mac`, or
`watch` — and is recorded in the bundle's `session_start` event and as a
`# Source:` header in `meeting.md`, so diarized exports say where the mic
was. An unknown value is a `400`.

`route_plan` (optional) chooses where the notes get filed **before** the
recording starts. It is written to the bundle as a `route_plan` event
immediately after creation — before the first segment — so a meeting that dies
mid-capture still leaves a durable filing intent that `samantha meeting sweep`
delivers later.

- `destination_id` — a configured `meeting.route.destinations[]` id or
  `camp:<campaign>`; the same vocabulary as `meeting route --to`. Required
  when `route_plan` is present; empty is `400`
  `{"error":"meeting: route_plan requires destination_id"}`.
- `body` — `notes`, `full`, or absent for the configured `meeting.route.body`.
  Anything else is `400`
  `{"error":"meeting: route_plan body must be notes or full"}`. Both scopes
  embed the full transcript.

Whether the destination *exists* is not checked at start: discovery costs a
subprocess on the request path, and an unresolvable id must fail loudly at
delivery (a durable `route_failed` event the sweep retries) rather than block a
recording. Delivery runs once when the meeting reaches `ready`, through the
same single-flight gate as `POST /v1/meeting/{id}/route`, so a plan and a
manual route of the same meeting can never both file it. `campaign` still
names the phone's post-stop route target; when both are present `route_plan`
owns the durable plan.

Segment uploads are **idempotent per `(meeting_id, seq)`** and tolerate
out-of-order arrival, so a client may retry freely. `seq` is monotonic from 0.
Segment uploads also have their own rate budget, separate from the general
30-requests-per-10-seconds guard, so a client draining a buffered outbox after
a reconnect is not throttled.

```http
PUT /v1/meeting/{id}/segments/{seq}
Content-Type: application/octet-stream
<raw pcm_s16le, 16 kHz, mono>
→ 204 No Content
```

Audio that never arrives is replaced at finalize time with silence of the
nominal segment length, so a dropout never slides later audio earlier — client
bookmark and idea-span offsets stay aligned with the recording. Each run of
lost segments is recorded as a `segment_gap` event at its real offset.

```http
POST /v1/meeting/{id}/control
{"action":"bookmark","offset_ms":91500,"text":"decision"}
```

Each control becomes an event in the bundle's `.samantha/events.jsonl` using
the same schema a desktop recording writes.

| `action` | `text` | `label` | Bundle event |
|---|---|---|---|
| `pause` | ignored | ignored | `pause` |
| `resume` | ignored | ignored | `resume` |
| `bookmark` | optional caption | optional (`important` when absent) | `bookmark`, counted in `Summary.bookmarks` |
| `note` | **required** | ignored | `note`, counted in `Summary.notes` |
| `idea_start` | optional | span id | `idea_start` |
| `idea_end` | optional | span id | `idea_end` |

```http
POST /v1/meeting/{id}/control
{"action":"note","offset_ms":184300,"text":"decide the pricing tier next week"}
```

A `note` is a timestamped line in `meeting.md` (`[HH:MM:SS] 📝 note: …`),
identical to one typed in the TUI recorder. Empty or whitespace-only `text` is
a `400` `{"error":"meeting: note requires text"}` — an empty note would bump
the counter and leave a marker with nothing in it.

```http
POST /v1/meeting/{id}/stop
{"last_seq":417}
```

```json
{"state":"processing","missing_seqs":[],"missing_count":0}
```

A non-empty `missing_seqs` means the server is still short of audio: the
client re-pushes those segments and calls stop again. The list is truncated
for very gappy meetings; `missing_count` is always the true total.

`last_seq` is a floor, not a truth: serve raises it to the highest sequence it
actually received, so an under-reported value can never cause delivered audio
to be dropped. Sequence numbers above `100000` are rejected with `400`.

```http
GET /v1/meeting/{id}
```

```json
{"state":"recording|processing|ready|failed|interrupted",
 "step":"transcribing|filing ideas|diarizing",
 "missing_seqs":[],"result":{…}}
```

`step` names the pipeline stage and is present only while `state` is
`processing`, so a client can show what the Mac is doing instead of an
anonymous spinner. The `diarizing` step appears only when speaker analysis
is enabled. The set of step names is informational, not contractual —
render the string, don't switch on it.

```http
GET /v1/meeting/{id}/document
```

Returns the finished meeting's canonical `meeting.md` as `text/markdown` —
including speaker-labeled sections when diarization ran. `409` until the
meeting has notes (ready, or interrupted after janitor processing). The
document is the single source for rendered results; clients render it
rather than reconstructing notes from events.

**Failure semantics.** A phone that loses the network keeps recording into its
own outbox and resumes from the first unacked seq. If serve sees no segment or
control for **5 minutes**, a janitor marks the meeting `interrupted`: the audio
captured so far is preserved and the bundle stays open, so a client that
reconnects can still push its tail and call stop for a normal `ready` finish.
If the client stays gone for **another 5 minutes**, serve processes the partial
recording and closes the bundle, leaving the state at `interrupted` so nobody
mistakes it for a complete meeting. A pipeline failure leaves state `failed`
with the bundle intact, re-runnable from the Mac.

**Serve restart.** Meeting *sessions* are in-memory: after a restart every
**live id** answers `404`, and a recording cannot be resumed. This surface's
resilience covers network interruptions, not process restarts. Bundles are
closed at shutdown, so audio already delivered is preserved on the Mac. What
survives is the **bundle id** (D7): the meeting still lists in
`GET /v1/meetings`, still answers `GET /v1/meeting/{bundle-id}` and
`/document`, and can still be routed. The phone's outbox keeps its undelivered
tail on disk, but a dead live id can never accept it — report the loss rather
than retrying forever.

### Meeting history (D7)

```http
GET /v1/meetings?limit=200&since=2026-08-01T00:00:00Z
```

```json
{"meetings_dir":"/Users/lance/.obey/agents/voice/festival-voice/meetings",
 "count":42,"truncated":false,
 "meetings":[
   {"id":"weekly-sync-20260816-101500.meeting",
    "live_id":"9f2c4e1b77a30d55",
    "bundle":"/…/weekly-sync-20260816-101500.meeting",
    "document":"/…/meeting.md",
    "events_file":"/…/.samantha/events.jsonl",
    "description":"Weekly sync","source":"mac","state":"ready",
    "started_at":"2026-08-16T10:15:00Z","ended_at":"2026-08-16T10:47:12Z",
    "duration_seconds":1932,
    "utterances":214,"notes":3,"bookmarks":2,"errors":0,
    "speaker_status":"complete","speaker_count":3,
    "speaker_analysis_file":"/…/.samantha/speaker-analysis.json",
    "audio_file":"/…/audio.wav",
    "route":{"status":"routed","destination_id":"camp:blockhead",
             "type":"campaign","detail":"notes/meetings/weekly-sync-2026-08-16.md",
             "at":"2026-08-16T10:48:02Z","attempts":0,"retryable":false}}]}
```

Entries are newest first, read from the bundles on disk — which is what makes
history survive a restart. `limit` defaults to 200 and is capped at 1000;
`truncated` says entries were dropped. `since` is RFC3339. A missing meetings
dir is an empty list, not an error; a bad `limit` or `since` is a `400`
(`{"error":"invalid limit"}` / `{"error":"invalid since"}`).

`live_id` and a live `state` (`recording` | `processing`) are overlaid from
this process's session map when it owns that bundle — disk cannot know a
meeting is still recording, because the trailer is only written at the end.

`audio_file` is present **only** when `speaker.meeting.record_audio` was on
during the recording. Without it, speakers cannot be re-analyzed later; the
transient working file is never reported.

`route.retryable` is exactly the predicate `samantha meeting sweep` uses, so a
client offering "retry pending routes" promises only what the sweep delivers.

### Two ids, and which one to use

| Id | Shape | Lifetime | Where it comes from |
|---|---|---|---|
| **live id** | 16 lowercase hex | in-memory, dies with the serve process | `POST /v1/meeting/start` → `meeting_id` |
| **bundle id** | `<slug>-<YYYYMMDD-HHMMSS>[-<liveid>].meeting` | forever, unique in `meetings_dir` | `basename(Status.bundle)`; `id` in the index |

Every `Status` carries `bundle` from the first poll after start, so a client
learns its bundle id one request after starting — **persist it immediately**.
Use the live id while recording (segments and control only ever accept a live
session); use the bundle id for everything afterwards.

`{id}` in every `/v1/meeting/{id}` path accepts either. Resolution is: the
in-memory session first, then a `*.meeting` directory inside `meetings_dir`,
then `404` `{"error":"meeting: unknown meeting id"}`. An id that could not
name either — a path separator, a leading dot, anything over 121 characters —
is `400` `{"error":"meeting: invalid meeting id"}`, refused on shape before it
is joined onto a path.

| Route | Live session | Bundle id |
|---|---|---|
| `GET /v1/meeting/{id}` | unchanged | `200` with `state: ready\|interrupted`, `bundle`, `title`, `started_at`, and `result` rebuilt from the bundle; no `missing_seqs`, no `step` |
| `GET /v1/meeting/{id}/document` | unchanged | `200 text/markdown`; `409` `{"error":"meeting: notes are not ready yet"}` when the document is not written |
| `POST /v1/meeting/{id}/route` | unchanged | allowed; `409` `{"error":"meeting: meeting was already routed to <dest>"}` when the bundle already carries a `routed` event |
| `PUT …/segments/{seq}`, `POST …/control`, `POST …/stop` | unchanged | `409` — a finished bundle never takes audio or control again, and a bundle id is never a substitute for the live id of a recording in flight |

A bundle id that names a meeting **this serve is still recording** reads and
routes as the live meeting does — `state: recording`, `409` on route until the
notes exist — because on disk it only looks finished: no trailer, no notes, no
summary yet.

**Mid-meeting idea capture** reuses `POST /v1/intent` unchanged (typed text,
optionally carrying `context: {meeting_id, offset_ms}`); spoken ideas are
marked with `idea_start` / `idea_end` controls and resolved from the
transcript after stop. Meetings land in campaign `notes/meetings/`; ideas land
in the Inbox — the sinks stay separate.

**Out of scope for v1:** meeting audio over `/v1/stream`, multiple concurrent
meetings per serve, and on-phone STT.

## WebSocket `/v1/stream`

Connect: `wss://host:port/v1/stream?token=...` (or Bearer on non-browser clients).

### Client → server (JSON text frames)

| `type` | Fields | Meaning |
|--------|--------|---------|
| `text_input` | `text` | Enqueue a text turn |
| `interrupt` | | Cancel in-flight turn; server also sends `audio_reset` |
| `clear_history` | | Wipe conversation history |
| `audio_output` | `mode`: `stream` \| other | Opt into TTS `audio_chunk` delivery (`stream` on) |
| `voice_start` | | Exclusive mic claim + start remote STT turn |
| `audio_input` | `data` base64, `sample_rate` 16000 | PCM s16le mono @ 16 kHz |
| `voice_end` | | Finalize utterance; release mic claim |
| `set_persona` | `name`: persona id | Switch persona for **subsequent** turns (see below) |

### Server → client

**Events** mirror the host bus (`type` = wire event name), e.g.:

- `user_input`, `transcript_partial`, `thinking_started`, `thinking_complete`
- `response_ready`, `turn_metrics`, `error`, `info`, `conversation_cleared`
- speech lifecycle: `generating_voice`, `speaking_started`, `speaking_complete`, …

`user_input` fields: `text` (string) and `speaker` (string, optional) — the
stable live label for the utterance (`speaker-1`, or the enrolled name once
seeded). Absent for text turns and when live analysis produced no label; a
client must not assume the key is present.

**Audio** (only if `audio_output` mode is `stream`):

| `type` | Fields |
|--------|--------|
| `audio_chunk` | `data` (base64 pcm_s16le), `sample_rate`, `segment_id` |
| `audio_end` | `segment_id`, `reason` (`complete` / `interrupted` / …) |
| `audio_reset` | Clear client playback after interrupt |
| `audio_output_ack` | `mode` applied |
| `set_persona_ack` | `id`, `display_name`, `prompt_hash`, `applies_to` |

## Audio formats

| Direction | Format |
|-----------|--------|
| TTS → client | `pcm_s16le`, mono, sample rate in envelope (often 24 kHz from Kokoro) |
| Mic → server | `pcm_s16le`, mono, **16000 Hz** (client resamples) |

## Security notes

- No public Funnel / UPnP — LAN or Tailscale only.
- Remote tool calls default **off** (`remote_tools_enabled`).
- Treat network reach as keyboard reach: keep the token private.

## Client checklist (Swift / web)

1. Discover URL (MagicDNS banner, mDNS `_samantha._tcp`, or config).
2. Pair once → persist token + optional cert fingerprint.
3. Open WebSocket with token.
4. Send `audio_output` `{mode:stream}` for playback.
5. Text: `text_input`. Voice: `voice_start` → stream `audio_input` → `voice_end`.
6. On interrupt: stop local playback, send `interrupt`, wait for `audio_reset`.
7. Reconnect with same token after backgrounding; optional `GET /v1/sessions` + resume.


## `set_persona`

```json
{"type": "set_persona", "name": "pirate"}
```

Ack:

```json
{"type": "set_persona_ack", "id": "pirate", "display_name": "Pirate",
 "prompt_hash": "a1b2c3d4e5f6", "applies_to": "next_turn"}
```

`prompt_hash` is the first 12 hex characters of the sha256 of the **assembled
prompt text** (placeholders unresolved) — the same digest
`samantha persona show --json --with-prompt` reports. It changes whenever the
document changes, which is how a client tells "the prompt I just edited is the
prompt the model now sees". It is not a document name and must not be compared
to one. Empty means serve could not resolve the document.

**`applies_to` is always `next_turn`, and that is a guarantee rather than a
limitation.** A conversation binds its identity — persona, prompt, voice, brain
routing — when it starts, and keeps it for its whole life. A switch therefore
cannot retarget a turn already in flight, and the ack says so explicitly rather
than leaving the client to assume otherwise.

Editing the *document* behind the persona a session is already bound to does
take effect on the next turn; that is a different axis from switching identity.

The change is not persisted: a remote client selecting a persona does not
rewrite the host's `config.yaml`. `samantha persona use` remains the persisting
path.

An unknown name returns an error envelope naming the personas that do exist.
Per-connection personas are out of scope — the pipeline is single-brained, so
this is per-instance.


## `GET /v1/personas`

The read side of `set_persona`: which personas this serve can switch to, and
which one it is using right now.

```json
{"personas": [
  {"id": "samantha", "display_name": "Samantha", "active": false, "builtin": true,
   "brain": {"provider": "ollama", "model": "qwen2.5:14b"},
   "tts": {"provider": "kokoro", "voice": "af_heart", "tier": ""}},
  {"id": "uncle-fu", "display_name": "Uncle Fu", "active": true, "builtin": false,
   "brain": {"provider": "ollama", "model": "qwen2.5:14b"},
   "tts": {"provider": "qwen3-tts", "voice": "Uncle_Fu", "tier": "1.7b"}}
]}
```

`brain` and `tts` are the **effective** stack: a persona leaves a field empty to
inherit the app default, and this route resolves that inheritance so a client
never has to. `tier` is empty for providers that do not select a model tier.

`active` is the **runtime** persona. A `set_persona` deliberately does not write
`config.yaml`, so this field can name a persona the persisted config does not —
that is correct, and a client that reads the config file instead will be wrong
for the rest of the session.

`builtin` marks the shipped persona, which cannot be deleted. It is additive
beyond ADR-004; decode it as optional.

The route is **absent (404)** on a serve that did not wire persona resolution,
the same way `/v1/meeting/*` is absent without meeting capture. Feature-detect
by status rather than by `protocol_version`, which does not change for additive
routes.
