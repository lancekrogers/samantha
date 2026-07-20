// Package calibre resolves books from a Calibre library via calibredb.
//
// The client is opt-in (gated by calibre_enabled config) and injectable for
// tests: no real Calibre binary is required in CI. Supports browse (List),
// search, metadata, and path resolution. EPUB/PDF only for v1 audiobook
// paths; MOBI/AZW3-only books return ErrNoSupportedFormat.
package calibre

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

// Sentinel errors for common resolution outcomes.
var (
	// ErrCalibreNotFound means calibredb could not be located on PATH or in
	// known application-bundle locations.
	ErrCalibreNotFound = errors.New("calibre: calibredb not found")
	// ErrNoSupportedFormat means the book has no EPUB or PDF format (v1).
	ErrNoSupportedFormat = errors.New("calibre: no supported format (need epub or pdf)")
	// ErrFormatMissing means Calibre listed a supported format whose file is
	// no longer present on disk.
	ErrFormatMissing = errors.New("calibre: supported format file missing")
	// ErrAmbiguous means Resolve found multiple candidates and needs a tighter query.
	ErrAmbiguous = errors.New("calibre: ambiguous query")
	// ErrNotFound means no books matched the query.
	ErrNotFound = errors.New("calibre: no books matched")
	// ErrDisabled means the integration is turned off in config.
	ErrDisabled = errors.New("calibre: disabled (set calibre_enabled=true)")
)

// Book is one library entry from calibredb --for-machine JSON.
type Book struct {
	ID       int      `json:"id"`
	Title    string   `json:"title"`
	Authors  []string `json:"authors"`
	Series   string   `json:"series"`
	Tags     []string `json:"tags"`
	Formats  []string `json:"formats"` // absolute file paths
	PubDate  string   `json:"pubdate"`
	Comments string   `json:"comments,omitempty"`
}

