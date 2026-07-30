# Flexprice — monetization infrastructure

Evaluation and reference notes for [Flexprice](https://flexprice.io/): open-source
usage-based metering, pricing, entitlements, and invoicing aimed at AI-native
and SaaS products. Relevant if Samantha (or other Obedience Corp surfaces)
charges by plan, seats, credits, or metered usage.

**Status:** evaluated / not integrated (2026-07-29).  
**Use upstream, not the digimata fork.**

| | |
|---|---|
| Upstream (preferred) | https://github.com/flexprice/flexprice |
| digimata fork (snapshot) | https://github.com/digimata/flexprice |
| Docs | https://docs.flexprice.io |
| Product | https://flexprice.io |
| SDKs | Go · Python · JS (`@flexprice/sdk`) |

The digimata org mirrors Flexprice for local tooling interest; the digimata copy
lags the active upstream (fewer commits, older tip). Integrate against
`flexprice/flexprice` and the official cloud unless digimata-specific patches
are required later.

Org-wide tool map: **[digimata-tools.md](digimata-tools.md)**.

## What it is (and is not)

Flexprice is **billing and metering infrastructure**, not a card network.

| Layer | Role |
|-------|------|
| **Flexprice** | Plans, meters, credit wallets, feature entitlements, subscriptions, invoices, checkout sessions, webhooks |
| **Payment processor** | Stripe, Razorpay, Paddle, Whop, etc. — actually move money |
| **Your app** | Create customers, ingest usage events, gate features via entitlements |

It is built for hybrid monetization common to AI products: seat subscriptions,
pay-as-you-go usage, prepaid credit packs, free tiers with overage, and
per-customer plan overrides — without hard-coding pricing rules into product
code.

It is **not** a replacement for simple one-SKU Stripe Checkout. If the only
need is a flat monthly subscription with no metering, Stripe Billing alone is
smaller and faster.

## Why it is interesting for Obedience Corp

Obedience Corp products already skew AI / voice / multi-surface (e.g. Samantha
voice, remote serve, campaign tooling). Pricing that will age well tends to
include some combination of:

| Product shape | Flexprice model |
|---------------|-----------------|
| Voice minutes / turns | Metered events (`voice_seconds`, `conversation_turns`) |
| LLM / agent work | Token or `agent_run` meters; AI cost-tracking docs exist |
| Org seats | Seat-based subscription + entitlements |
| Prepaid packs | Wallets, credit grants, auto top-up thresholds |
| Feature gates | Boolean or usage entitlements (pro TTS, multi-device, API) |

Their cookbooks explicitly target patterns like usage-based voice (Vapi-style),
credit packs (Apollo-style), and hybrid subscription + overage (Resend-style).

## Core capabilities

- **Usage metering** — custom events, real-time aggregation, bulk ingest, usage
  analytics; high-volume pipeline (Kafka + ClickHouse in self-hosted stack).
- **Credits / wallets** — prepaid and promotional grants, top-ups, expiration,
  balance alerts.
- **Plans & prices** — seat, usage, hybrid, add-ons, per-customer overrides,
  plan versioning / clone.
- **Entitlements / features** — on/off flags, metered limits, config values
  tied to plan; query by customer or external id for gating in-app.
- **Subscriptions & invoicing** — proration, draft → finalize, PDF, one-offs,
  credit notes, tax associations.
- **Checkout** — hosted / session-based checkout that activates subscription on
  payment (with processor webhooks).
- **Integrations** — payment processors, CRM/accounting sync paths, webhooks
  for invoice/subscription/wallet lifecycle, optional MCP server for agents.
- **Deploy modes** — hosted cloud, or self-host (open core).

## Architecture (self-host mental model)

```text
App / agents / pipelines
        │  usage events (API / SDK / bulk / collector)
        ▼
   Flexprice core
   (meters → credits → plans → invoices → entitlements)
        │
        ├──► Stripe / Razorpay / …  (charge)
        ├──► CRM / accounting       (sync)
        └──► Webhooks → your app    (gate, notify)
```

Self-host stack (from upstream Docker Compose / `make dev-setup`):

| Component | Role |
|-----------|------|
| PostgreSQL | Primary application state |
| Kafka | Event streaming for usage |
| ClickHouse | High-volume usage analytics |
| Temporal | Workflows (billing period, invoice finalization, etc.) |
| Flexprice API / consumer / worker | Control plane + processing |

Local API default after setup: `http://localhost:8080` (Temporal UI `:8088`,
ClickHouse UI `:8123`).

Cloud avoids operating that stack; self-host maximizes data control and avoids
hosting fees at the cost of ops.

## Integration sketch (product path)

Not implemented in Samantha today. When monetization lands, the intended
shape is:

1. **Customer** — map app user (or org) to Flexprice customer via
   `external_customer_id`.
2. **Plan / wallet** — subscribe (seat or hybrid) and/or fund a credit wallet
   through checkout.
3. **Meter** — emit events from product boundaries (e.g. after a voice turn,
   after remote serve usage, after batch narration minutes). Prefer coarse,
   stable event names that survive pricing changes.
4. **Gate** — before expensive or premium work, query entitlements / wallet
   balance; fail closed with a clear upgrade path.
5. **Invoice / charge** — Flexprice computes invoices; payment processor
   collects; webhooks update local entitlement cache if needed.

Example event names (illustrative only):

```text
samantha.voice.turn          properties: duration_ms, stt, tts, brain
samantha.serve.session       properties: minutes, client
samantha.narrate.minutes     properties: profile, provider
```

Keep pricing tables **out** of Go business logic; change plan prices and
included credits in Flexprice without a product release when possible.

## License and legal notes

Flexprice is **open core**:

- Core: **AGPLv3** (majority of the public repo).
- Enterprise paths: commercial (`ee` / `internal/ee` style split; confirm on
  the version you pin).

Implications:

- Self-hosting for internal Obedience Corp ops is usually fine.
- Embedding Flexprice into a proprietary multi-tenant SaaS offered to third
  parties can trigger AGPL network/source obligations — use their commercial
  license or the hosted product if that is a concern.
- digimata’s fork inherits the same license posture; do not assume MIT.

Re-read `LICENSE` and enterprise docs on the **pinned** release before shipping.

## When to use Flexprice

**Yes, if:**

- Pricing involves usage, credits, hybrid plans, or frequent plan experiments.
- Multiple products (voice, agents, seats) should share one metering/billing
  brain.
- You want entitlements and invoices without building a custom billing service.
- You already accept Stripe (or similar) and need the layer *above* payments.

**No / not yet, if:**

- Single flat SKU, no meters, no credit wallet — use Stripe Billing / Checkout.
- You are not ready to own Kafka + ClickHouse + Temporal (and do not want
  hosted Flexprice).
- Time-to-first-dollar is measured in hours and a simple payment link is enough.

## Operational recommendation

| Step | Choice |
|------|--------|
| Source of truth | Upstream `flexprice/flexprice` + docs.flexprice.io |
| First deploy | Hosted Flexprice + Stripe (minimize ops) |
| Later | Self-host if data residency, cost, or AGPL strategy demand it |
| Product shell | Separate concern — e.g. digimata `saasy` templates for auth/tenancy |
| Samantha today | Document only; no runtime dependency |

## Related digimata tools (context only)

Full index: **[digimata-tools.md](digimata-tools.md)**.

| Repo | Role | Notes |
|------|------|--------|
| [digimata/saasy](https://github.com/digimata/saasy) | Multi-tenant SaaS starters | B2B BetterAuth+Next, B2C Clerk+Vite — app shell, not billing |
| [digimata/kdb](https://github.com/digimata/kdb) | Knowledge + code structural CLI/LSP | Campaign pilot; not a Samantha dependency |
| [digimata/quill](https://github.com/digimata/quill) | Local macOS meeting record + STT | Personal capture; patterns only |

## References

- Upstream README / architecture: https://github.com/flexprice/flexprice
- API surface (events, wallets, plans, checkout, webhooks): https://docs.flexprice.io
- Cookbooks: usage-based, credit-based, hybrid — under docs “boiler-plate”
- digimata org listing: https://github.com/orgs/digimata/repositories

## Changelog

| Date | Note |
|------|------|
| 2026-07-29 | Initial evaluation notes; prefer upstream over digimata fork |
