# embed — a host application for the samantha voice agent

A ~120 line program that imports `pkg/voiceagent` and runs a turn.

It is a **separate Go module** with a `replace` directive pointing at a local
checkout. That is deliberate and load-bearing: compiling in-tree would still
succeed if the library secretly leaned on `internal/`, so only a separate module
proves importability.

```sh
go run .              # uses your real config
go run . -hermetic    # config built in code, every provider injected
```

## The hermetic gate

This is the acceptance criterion for the whole library extraction: **can a host
construct and run the agent with no samantha installation on the machine?**

It only means something run somewhere genuinely clean. Running as your own user
with `~/.obey` present proves nothing, because any leftover reach-in would find
the directory and silently succeed.

### Procedure

```sh
FAKEHOME=$(mktemp -d)
go build -o /tmp/embed-bin .
env -i HOME="$FAKEHOME" PATH=/usr/bin:/bin TMPDIR=/tmp /tmp/embed-bin -hermetic
find "$FAKEHOME"          # must still be empty
```

`env -i` clears the environment, so nothing is inherited — no `SAMANTHA_*`, no
`OLLAMA_MODEL`, no `XDG_*`. `-hermetic` states the config in code, injects the
brain and TTS, sets `Env` explicitly and points `PromptsDir` at a path that does
not exist.

### Result — 2026-08-11, passing

```
event: events.UserInput
event: events.ThinkingStarted
event: events.ResponseStreamingStarted
event: events.ResponseDelta
event: events.ThinkingComplete
event: events.ResponseReady
event: events.TurnMetrics
ok: agent constructed, ran a turn, and shut down cleanly
EXIT=0
```

The fake home was still empty afterwards: nothing was read from it and nothing
written to it.

Re-run this after any change to config resolution, provider construction or path
lookups. A failure here is a global reach-in, and the failure message will point
at the directory it wanted.

## Known limits

**The mechanical `grep -rn 'os.Exit\|os.Getenv' pkg/` check does not return
nothing**, and cannot without breaking documented behaviour. Twelve reads remain:

| Where | Why it stays |
|---|---|
| `config` | `BRAIN_PROVIDER`, `VOICE_TOOLS_ENABLED`, `SKILLS_ENABLED`, `SAMANTHA_SYS_MEM_BYTES` — the documented env-var configuration contract |
| `qwen` | `SAMANTHA_QWEN_NATIVE_URL`, `QWEN_TTS_NATIVE_SHA256` and friends |
| `brain`, `prompts` | `USER`, reached **only** when the caller supplied no `Env` or placeholder value |

The intent behind that check — the library must not reach for process state
behind the caller's back — is what the hermetic run above actually verifies, and
it verifies it directly rather than by proxy. `os.Exit` in `pkg/` is **zero**,
which is the half that matters for embedding: a library must never terminate its
host.

**Without `-hermetic`** the example needs a working brain provider. With none
configured it exits with `init brain: ollama_model not configured — run: samantha
config ollama_model <model>`, which is the error path behaving.