// ftsHit is kept private until a CLI/TUI surface needs full-text search.
type ftsHit struct {
	BookID  int    `json:"book_id"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
}

// Runner executes an external command. Tests inject fakes; production uses exec.
type Runner func(ctx context.Context, name string, args ...string) (stdout []byte, err error)

// Client shells out to calibredb (and optionally ebook-convert).
// All fields are optional; zero-value Client is usable with defaults.
type Client struct {
	// LookPath locates a binary (default: BundleLookPath).
	LookPath func(string) (string, error)
	// Run executes the binary (default: exec.CommandContext with stderr capture).
	Run Runner
	// Binary is the calibredb name or absolute path (default "calibredb").
	// When empty, LookPath is used with "calibredb".
	Binary string
	// ConvBinary is ebook-convert name/path (default "ebook-convert").
	ConvBinary string
	// LibraryPath is passed as --with-library when non-empty.
	LibraryPath string
	// Prefer is the preferred format extension, e.g. "epub" (default "epub").
	Prefer string
	// SearchTimeout bounds Search/Resolve when the parent context has no deadline.
	SearchTimeout time.Duration
}

// defaultSearchTimeout is applied when Search/Resolve contexts have no deadline.
const defaultSearchTimeout = 30 * time.Second

// NewClientFromConfig builds a Client from Samantha config fields.
// Binary overrides (non-empty) skip LookPath for that binary.
func NewClientFromConfig(calibreEnabled bool, libraryPath, calibredbBinary, convertBinary, prefer string) Client {
	c := Client{
		LibraryPath: libraryPath,
		Binary:      strings.TrimSpace(calibredbBinary),
		ConvBinary:  strings.TrimSpace(convertBinary),
		Prefer:      strings.TrimSpace(prefer),
	}
	if c.Prefer == "" {
		c.Prefer = "epub"
	}
	_ = calibreEnabled // caller gates; kept for signature clarity at call sites
	return c
}

// BundleLookPath finds name on PATH, then in known Calibre install locations.
func BundleLookPath(name string) (string, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", fmt.Errorf("%w: empty binary name", ErrCalibreNotFound)
	}
	// Absolute or relative path with separators: use as-is if it exists.
	if strings.Contains(name, string(os.PathSeparator)) || filepath.IsAbs(name) {
		if st, err := os.Stat(name); err == nil && !st.IsDir() {
			return name, nil
		}
		return "", fmt.Errorf("%w: %s", ErrCalibreNotFound, name)
	}
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	for _, dir := range bundleDirs() {
		candidate := filepath.Join(dir, name)
		if st, err := os.Stat(candidate); err == nil && !st.IsDir() {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("%w: %s not on PATH or in Calibre app bundle; install Calibre (macOS: brew install --cask calibre) or set calibredb_binary", ErrCalibreNotFound, name)
}

func bundleDirs() []string {
	var dirs []string
	if runtime.GOOS == "darwin" {
		dirs = append(dirs, "/Applications/calibre.app/Contents/MacOS")
	}
	dirs = append(dirs, "/opt/calibre")
	return dirs
}

func (c Client) look() func(string) (string, error) {
	if c.LookPath != nil {
		return c.LookPath
	}
	return BundleLookPath
}

func (c Client) runner() Runner {
	if c.Run != nil {
		return c.Run
	}
	return defaultRunner
}

func (c Client) calibredbName() string {
	if strings.TrimSpace(c.Binary) != "" {
		return strings.TrimSpace(c.Binary)
	}
	return "calibredb"
}

func (c Client) preferFormat() string {
	p := strings.ToLower(strings.TrimSpace(c.Prefer))
	if p == "" {
		return "epub"
	}
	return p
}

func (c Client) resolveBinary(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	name := c.calibredbName()
	// If Binary is an absolute path that exists, use it without LookPath.
	if filepath.IsAbs(name) {
		if st, err := os.Stat(name); err == nil && !st.IsDir() {
			return name, nil
		}
	}
	p, err := c.look()(name)
	if err != nil {
		if errors.Is(err, ErrCalibreNotFound) {
			return "", err
		}
		return "", fmt.Errorf("%w: %v", ErrCalibreNotFound, err)
	}
	return p, nil
}

// List browses the library without a search filter (catalog order by title).
// limit <= 0 defaults to 50. Use Search for filtered queries.
func (c Client) List(ctx context.Context, limit int) ([]Book, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if limit <= 0 {
		limit = 50
	}
	return c.list(ctx, "", limit, "title")
}

// Search runs calibredb list --for-machine for query and returns parsed books.
// limit <= 0 defaults to 20.
func (c Client) Search(ctx context.Context, query string, limit int) ([]Book, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	query = strings.TrimSpace(query)
	if query == "" {
		return nil, fmt.Errorf("calibre: empty search query")
	}
	if limit <= 0 {
		limit = 20
	}
	return c.list(ctx, query, limit, "")
}

// list runs calibredb list --for-machine. Empty query omits --search (browse).
// sortBy, when non-empty, is passed as --sort-by (e.g. "title").
func (c Client) list(ctx context.Context, query string, limit int, sortBy string) ([]Book, error) {
	ctx, cancel := withSearchTimeout(ctx, c.SearchTimeout)
	defer cancel()

	bin, err := c.resolveBinary(ctx)
	if err != nil {
		return nil, err
	}
	args := []string{
		"list",
		"--for-machine",
		"--fields", "title,authors,tags,series,formats,pubdate",
		"--limit", fmt.Sprintf("%d", limit),
	}
	if q := strings.TrimSpace(query); q != "" {
		args = append(args, "--search", q)
	}
	if s := strings.TrimSpace(sortBy); s != "" {
		args = append(args, "--sort-by", s, "--ascending")
	}
	if lib := strings.TrimSpace(c.LibraryPath); lib != "" {
		args = append(args, "--with-library", lib)
	}
	out, err := c.runner()(ctx, bin, args...)
	if err != nil {
		if query != "" {
			return nil, fmt.Errorf("calibre: search %q: %w", query, err)
		}
		return nil, fmt.Errorf("calibre: list: %w", err)
	}
	books, err := parseListJSON(out)
	if err != nil {
		return nil, fmt.Errorf("calibre: parse list results: %w", err)
	}
	return books, nil
}

// Resolve searches and returns a single unambiguous book.
// Zero hits → ErrNotFound; multiple → ErrAmbiguous with candidates listed.
func (c Client) Resolve(ctx context.Context, query string) (Book, error) {
	books, err := c.Search(ctx, query, 10)
	if err != nil {
		return Book{}, err
	}
	switch len(books) {
	case 0:
		return Book{}, fmt.Errorf("%w: %q", ErrNotFound, query)
	case 1:
		return books[0], nil
	default:
		// Exact title match (case-insensitive) collapses ambiguity.
		q := strings.ToLower(strings.TrimSpace(query))
		var exact []Book
		for _, b := range books {
			if strings.ToLower(strings.TrimSpace(b.Title)) == q {
				exact = append(exact, b)
			}
		}
		if len(exact) == 1 {
			return exact[0], nil
		}
		return Book{}, fmt.Errorf("%w: %q matches %d books (%s)", ErrAmbiguous, query, len(books), summarizeTitles(books, 5))
	}
}

// BestFormatPath picks the preferred absolute format path for b.
// Preference: configured Prefer (default epub), then epub, then pdf.
// Returns ErrNoSupportedFormat when only MOBI/AZW3/etc. are present.
func (c Client) BestFormatPath(b Book) (path, format string, err error) {
	if len(b.Formats) == 0 {
		return "", "", fmt.Errorf("%w: book %d %q has no formats", ErrNoSupportedFormat, b.ID, b.Title)
	}
	prefer := c.preferFormat()
	order := []string{prefer}
	for _, f := range []string{"epub", "pdf"} {
		if f != prefer {
			order = append(order, f)
		}
	}
	byExt := map[string]string{}
	for _, p := range b.Formats {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
		if ext == "" {
			continue
		}
		// Keep first path per extension.
		if _, ok := byExt[ext]; !ok {
			byExt[ext] = p
		}
	}
	listedSupported := false
	for _, want := range order {
		if p, ok := byExt[want]; ok {
			listedSupported = true
			if st, statErr := os.Stat(p); statErr == nil && !st.IsDir() {
				return p, want, nil
			}
		}
	}
	if listedSupported {
		return "", "", fmt.Errorf("%w: book %d %q lists an EPUB/PDF path that is unavailable", ErrFormatMissing, b.ID, b.Title)
	}
	return "", "", fmt.Errorf("%w: book %d %q has %v (v1 supports epub/pdf only)", ErrNoSupportedFormat, b.ID, b.Title, formatList(b.Formats))
}

// Metadata fetches full-ish metadata for one book id via calibredb list.
func (c Client) Metadata(ctx context.Context, id int) (Book, error) {
	if err := ctx.Err(); err != nil {
		return Book{}, err
	}
	if id <= 0 {
		return Book{}, fmt.Errorf("calibre: invalid book id %d", id)
	}
	ctx, cancel := withSearchTimeout(ctx, c.SearchTimeout)
	defer cancel()

	bin, err := c.resolveBinary(ctx)
	if err != nil {
		return Book{}, err
	}
	// Search by id: is a precise calibredb search grammar term.
	args := []string{
		"list",
		"--for-machine",
		"--fields", "all",
		"--search", fmt.Sprintf("id:%d", id),
		"--limit", "1",
	}
	if lib := strings.TrimSpace(c.LibraryPath); lib != "" {
		args = append(args, "--with-library", lib)
	}
	out, err := c.runner()(ctx, bin, args...)
	if err != nil {
		return Book{}, fmt.Errorf("calibre: metadata id=%d: %w", id, err)
	}
	books, err := parseListJSON(out)
	if err != nil {
		return Book{}, fmt.Errorf("calibre: parse metadata: %w", err)
	}
	if len(books) == 0 {
		return Book{}, fmt.Errorf("%w: id %d", ErrNotFound, id)
	}
	return books[0], nil
}

// fullTextSearch runs calibredb fts_search for a future library search
// surface. When FTS is disabled or unavailable, it returns a wrapped error so
// that surface can fall back to Search.
func (c Client) fullTextSearch(ctx context.Context, phrase string, limit int) ([]ftsHit, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	phrase = strings.TrimSpace(phrase)
	if phrase == "" {
		return nil, fmt.Errorf("calibre: empty fts phrase")
	}
	if limit <= 0 {
		limit = 10
	}
	ctx, cancel := withSearchTimeout(ctx, c.SearchTimeout)
	defer cancel()

	bin, err := c.resolveBinary(ctx)
	if err != nil {
		return nil, err
	}
	args := []string{
		"fts_search",
		"--output-format", "json",
		"--include-snippets",
		phrase,
	}
	if lib := strings.TrimSpace(c.LibraryPath); lib != "" {
		args = append(args, "--with-library", lib)
	}
	out, err := c.runner()(ctx, bin, args...)
	if err != nil {
		return nil, fmt.Errorf("calibre: fts_search: %w", err)
	}
	hits, err := parseFTSJSON(out)
	if err != nil {
		return nil, fmt.Errorf("calibre: parse fts results: %w", err)
	}
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, nil
}

// summarizeTitles formats up to n candidate titles for an ambiguity error.
func summarizeTitles(books []Book, n int) string {
	if n <= 0 || n > len(books) {
		n = len(books)
	}
	parts := make([]string, 0, n)
	for i := 0; i < n; i++ {
		parts = append(parts, fmt.Sprintf("%d:%q", books[i].ID, books[i].Title))
	}
	if len(books) > n {
		parts = append(parts, "…")
	}
	return strings.Join(parts, ", ")
}

func formatList(paths []string) []string {
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
		if ext == "" {
			ext = filepath.Base(p)
		}
		out = append(out, ext)
	}
	return out
}

func withSearchTimeout(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	if d <= 0 {
		d = defaultSearchTimeout
	}
	return context.WithTimeout(ctx, d)
}

func defaultRunner(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg != "" {
			return nil, fmt.Errorf("%w: %s", err, msg)
		}
		return nil, err
	}
	return stdout.Bytes(), nil
}

// rawBook is the JSON shape from calibredb --for-machine (authors/tags may be
// string or array depending on version/fields).
type rawBook struct {
	ID       int             `json:"id"`
	Title    string          `json:"title"`
	Authors  json.RawMessage `json:"authors"`
	Series   string          `json:"series"`
	Tags     json.RawMessage `json:"tags"`
	Formats  json.RawMessage `json:"formats"`
	PubDate  string          `json:"pubdate"`
	Comments string          `json:"comments"`
}

func parseListJSON(data []byte) ([]Book, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	var raw []rawBook
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, err
	}
	out := make([]Book, 0, len(raw))
	for _, r := range raw {
		b := Book{
			ID:       r.ID,
			Title:    r.Title,
			Series:   r.Series,
			PubDate:  r.PubDate,
			Comments: r.Comments,
			Authors:  parseStringOrList(r.Authors),
			Tags:     parseStringOrList(r.Tags),
			Formats:  parseStringOrList(r.Formats),
		}
		out = append(out, b)
	}
	return out, nil
}

func parseStringOrList(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var list []string
	if err := json.Unmarshal(raw, &list); err == nil {
		return list
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		s = strings.TrimSpace(s)
		if s == "" {
			return nil
		}
		// authors often come as a single space-joined string from calibredb.
		// Keep as one element when no " & " separator; split on " & " when present.
		if strings.Contains(s, " & ") {
			return splitAndTrim(s, " & ")
		}
		return []string{s}
	}
	return nil
}

// PlainComments strips HTML tags from Calibre book comments for TUI/CLI display.
// Calibre stores descriptions as HTML; empty input returns "".
func PlainComments(html string) string {
	html = strings.TrimSpace(html)
	if html == "" {
		return ""
	}
	// Cheap tag strip — good enough for terminal display of short blurbs.
	var b strings.Builder
	b.Grow(len(html))
	inTag := false
	for _, r := range html {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			b.WriteRune(r)
		}
	}
	s := strings.TrimSpace(b.String())
	// Collapse whitespace runs from block tags.
	fields := strings.Fields(s)
	return strings.Join(fields, " ")
}

func splitAndTrim(s, sep string) []string {
	parts := strings.Split(s, sep)
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

func parseFTSJSON(data []byte) ([]ftsHit, error) {
	data = bytes.TrimSpace(data)
	if len(data) == 0 {
		return nil, nil
	}
	// calibredb fts_search JSON shapes vary; accept an array of objects with
	// common field names.
	var generic []map[string]any
	if err := json.Unmarshal(data, &generic); err != nil {
		// Sometimes a wrapper object.
		var wrap struct {
			Results []map[string]any `json:"results"`
		}
		if err2 := json.Unmarshal(data, &wrap); err2 != nil {
			return nil, err
		}
		generic = wrap.Results
	}
	hits := make([]ftsHit, 0, len(generic))
	for _, m := range generic {
		h := ftsHit{
			Title:   stringField(m, "title", "book_title"),
			Snippet: stringField(m, "snippet", "text", "match"),
		}
		h.BookID = intField(m, "book_id", "id", "book")
		hits = append(hits, h)
	}
	return hits, nil
}

func stringField(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case string:
				return t
			}
		}
	}
	return ""
}

func intField(m map[string]any, keys ...string) int {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch t := v.(type) {
			case float64:
				return int(t)
			case int:
				return t
			case json.Number:
				n, _ := t.Int64()
				return int(n)
			}
		}
	}
	return 0
}
