package tui

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/samantha/internal/calibre"
	"github.com/lancekrogers/samantha/internal/config"
)

// Focus regions inside the library browser.
const (
	libFocusQuery = iota
	libFocusList
)

// library pane modes: browse list vs detail viewer.
const (
	libPaneBrowse = iota
	libPaneDetail
)

// libraryResultsMsg folds an async browse/search into the model.
type libraryResultsMsg struct {
	requestID uint64
	books     []calibre.Book
	err       error
	browsed   bool // true when this was an unfiltered List
}

// libraryDetailMsg folds async Metadata into the detail pane.
type libraryDetailMsg struct {
	requestID uint64
	book      calibre.Book
	err       error
}

// libraryAudiobookMsg jumps to Create audiobook with a resolved input path.
type libraryAudiobookMsg struct {
	path string
}

type libraryModel struct {
	cfg       *config.Config
	client    calibre.Client
	width     int
	height    int
	pane      int // libPaneBrowse or libPaneDetail
	query     string
	editing   bool
	editBuf   string
	focus     int // libFocusQuery or libFocusList
	books     []calibre.Book
	cursor    int
	offset    int
	loading   bool
	errText   string
	message   string
	detail    calibre.Book
	detailOK  bool
	detailErr string
	requestID uint64
}

var libraryRequestSequence atomic.Uint64

func nextLibraryRequestID() uint64 {
	return libraryRequestSequence.Add(1)
}

func newLibrary(cfg *config.Config) libraryModel {
	m := libraryModel{
		cfg:     cfg,
		editing: false,
		focus:   libFocusList,
		pane:    libPaneBrowse,
	}
	if cfg != nil {
		m.client = calibre.NewClientFromConfig(
			cfg.CalibreEnabled,
			cfg.CalibreLibraryPath,
			cfg.CalibredbBinary,
			cfg.CalibreConvertBinary,
			cfg.CalibrePreferFormat,
		)
	}
	return m
}

func (m libraryModel) enabled() bool {
	return m.cfg != nil && m.cfg.CalibreEnabled
}

// InitCmd returns the initial browse load when Calibre is enabled.
func (m *libraryModel) InitCmd() tea.Cmd {
	if !m.enabled() {
		return nil
	}
	return m.runBrowse()
}

func (m libraryModel) Update(msg tea.Msg) (libraryModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureVisible()
	case libraryResultsMsg:
		if msg.requestID != m.requestID {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.errText = msg.err.Error()
			m.books = nil
			m.message = ""
			m.focus = libFocusQuery
			return m, nil
		}
		m.errText = ""
		m.books = msg.books
		m.cursor = 0
		m.offset = 0
		if len(m.books) == 0 {
			if msg.browsed {
				m.message = "Library is empty (or nothing returned)."
			} else {
				m.message = "No books matched."
			}
			m.focus = libFocusQuery
		} else {
			if msg.browsed {
				m.message = fmt.Sprintf("%d book(s) · title order", len(m.books))
			} else {
				m.message = fmt.Sprintf("%d result(s)", len(m.books))
			}
			m.focus = libFocusList
			m.editing = false
		}
		m.ensureVisible()
	case libraryDetailMsg:
		if msg.requestID != m.requestID {
			return m, nil
		}
		m.loading = false
		if msg.err != nil {
			m.detailErr = msg.err.Error()
			m.detailOK = false
			// Still show list-row data if we have it.
			return m, nil
		}
		m.detail = msg.book
		m.detailOK = true
		m.detailErr = ""
		m.pane = libPaneDetail
	case tea.KeyMsg:
		key := msg.String()
		if m.pane == libPaneDetail {
			return m.handleDetailKey(key)
		}
		if m.editing && m.focus == libFocusQuery {
			return m.handleQueryEdit(key)
		}
		switch key {
		case "up", "k":
			if m.focus == libFocusList && m.cursor > 0 {
				m.cursor--
				m.ensureVisible()
			}
		case "down", "j":
			if m.focus == libFocusList && m.cursor < len(m.books)-1 {
				m.cursor++
				m.ensureVisible()
			}
		case "home", "g":
			if m.focus == libFocusList {
				m.cursor = 0
				m.ensureVisible()
			}
		case "end", "G":
			if m.focus == libFocusList && len(m.books) > 0 {
				m.cursor = len(m.books) - 1
				m.ensureVisible()
			}
		case "/":
			m.focus = libFocusQuery
			m.editing = true
			m.editBuf = m.query
		case "enter":
			if m.focus == libFocusQuery {
				m.query = strings.TrimSpace(m.editBuf)
				m.editing = false
				return m, m.runQuery()
			}
			if m.focus == libFocusList && len(m.books) > 0 {
				return m.openDetail()
			}
		case "a":
			if m.focus == libFocusList && len(m.books) > 0 {
				return m.sendToAudiobook(m.books[m.cursor])
			}
		case "r":
			// Reload browse or re-run search.
			return m, m.runQuery()
		case "esc":
			return m, func() tea.Msg { return switchScreenMsg(screenLauncher) }
		case "q":
			return m, func() tea.Msg { return quitMsg{} }
		default:
			if m.focus == libFocusQuery && isEditableInsert(key) {
				m.editing = true
				m.editBuf = m.query + key
			}
		}
	}
	return m, nil
}

