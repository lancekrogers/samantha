# ADR 0004: The `integration` Build Tag Selects the CGO-Free CLI

## Status

Accepted — documents the convention already in force. Written because
`go build -tags integration ./...` fails, and that failure has twice been
mistaken for a defect on `main`.

## Context

Container integration tests run the CLI on Linux with `CGO_ENABLED=0`. Audio
and speaker work cannot survive that: `internal/speaker` and
`pkg/voiceagent/audio` both import `github.com/k2-fsa/sherpa-onnx-go`, whose
platform packages exclude every Go file without cgo.

The `integration` tag is how those packages are kept out of that build. It
selects an alternate compilation of the CLI, not an additional one:

| Files | Role |
|---|---|
| 26 in `cmd/samantha/cmd` tagged `!integration` | real commands |
| 6 in `cmd/samantha/cmd` tagged `integration` | stubs replacing them |
| 3 in `internal/meeting`, 1 in `internal/listen` tagged `!integration` | reach `internal/speaker` / `pkg/voiceagent/audio` |

The name is misleading. It reads as "also build the integration tests", and
that reading is what produces the confusing failure:

```
$ go build -tags integration ./...
# github.com/lancekrogers/samantha/internal/tui
internal/tui/meeting.go:38:27: undefined: meeting.AnalysisStatus
internal/tui/runtime.go:68:50: undefined: meeting.AnalysisResult
```

`internal/tui` uses `meeting.AnalysisResult` unconditionally, and that type
embeds `speaker.Timeline` — so it cannot exist in a build whose whole purpose
is to exclude the sherpa dependency. The error is the tag working, reported
badly.

## Decision

1. **`-tags integration` means "the CGO-free container CLI".** It is a
   variant selector, not a superset.
2. **`go build -tags integration ./...` is not a supported invocation** and is
   not expected to pass. Neither is `go test -tags integration ./...`. Build
   or test the specific packages that opt in.
3. A package excluded from the tagged build needs no counterpart unless a
   tagged package imports it. `cmd/samantha/cmd` has counterparts because the
   CLI must still compile; `internal/meeting` and `internal/listen` have none
   because nothing in the tagged build reaches them — `internal/tui` is itself
   stubbed out at the `cmd` layer.

## Consequences

- What CI runs, and all that is expected to pass:
  - `go run ./internal/buildutil integration` — container CLI build + `tests/integration`
  - `go test -race -tags integration ./tests/voiceflow/...`
- Local hardware tests name their own package, e.g.
  `go test -tags integration ./pkg/voiceagent/tts/ -run TestKokoroRealtimeFactor`.
  These are unaffected by the `./...` failure above; the TTS realtime-factor
  instrument that decisions D009 and D010 both rest on runs normally.
- Adding a package that imports `internal/speaker` or
  `pkg/voiceagent/audio`: tag it `!integration` if anything in the tagged CLI
  build could reach it, and add a stub counterpart only if the tagged build
  stops compiling without one.
- Renaming the tag to `containercli` would remove the ambiguity at the cost of
  touching 32 files and the CI workflow. Not done here; this ADR is the
  cheaper fix for the same confusion.

## Alternatives Considered

1. **Split `internal/speaker` into pure types plus a sherpa implementation** so
   `internal/tui` compiles under the tag — rejected. It is a wide refactor
   whose only benefit is making an unsupported command succeed; the container
   build already stubs `internal/tui` away at the `cmd` layer.
2. **Drop the `!integration` tags from `internal/meeting` and
   `internal/listen`** — rejected; verified to break the container build,
   which is the job the tag exists to do.
3. **Leave it undocumented** — rejected. Two separate investigations have now
   started from the assumption that `main` was broken.
