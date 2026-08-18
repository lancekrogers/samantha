# Narration and audiobooks

[Back to README](../README.md)

`samantha render` turns documents into audio files and a manifest without the
live voice pipeline (no microphone). It reads text, Markdown, HTML, URL articles,
or EPUB, segments the text, synthesizes with the configured TTS, and always
writes WAV (the source of truth).

```bash
# Single file (format auto-detected from the extension; --stdin reads text):
samantha render article.md --out out/article.wav
cat notes.txt | samantha render --stdin --out out/notes.wav
samantha render https://example.com/post --out out/post.wav   # URL article

# Sectioned multi-file: one WAV per heading/section + a manifest.
# Works for Markdown, HTML, URL, and EPUB (EPUB requires --out-dir):
samantha render article.md --out-dir out/article
samantha render book.epub --out-dir out/book

# Optional compressed output via an external encoder (default ffmpeg); WAV is
# still written. A missing encoder fails before any synthesis:
samantha render book.epub --out-dir out/book --audio-format mp3

# Resume a long render: unchanged chapters/sections are skipped, changed/failed
# ones rebuild. --json prints completed/skipped/failed counts and exits non-zero
# if any unit failed, so scripts can branch:
samantha render book.epub --out-dir out/book --resume --json | jq '.failed'

# Optional planning controls (defaults preserve prior behavior):
samantha render article.md --out-dir out/article \
  --max-segment-chars 1200 --pause-heading 750ms --pause-paragraph 400ms \
  --code-blocks skip
```

## Audiobook creation

`samantha audiobook create` is a task-oriented wrapper over the same render
runtime for EPUB books and digital PDFs: one WAV per chapter (EPUB spine) or
page (PDF) plus a manifest under `--out-dir` (required). It accepts render's
pass-through flags (`--resume`, `--voice`, `--speed`, `--audio-format`,
`--language`, `--encoder`, `--json`, `--manifest`, `--overwrite`). Qwen accepts
`--voice` and `--language`; its pinned CustomVoice model does not support
`--speed`. Use `samantha render` for
markdown, HTML, URL (including sectioned `--out-dir`), and text sources.

Before synthesis, build a reviewable production plan. This writes the
extracted sections, a YAML source of truth, and a Markdown preview; it does
not load TTS or create audio:

```bash
samantha audiobook plan book.epub --out-dir out/book
samantha audiobook review out/book/production-plan.yaml
# Apply explicit human decisions when needed:
samantha audiobook review out/book/production-plan.yaml --exclude contents --exclude body --reason "navigation/front matter"
```

The plan classifies likely navigation, index, front matter, main content,
reference, and back matter. Ambiguous sections remain `review` until a human
decides. Rendering from the approved plan will be added in a follow-up slice;
the current `create` command remains the direct raw-spine compatibility path.

```bash
samantha audiobook create book.epub --out-dir out/book
samantha audiobook create book.epub --out-dir out/book --voice Ryan --language English
samantha audiobook create book.epub --out-dir out/book --audio-format m4b --resume --json
# From Calibre library (requires calibre_enabled=true and Calibre installed):
samantha config calibre_enabled true
samantha library list
samantha library search "cryptography"
samantha library show 42
samantha audiobook create --from-library "Crypto 101" --out-dir out/crypto
```

## Calibre library (optional)

[Calibre](https://calibre-ebook.com) is free software that organizes ebooks on
your computer (EPUB, PDF, MOBI, …). You do **not** need it for voice chat.
Samantha can browse a Calibre library so you can pick books for audiobooks or
ask what titles you own.

**Typical setup (most users):**

1. Install Calibre and open it once so it creates your library:
   - macOS: `brew install --cask calibre`
   - Arch Linux: `sudo pacman -S calibre`
   - Debian/Ubuntu: `sudo apt install calibre`
   - Windows or other platforms: use the installer from calibre-ebook.com
2. Enable the integration: `samantha config calibre_enabled true`
   (or open **Library** in the TUI and press **e**).
3. Samantha finds `calibredb` on `PATH`, or in the macOS app bundle
   (`/Applications/calibre.app/Contents/MacOS/`), or `/opt/calibre` on Linux.
   Your default Calibre library is used when `calibre_library_path` is empty.

**If something is non-default:**

| Situation | Config |
|-----------|--------|
| Library not at Calibre’s default path | `calibre_library_path` |
| `calibredb` not found automatically | `calibredb_binary` (full path) |
| Prefer PDF over EPUB when both exist | `calibre_prefer_format pdf` |

`samantha doctor` reports `calibre-binary` as a **Warn** when missing (never a
hard failure). Voice and other features keep working.

In the TUI, open **Library** from the launcher to browse the catalog, search,
and view book details. From a book press **enter** or **a** to send an
EPUB/PDF path into **Create audiobook**; MOBI/AZW-family books are converted
to a cached EPUB with Calibre's `ebook-convert`. The audiobook screen's **Pick
from library** opens with a browsable catalog, and `/` switches to search.
Direct audiobook rendering still consumes EPUB/PDF after any library
conversion.

## Narrate pipeline (prompt-controlled)

```bash
samantha narrate plan article.md --out narration.plan.yaml
samantha narrate prepare narration.plan.yaml --resume
samantha narrate render narration.plan.yaml --resume
samantha narrate plan book.pdf --out out/book.plan.yaml   # requires pdftotext (Poppler)
```

Digital PDFs also work with direct render / audiobook create:

```bash
samantha render book.pdf --out-dir out/book
samantha audiobook create book.pdf --out-dir out/book
```
