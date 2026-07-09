# ADR 0001: STT Source Lifecycle Ownership

## Status

Accepted — **keep current construction** (no `Start(ctx, source)` migration in this festival).

## Context

Samantha's STT providers own microphone/source setup at construction time. Gap-closure item E4 asked whether providers should instead accept a source at `Start(ctx, source)` so lifecycle and test injection are clearer.

## Decision

Keep the current construction-time source ownership for this release train.

## Consequences

- No STT API break; interactive pipeline unchanged.
- Future migration may revisit `Start(ctx, source)` if multi-source capture or stronger DI is needed.
- Tests continue to inject fakes at construction.

## Evidence

- Existing providers (`sherpa`, streaming variants, whispercpp) bind capture devices/files during `New*` construction and shutdown on provider close.
- Batch render/narrate paths never touch STT; a migration would only benefit interactive voice.
- Risk of a half-migrated dual path exceeds benefit for the remaining-gaps festival scope.

## Alternatives Considered

1. **Full `Start(ctx, source)` migration** — cleaner DI, but touches every provider, pipeline, and TUI warm path for little user-visible value now.
2. **Spike only** — rejected; evaluation already answers the design question without code churn.
