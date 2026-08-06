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

## REST

| Method | Path | Auth | Purpose |
|--------|------|------|---------|
| `GET` | `/v1/status` | yes | `turn_active`, `providers`, `uptime_seconds`, `fingerprint` |
| `GET` | `/v1/sessions` | yes | Session summaries |
| `POST` | `/v1/sessions/{id}/resume` | yes | Load history into the live pipeline |
| `POST` | `/v1/pair` | no | Exchange pairing code for token (optional `device_name`) |
| `GET` | `/v1/devices` | yes | List paired devices (D2) |
| `DELETE` | `/v1/devices/{id}` | yes | Revoke one device token + streams (D2) |
| `POST` | `/v1/intent` | yes | Capture intent (D3; file sink) |
| `GET` | `/v1/intent/targets` | yes | Intent routing targets (D3) |

## Meetings (draft — pending implementation)

> **Status: not implemented yet.** This section documents the agreed wire for
> the `/v1/meeting` surface (Obey Voice delta D6, festival OV0003, workitem
> `WI-f3c18a`) so clients and serve agree before code lands. Until the
> handlers ship, `/v1/meeting/*` returns 404 and `GET /v1/status` does not
> advertise the `meetings` capability.

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

```http
POST /v1/meeting/start
{"title":"Standup","campaign":"mytools"}
```

```json
{"meeting_id":"…","segment_seconds":5,"outbox_cap_segments":120}
```

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

`action` is one of `pause`, `resume`, `bookmark`, `idea_start`, `idea_end`.
Each becomes an event in the bundle's `.samantha/events.jsonl` using the same
schema a desktop recording writes.

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
 "missing_seqs":[],"result":{…}}
```

**Failure semantics.** A phone that loses the network keeps recording into its
own outbox and resumes from the first unacked seq. If serve sees no segment or
control for **5 minutes**, a janitor marks the meeting `interrupted`: the audio
captured so far is preserved and the bundle stays open, so a client that
reconnects can still push its tail and call stop for a normal `ready` finish.
If the client stays gone for **another 5 minutes**, serve processes the partial
recording and closes the bundle, leaving the state at `interrupted` so nobody
mistakes it for a complete meeting. A pipeline failure leaves state `failed`
with the bundle intact, re-runnable from the Mac.

**Serve restart.** Meeting sessions are in-memory: after a serve restart,
every existing meeting id answers `404`. This surface's resilience covers
network interruptions, not process restarts. Bundles are closed at shutdown,
so audio already delivered is preserved on the Mac and the meeting finishes
through the desktop tooling (`samantha meeting route` / reprocess); the
phone's outbox keeps its undelivered tail on disk.

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

### Server → client

**Events** mirror the host bus (`type` = wire event name), e.g.:

- `user_input`, `transcript_partial`, `thinking_started`, `thinking_complete`
- `response_ready`, `turn_metrics`, `error`, `info`, `conversation_cleared`
- speech lifecycle: `generating_voice`, `speaking_started`, `speaking_complete`, …

**Audio** (only if `audio_output` mode is `stream`):

| `type` | Fields |
|--------|--------|
| `audio_chunk` | `data` (base64 pcm_s16le), `sample_rate`, `segment_id` |
| `audio_end` | `segment_id`, `reason` (`complete` / `interrupted` / …) |
| `audio_reset` | Clear client playback after interrupt |
| `audio_output_ack` | `mode` applied |

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
