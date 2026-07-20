---
name: calibre
description: Search and answer questions about the user's Calibre ebook library (titles, authors, tags, formats). Use when the user asks what books they have, who wrote something, tags/topics, or to find a book path for audiobook creation.
---

# Calibre library skill

Answer questions about the user's **local Calibre ebook library**. Prefer
metadata search over reading whole books. Excerpts must stay short.

## Prerequisites

- `calibre_enabled=true` in Samantha config (`samantha config calibre_enabled true`).
- Calibre installed (`calibredb` on PATH or in the macOS app bundle).
- Optional: `calibre_library_path` if not using Calibre's default library.

## How to query (fixed argv — no shell interpolation of user text)

Use the Samantha CLI (preferred) or `calibredb` with fixed arguments:

```bash
# Browse catalog (no filter; title order)
samantha library list --limit 50

# Search catalog (Calibre search grammar: free text, author:"…", tag:…, series:…)
samantha library search "QUERY" --limit 20
samantha library search "QUERY" --json

# One book’s metadata (description, formats, tags)
samantha library show ID

# Or call calibredb directly when samantha library is unavailable:
# calibredb list --for-machine --fields title,authors,tags,series,formats,pubdate \
#   --search "QUERY" --limit 20
```

For a single book id (when `library show` is unavailable):

```bash
calibredb list --for-machine --fields all --search "id:N" --limit 1
```

Full-text content search (only if the library FTS index is enabled):

```bash
calibredb fts_search "PHRASE" --output-format json --include-snippets
```

If FTS is disabled, say so and fall back to title/author/tag search. Do **not**
run `fts_index enable` unless the user explicitly asks.

## Answering guidelines

1. **Catalog questions** ("what books on X", "do I have Norvig"): use
   `samantha library search` (or `list` for unfiltered browse) and summarize
   title, author, id, available formats.
2. **Detail on one book**: `samantha library show ID` (or search → pick id) for
   title, authors, tags, series, pubdate, formats. Quote comments/description
   only if present and keep under ~500 characters.
3. **Content / quote search**: try FTS; if it fails or is empty, explain and
   offer metadata search instead. Never dump a whole book into the reply.
4. **Audiobook path**: resolve to an EPUB or PDF path. v1 does not convert
   MOBI/AZW3 — if those are the only formats, say so clearly.
5. Cap results at ~20 titles unless the user asks for more. Prefer the most
   relevant hits.

## Safety

- Pass the user query as a **single argument** to `--search` / the CLI — never
  build shell strings with unescaped user text.
- Do not modify the library (no add/remove/set_metadata) unless the user
  explicitly requests a write and you confirm.
- The feature is optional: if Calibre is disabled or missing, tell the user how
  to enable it (`samantha config calibre_enabled true` and install Calibre).