func (m libraryModel) handleDetailKey(key string) (libraryModel, tea.Cmd) {
	switch key {
	case "esc", "backspace":
		m.requestID = nextLibraryRequestID()
		m.pane = libPaneBrowse
		m.detailOK = false
		m.detailErr = ""
		return m, nil
	case "enter", "a":
		if m.detailOK {
			return m.sendToAudiobook(m.detail)
		}
		if m.cursor >= 0 && m.cursor < len(m.books) {
			return m.sendToAudiobook(m.books[m.cursor])
		}
	case "q":
		return m, func() tea.Msg { return quitMsg{} }
	}
	return m, nil
}

func (m libraryModel) handleQueryEdit(key string) (libraryModel, tea.Cmd) {
	switch key {
	case "enter":
		m.query = strings.TrimSpace(m.editBuf)
		m.editing = false
		return m, m.runQuery()
	case "esc":
		m.editing = false
		m.editBuf = m.query
		if len(m.books) > 0 {
			m.focus = libFocusList
		}
		return m, nil
	case "backspace", "ctrl+h":
		if len(m.editBuf) > 0 {
			_, size := utf8.DecodeLastRuneInString(m.editBuf)
			m.editBuf = m.editBuf[:len(m.editBuf)-size]
		}
	case "down", "tab":
		if len(m.books) > 0 {
			m.editing = false
			m.query = strings.TrimSpace(m.editBuf)
			m.focus = libFocusList
		}
	default:
		if isEditableInsert(key) {
			m.editBuf += key
		}
	}
	return m, nil
}

func (m *libraryModel) runBrowse() tea.Cmd {
	if !m.enabled() {
		m.errText = "Calibre is off — enable with: samantha config calibre_enabled true"
		return nil
	}
	m.loading = true
	m.errText = ""
	m.message = "Loading library…"
	client := m.client
	requestID := nextLibraryRequestID()
	m.requestID = requestID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		books, err := client.List(ctx, 100)
		return libraryResultsMsg{requestID: requestID, books: books, err: err, browsed: true}
	}
}

func (m *libraryModel) runQuery() tea.Cmd {
	q := strings.TrimSpace(m.query)
	if q == "" {
		return m.runBrowse()
	}
	if !m.enabled() {
		m.errText = "Calibre is off — enable with: samantha config calibre_enabled true"
		return nil
	}
	m.loading = true
	m.errText = ""
	m.message = "Searching…"
	client := m.client
	requestID := nextLibraryRequestID()
	m.requestID = requestID
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		books, err := client.Search(ctx, q, 100)
		return libraryResultsMsg{requestID: requestID, books: books, err: err, browsed: false}
	}
}

