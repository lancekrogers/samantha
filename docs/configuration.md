# Configuration

[Back to README](../README.md)

Config lives at `~/.obey/agents/voice/festival-voice/config.yaml`. Values can also be overridden with environment variables where listed.

`samantha config set <key> <value>` rewrites only that key's line: comments,
blank lines and key order survive, a timestamped `.bak` is kept (newest five),
and the replacement is atomic. It is the writer the TUI and the Obey Voice Mac
app both use, so three front ends cannot clobber each other's edits.

The value is read according to the key's type, which `samantha config schema`
publishes along with the bounds, accepted values, help text, and whether a
change needs an agent restart. A `list<string>` value is a JSON array:

```bash
samantha config set skills_disabled '["pdf-fill","calibre"]'
samantha config set skills_disabled '[]'
```

`samantha config unset <key>` is the way back: it removes the key's lines from
config.yaml — as surgically as `set` writes them — so the built-in default, or
the key's environment variable, applies again and `config get` reports the
value's source as `default`. Removing a key the file does not hold changes
nothing and reports `changed: false`.

Clearing a key is not the same as writing an empty value to it. `config set
stt_mode ""` stores an empty string, which pins the key; `config unset
stt_mode` removes it, so a future default change or an exported `STT_MODE` is
free to take effect.

`config schema` marks every key that accepts an empty value `allows_empty:
true`: all the text keys, plus the enums whose own default is blank
(`stt_mode`, `meeting.route.default`, `qwen_tts_mode`, …). A bool, a number, a
list, or an enum with a real default (`tts_provider`) refuses one. For the
enums, blank *is* the unset state, so writing it back returns the key to it —
a front end offering an "(App default)" choice wants `allows_empty` together
with an empty `default`.

`config schema` and `config get` never write to the install root, and never
fail on a config the loader would reject. Add `--json` to any of them for a
machine-readable payload; `config set` and `config unset` exit 0 on success, 1
when the operation fails (with an error `code` in the payload), and 2 when
called with the wrong number of arguments.

Persona **profiles** live under `~/.obey/agents/voice/festival-voice/personas/<id>/persona.yaml`.
On load, the active profile overlays `agent_name`, the persona prompt name, and per-persona TTS
(`tts.provider` + `tts.voice` → `tts_provider` and either `tts_voice` or `qwen_tts_voice`).
Each persona can use any supported TTS backend and any voice for that backend.
Prompt bodies stay in `prompts/` (see `samantha prompts`).

> **Upgrading:** installs that saved settings while `claude_max_session_tokens` defaulted to `60000` still carry that value in `config.yaml` and keep the silent session drops. Delete the key (or set `0`) to adopt the warn-only default. `samantha doctor` flags this.

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
| `ollama_think` | `false` | `OLLAMA_THINK` | Ollama thinking/reasoning for models that support it (qwen3.x, …). Voice defaults off so the model returns speakable content; empty content was previously replaced with the “lost my train of thought” fallback. Set `true` or `low`/`medium`/`high`/`max` to re-enable. |
| `claude_max_session_tokens` | `0` | `CLAUDE_MAX_SESSION_TOKENS` | Opt-in fuse: caps the prompt the `claude` CLI replays on `--resume`. Past it the brain starts a fresh CLI session from recent history. `0` (default) trusts the CLI session and resumes forever — recommended for interactive use; set a cap for unattended/serve setups. |
| `claude_session_warn_tokens` | `60000` | `CLAUDE_SESSION_WARN_TOKENS` | Replayed-prompt size at which a visible warning appears (log + activity feed), once per CLI session. Nothing is dropped. `0` disables the warning. |
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
| `vad_silence_duration` | `0.5` | | Seconds of silence before ending speech (raise to stop being cut off) |
| `vad_threshold` | `0.6` | `VAD_THRESHOLD` | Speech-detection confidence (raise to ignore background noise) |
| `vad_min_speech_duration` | `0.25` | `VAD_MIN_SPEECH_DURATION` | Minimum speech length in seconds (raise to ignore brief noises) |
| `voice_frontend_enabled` | `false` | `VOICE_FRONTEND_ENABLED` | Local AEC/NS/AGC on mic input (off by default: the noise suppressor currently over-suppresses normal-volume speech; enable only with barge-in) |
| `tui_mouse_enabled` | `false` | `TUI_MOUSE_ENABLED` | Claim the mouse so the wheel/trackpad is routed to the transcript viewport. Off by default so the terminal keeps click-drag selection and copy; turning it on means selecting needs a modifier (option/fn/shift). Either way `Page Up`/`Page Down` scroll, and with it off the wheel scrolls whenever the composer is empty. Applies on next enter/leave of conversation (Settings → Tools toggle or re-entry); no full process restart required for the TUI claim |
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

## Agent Skills (Ollama)

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

## STT schema migration

The preferred STT schema is `stt_provider` + `stt_mode` (e.g. `stt_provider: sherpa` with `stt_mode: streaming`). The legacy compound aliases (`sherpa-streaming`, `sherpa-offline`) still work with `stt_mode` unset and are never rewritten; combining a compound alias with a conflicting `stt_mode` is a config error.

Use `samantha config migrate --dry-run` to preview the explicit
`stt_provider`/`stt_mode` values that would preserve the current STT behavior.
Dry runs report the config path and proposed values without writing files. Use
`samantha config migrate --write` to apply the migration; it creates a
timestamped `.bak` file before replacing an existing config. The write path
updates YAML via `yaml.v3`, so comments and unrelated keys are preserved where
possible, but scalar formatting around touched keys may be normalized.
