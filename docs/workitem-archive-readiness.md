# Workitem Archive Readiness — SR0001

Date: 2026-07-09

## Original design workitems

| Workitem | Location | Status | Evidence |
|----------|----------|--------|----------|
| WI-5ca46f samantha-config-driven-prompts | workflow/design/dungeon/completed/2026-07-08/ | Already completed | PRs #65/#66 merged; festival 003/01 recorded as external completion |
| WI-816569 samantha-architecture-gap-closure | workflow/design/dungeon/completed/2026-07-09/ | Absorbed + implemented in SR0001 | Phase 003–006: maintenance, sectioned render, planning controls, typed TTS, TUI audiobook generator, STT ADR |
| WI-931cb8 samantha-pdf-prompt-audiobooks | workflow/design/dungeon/completed/2026-07-09/ | Absorbed + implemented in SR0001 | Phase 005: narrate plan/prepare/render, pdftotext, doctor, direct PDF render/audiobook, profiles |

## Recommendation

No further dungeon moves required. Workitems already live under `dungeon/completed/2026-07-09/` (and config-driven under `2026-07-08/`). Festival completion is the remaining promotion step.

## Deferred (explicit)

- M4B chapter metadata enrichment (design D6)
- `--code-blocks summarize` (depends on batch brain polish)
- Full STT `Start(ctx, source)` migration (see ADR 0001)