func (m libraryModel) openDetail() (libraryModel, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.books) {
		return m, nil
	}
	book := m.books[m.cursor]
	// Show list-row data immediately; refresh with Metadata when available.
	m.detail = book
	m.detailOK = true
	m.detailErr = ""
	m.pane = libPaneDetail
	m.loading = true
	client := m.client
	id := book.ID
	requestID := nextLibraryRequestID()
	m.requestID = requestID
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		full, err := client.Metadata(ctx, id)
		return libraryDetailMsg{requestID: requestID, book: full, err: err}
	}
}

func (m libraryModel) sendToAudiobook(b calibre.Book) (libraryModel, tea.Cmd) {
	path, _, err := m.client.BestFormatPath(b)
	if err != nil {
		if m.pane == libPaneDetail {
			m.detailErr = err.Error()
		} else {
			m.errText = err.Error()
		}
		return m, nil
	}
	return m, func() tea.Msg { return libraryAudiobookMsg{path: path} }
}

func (m *libraryModel) ensureVisible() {
	visible := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
	maxOffset := max(len(m.books)-visible, 0)
	m.offset = min(max(m.offset, 0), maxOffset)
}

func (m libraryModel) visibleRows() int {
	if m.height > 0 && m.height < 12 {
		return max(m.height-6, 1)
	}
	return max(m.height-11, 3)
}

func (m libraryModel) View() string {
	if m.pane == libPaneDetail {
		return m.detailView()
	}
	return m.browseView()
}

func (m libraryModel) browseView() string {
	var b strings.Builder
	width := m.width
	if width <= 0 {
		width = 80
	}
	b.WriteString(ansi.Truncate(titleStyle.Render("  Calibre library"), width, "…"))
	b.WriteString("\n")
	b.WriteString(ansi.Truncate(subtitleStyle.Render("  Browse · search · view details · send to audiobook"), width, "…"))
	b.WriteString("\n\n")

	if !m.enabled() {
		b.WriteString(errorStyle.Render("  Calibre is off — enable with: samantha config calibre_enabled true"))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render(ansi.Truncate("  esc back", width, "…")))
		return b.String()
	}

	qLabel := "Query"
	qVal := m.query
	if m.editing && m.focus == libFocusQuery {
		qVal = m.editBuf + "█"
	} else if strings.TrimSpace(qVal) == "" {
		qVal = "(empty = browse all · type to filter)"
	}
	qCursor, qStyle := "  ", normalStyle
	if m.focus == libFocusQuery {
		qCursor, qStyle = "▸ ", selectedStyle
	}
	b.WriteString("  " + qCursor + qStyle.Render(fmt.Sprintf("%-8s %s", qLabel, qVal)) + "\n")

	if m.loading {
		b.WriteString(dimStyle.Render("  Loading…"))
		b.WriteString("\n")
	}

	if len(m.books) == 0 {
		if !m.loading && m.message != "" && m.errText == "" {
			b.WriteString(dimStyle.Render("  " + m.message))
			b.WriteString("\n")
		}
	} else {
		b.WriteString("\n")
		visible := m.visibleRows()
		end := min(m.offset+visible, len(m.books))
		for i := m.offset; i < end; i++ {
			book := m.books[i]
			cursor, style := "  ", normalStyle
			if i == m.cursor && m.focus == libFocusList {
				cursor, style = "▸ ", selectedStyle
			}
			authors := strings.Join(book.Authors, ", ")
			if authors == "" {
				authors = "unknown"
			}
			fmts := formatExtList(book.Formats)
			line := fmt.Sprintf("%s — %s  [%s]", book.Title, authors, fmts)
			maxWidth := max(width-6, 1)
			line = ansi.Truncate(line, maxWidth, "…")
			b.WriteString("  " + cursor + style.Render(line) + "\n")
		}
	}

	if m.errText != "" {
		b.WriteString("\n")
		b.WriteString(errorStyle.Render("  " + m.errText))
		b.WriteString("\n")
	} else if m.message != "" && !m.loading && len(m.books) > 0 {
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  " + m.message))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render(ansi.Truncate(
		"  enter view · / search · a audiobook · r reload · ↑/↓ list · esc back",
		width, "…",
	)))
	return b.String()
}

