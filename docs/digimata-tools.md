# digimata tools — relevance index

Short map of [digimata](https://github.com/orgs/digimata/) repositories that
came up while evaluating monetization and adjacent tooling for Obedience Corp /
Samantha. This is an **evaluation index**, not a product dependency list.
Nothing here is required to build or run Samantha.

Org: https://github.com/orgs/digimata/repositories  
Date: 2026-07-29

## Priority ranking

| Priority | Repo | Role | Samantha / OBC action |
|----------|------|------|------------------------|
| **Product / money** | [flexprice](https://github.com/flexprice/flexprice) (upstream) | Usage metering, credits, plans, entitlements, invoices | Track for monetization; see [flexprice.md](flexprice.md) |
| **Product shell** | [saasy](https://github.com/digimata/saasy) | Multi-tenant SaaS starters (B2B Next+BetterAuth, B2C Vite+Clerk) | Steal patterns when a web SaaS face is needed |
| **Ops / knowledge** | [kdb](https://github.com/digimata/kdb) | Structural index CLI + LSP (docs + code graph, search, tasks) | Pilot on campaign workspace; not a runtime dep |
| **Personal capture** | [quill](https://github.com/digimata/quill) | Local macOS meeting record + dual-track STT | Use personally; patterns only for product |
| Lower | parrot, blade, imsg, xio, gh-render, opendoc, terrazzo, chatbot | Dictation, Slack→Claude, local CLIs, agent docs | Ad hoc interest |

## Flexprice

**Use upstream** [flexprice/flexprice](https://github.com/flexprice/flexprice),
not the digimata fork ([digimata/flexprice](https://github.com/digimata/flexprice)),
which lags.

- Billing/metering layer above Stripe (etc.), not a card processor.
- Fits AI/voice: usage events, credit wallets, seat plans, feature gates.
- Full write-up: **[flexprice.md](flexprice.md)**.

## saasy

Multi-tenant starters only — auth and app shell, not money:

| Variant | Stack |
|---------|--------|
| `b2b-betterauth-next/` | B2B · Next.js · Better Auth |
| `b2c-clerk-vite/` | B2C · Vite + React · Clerk |

Pair with Flexprice (or Stripe alone) when charging; do not expect billing
inside saasy.

## kdb

Structural index for a workspace: markdown headings/wikilinks and code
symbols/imports share one model (outline, refs, deps, check, FTS search,
projects/spaces/tasks, CODEMAP lint). Rust CLI + LSP (Zed first). MIT.

**Opinion:** High value for multi-repo campaign / festival / docs-code work;
zero value as a Samantha product dependency. Pilot at the campaign root, not
inside the voice binary tree.

**Campaign pilot draft:**  
`~/campaigns/obedience-growth-rd/docs/guides/kdb-pilot.md`  
(when that campaign is checked out on this machine).

## quill

Menu-bar macOS meeting recorder: mic + system audio as two tracks (free
`me`/`them` diarization), on-device Parakeet STT, session dirs under
`~/Recordings`, optional `on_stop` shell hook. macOS 15+, Apple Silicon
preferred. Sibling of [parrot](https://github.com/digimata/parrot).

**Opinion:** Excellent personal/local capture tool. Not Samantha STT —
different job (batch meeting vs low-latency conversational pipeline). Steal
dual-track + local STT + hook ideas for a separate capture product if needed;
do not merge into Samantha’s sherpa/malgo path.

## What not to do

- Do not add digimata tools as git submodules of Samantha “just in case.”
- Do not self-host Flexprice until monetization scope is real; hosted + Stripe
  is the first path if you go live.
- Do not replace fest / camp with kdb tasks on day one — run them side by side
  for a short pilot and keep whichever reduces friction.

## Related in this repo

| Doc | Purpose |
|-----|---------|
| [flexprice.md](flexprice.md) | Flexprice evaluation and integration sketch |
| [serve-protocol.md](serve-protocol.md) | Product remote protocol (unrelated to digimata) |
| [adr/](adr/) | Product ADRs |

## Changelog

| Date | Note |
|------|------|
| 2026-07-29 | Initial index after digimata / Flexprice review |
