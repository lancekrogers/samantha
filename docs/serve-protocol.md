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

Interactive: launch `samantha`, then choose **Use on iPad (Tailscale)**. The
TUI displays the MagicDNS URL and pairing code and stops the child server when
the user leaves the screen.

Headless/CLI:

```bash
samantha serve --tailscale
```

Binds the node’s Tailscale IPv4, loads a cert via `tailscale cert` into
`~/.obey/agents/voice/samantha/serve/tls/`, mutes the host speaker by default,
and prints the MagicDNS URL (e.g. `https://mac.tailnet.ts.net:7262/`).

Requires: Tailscale CLI logged in, MagicDNS on, HTTPS certs allowed for the
tailnet if `tailscale cert` fails.

## Auth

| Mechanism | Where |
|-----------|--------|
| `Authorization: Bearer <token>` | All `/v1/*` except pair + static page |
| `?token=` query | **Only** `GET /v1/stream` (browser WebSocket cannot set headers) |
| Pairing code | `POST /v1/pair` (public, rate-limited) |

Token file: `~/.obey/agents/voice/samantha/serve/token` (0600).  
Revoke: `samantha serve --revoke-tokens`.

### Pairing

1. Serve prints a 6-digit code (single-use, ~10 minutes).
2. Client:

```http
POST /v1/pair
Content-Type: application/json

{"code":"482193"}
```

3. Response:

```json
{"token":"<hex>","fingerprint":"<sha256 of leaf cert DER>"}
```

Store token (Keychain / localStorage). Pin `fingerprint` for TOFU if desired.

## REST

| Method | Path | Auth | Purpose |
|--------|------|------|---------|
| `GET` | `/v1/status` | yes | `turn_active`, `providers`, `uptime_seconds`, `fingerprint` |
| `GET` | `/v1/sessions` | yes | Session summaries |
| `POST` | `/v1/sessions/{id}/resume` | yes | Load history into the live pipeline |
| `POST` | `/v1/pair` | no | Exchange pairing code for token |

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