func (m libraryModel) detailView() string {
	var b strings.Builder
	width := m.width
	if width <= 0 {
		width = 80
	}
	book := m.detail
	b.WriteString(ansi.Truncate(titleStyle.Render("  Book details"), width, "…"))
	b.WriteString("\n")
	if m.loading {
		b.WriteString(ansi.Truncate(subtitleStyle.Render("  Loading full metadata…"), width, "…"))
	} else {
		b.WriteString(ansi.Truncate(subtitleStyle.Render("  Metadata from Calibre"), width, "…"))
	}
	b.WriteString("\n\n")

	authors := strings.Join(book.Authors, ", ")
	if authors == "" {
		authors = "(unknown author)"
	}

	writeField := func(label, val string) {
		if strings.TrimSpace(val) == "" {
			return
		}
		line := fmt.Sprintf("  %-10s %s", label, val)
		b.WriteString(ansi.Truncate(line, width, "…"))
		b.WriteString("\n")
	}

	writeField("ID", fmt.Sprintf("%d", book.ID))
	writeField("Title", book.Title)
	writeField("Authors", authors)
	writeField("Series", book.Series)
	if len(book.Tags) > 0 {
		writeField("Tags", strings.Join(book.Tags, ", "))
	}
	if book.PubDate != "" {
		// Truncate ISO timestamps for display.
		pub := book.PubDate
		if i := strings.Index(pub, "T"); i > 0 {
			pub = pub[:i]
		}
		writeField("Published", pub)
	}
	writeField("Formats", formatExtList(book.Formats))
	for _, p := range book.Formats {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
		line := fmt.Sprintf("             · %s  %s", ext, p)
		b.WriteString(ansi.Truncate(dimStyle.Render(line), width, "…"))
		b.WriteString("\n")
	}

	if blurb := calibre.PlainComments(book.Comments); blurb != "" {
		b.WriteString("\n")
		b.WriteString(ansi.Truncate("  Description", width, "…"))
		b.WriteString("\n")
		// Wrap blurb simply by truncating long lines.
		const maxBlurb = 600
		if len(blurb) > maxBlurb {
			blurb = blurb[:maxBlurb] + "…"
		}
		for _, para := range wrapWords(blurb, max(width-4, 20)) {
			b.WriteString(ansi.Truncate(dimStyle.Render("  "+para), width, "…"))
			b.WriteString("\n")
		}
	}

	if m.detailErr != "" {
		b.WriteString("\n")
		b.WriteString(errorStyle.Render("  " + m.detailErr))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render(ansi.Truncate(
		"  enter/a send to audiobook · esc back to list · q quit",
		width, "…",
	)))
	return b.String()
}

func formatExtList(paths []string) string {
	if len(paths) == 0 {
		return "none"
	}
	exts := make([]string, 0, len(paths))
	for _, p := range paths {
		ext := strings.ToLower(strings.TrimPrefix(filepath.Ext(p), "."))
		if ext == "" {
			ext = "?"
		}
		exts = append(exts, ext)
	}
	return strings.Join(exts, ", ")
}

// wrapWords soft-wraps s to width runes on word boundaries.
func wrapWords(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	words := strings.Fields(s)
	if len(words) == 0 {
		return nil
	}
	var lines []string
	var cur strings.Builder
	for _, w := range words {
		if cur.Len() == 0 {
			cur.WriteString(w)
			continue
		}
		if cur.Len()+1+len(w) > width {
			lines = append(lines, cur.String())
			cur.Reset()
			cur.WriteString(w)
			continue
		}
		cur.WriteByte(' ')
		cur.WriteString(w)
	}
	if cur.Len() > 0 {
		lines = append(lines, cur.String())
	}
	return lines
}
