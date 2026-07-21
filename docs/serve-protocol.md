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
`~/.obey/agents/voice/samantha/serve/tls/`, mutes the host speaker by default,
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

Primary token file: `~/.obey/agents/voice/samantha/serve/token` (0600).  
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
