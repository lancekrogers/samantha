package tui

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"
	"time"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/samantha/internal/calibre"
	"github.com/lancekrogers/samantha/internal/config"
)

// Focus regions inside the pick-book screen.
const (
	pickFocusQuery = iota
	pickFocusList
)

// calibreResultsMsg folds an async library browse/search into the model.
type calibreResultsMsg struct {
	books   []calibre.Book
	err     error
	browsed bool
}

// bookPickedMsg carries a resolved audiobook input path back to the audiobook form.
type bookPickedMsg struct {
	path      string
	err       error
	requestID uint64
}

// pickLoadPhase separates search vs prepare so the status line is accurate.
type pickLoadPhase int

const (
	pickIdle pickLoadPhase = iota
	pickSearching
	pickPreparing
)

type pickBookModel struct {
	cfg       *config.Config
	client    calibre.Client
	width     int
	height    int
	query     string
	editing   bool
	editBuf   string
	focus     int // pickFocusQuery or pickFocusList
	books     []calibre.Book
	cursor    int
	offset    int
	loadPhase pickLoadPhase
	// requestID tags async selectBook work; stale results are ignored. IDs are
	// process-wide so reopening the picker cannot reuse an old request ID.
	requestID     uint64
	resolveCancel context.CancelFunc
	errText       string
	message       string
}

var pickBookRequestSequence atomic.Uint64

func nextPickBookRequestID() uint64 {
	return pickBookRequestSequence.Add(1)
}

func newPickBook(cfg *config.Config) pickBookModel {
	m := pickBookModel{
		cfg:   cfg,
		focus: pickFocusList,
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

func (m pickBookModel) busy() bool {
	return m.loadPhase != pickIdle
}

func (m pickBookModel) Update(msg tea.Msg) (pickBookModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureVisible()
	case calibreResultsMsg:
		m.loadPhase = pickIdle
		if msg.err != nil {
			m.errText = msg.err.Error()
			m.books = nil
			m.message = ""
			m.focus = pickFocusQuery
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
			m.focus = pickFocusQuery
		} else {
			if msg.browsed {
				m.message = fmt.Sprintf("%d book(s) · title order", len(m.books))
			} else {
				m.message = fmt.Sprintf("%d result(s)", len(m.books))
			}
			m.focus = pickFocusList
			m.editing = false
		}
		m.ensureVisible()
	case tea.KeyMsg:
		key := msg.String()
		// Block concurrent selects / searches while work is in flight.
		if m.busy() {
			switch key {
			case "esc":
				return m, func() tea.Msg { return switchScreenMsg(screenAudiobook) }
			case "q":
				return m, func() tea.Msg { return quitMsg{} }
			default:
				return m, nil
			}
		}
		if m.editing && m.focus == pickFocusQuery {
			return m.handleQueryEdit(key)
		}
		switch key {
		case "up", "k":
			if m.focus == pickFocusList && m.cursor > 0 {
				m.cursor--
				m.ensureVisible()
			}
		case "down", "j":
			if m.focus == pickFocusList && m.cursor < len(m.books)-1 {
				m.cursor++
				m.ensureVisible()
			}
		case "/":
			// Jump to query box.
			m.focus = pickFocusQuery
			m.editing = true
			m.editBuf = m.query
		case "b":
			if m.focus == pickFocusList {
				return m, m.runBrowse()
			}
		case "r":
			if m.focus == pickFocusList {
				return m, m.runSearch()
			}
		case "enter":
			if m.focus == pickFocusQuery {
				m.query = strings.TrimSpace(m.editBuf)
				m.editing = false
				return m, m.runSearch()
			}
			if m.focus == pickFocusList && len(m.books) > 0 {
				return m.selectBook()
			}
		case "esc":
			return m, func() tea.Msg { return switchScreenMsg(screenAudiobook) }
		case "q":
			return m, func() tea.Msg { return quitMsg{} }
		default:
			if m.focus == pickFocusQuery && isEditableInsert(key) {
				m.editing = true
				m.editBuf = m.query + key
			}
		}
	}
	return m, nil
}

func (m pickBookModel) handleQueryEdit(key string) (pickBookModel, tea.Cmd) {
	switch key {
	case "enter":
		m.query = strings.TrimSpace(m.editBuf)
		m.editing = false
		return m, m.runSearch()
	case "esc":
		m.editing = false
		m.editBuf = m.query
		return m, func() tea.Msg { return switchScreenMsg(screenAudiobook) }
	case "backspace", "ctrl+h":
		if len(m.editBuf) > 0 {
			_, size := utf8.DecodeLastRuneInString(m.editBuf)
			m.editBuf = m.editBuf[:len(m.editBuf)-size]
		}
	case "down", "tab":
		if len(m.books) > 0 {
			m.editing = false
			m.query = strings.TrimSpace(m.editBuf)
			m.focus = pickFocusList
		}
	default:
		if isEditableInsert(key) {
			m.editBuf += key
		}
	}
	return m, nil
}

func (m *pickBookModel) runSearch() tea.Cmd {
	q := strings.TrimSpace(m.query)
	if q == "" {
		return m.runBrowse()
	}
	m.loadPhase = pickSearching
	m.errText = ""
	m.message = "Searching…"
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		books, err := client.Search(ctx, q, 50)
		return calibreResultsMsg{books: books, err: err}
	}
}

func (m *pickBookModel) runBrowse() tea.Cmd {
	if m.cfg == nil || !m.cfg.CalibreEnabled {
		m.errText = "Calibre is off — enable with: samantha config calibre_enabled true"
		return nil
	}
	m.loadPhase = pickSearching
	m.errText = ""
	m.message = "Loading library…"
	client := m.client
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		books, err := client.List(ctx, 100)
		return calibreResultsMsg{books: books, err: err, browsed: true}
	}
}

func (m pickBookModel) selectBook() (pickBookModel, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.books) {
		return m, nil
	}
	if m.loadPhase == pickPreparing {
		return m, nil
	}
	b := m.books[m.cursor]
	client := m.client
	m.requestID = nextPickBookRequestID()
	reqID := m.requestID
	m.loadPhase = pickPreparing
	m.errText = ""
	m.message = "Preparing audiobook input…"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	m.resolveCancel = cancel
	return m, func() tea.Msg {
		defer cancel()
		path, _, err := client.BestFormatPathContext(ctx, b)
		return bookPickedMsg{path: path, err: err, requestID: reqID}
	}
}

func (m *pickBookModel) cancelResolve() {
	if m.resolveCancel != nil {
		m.resolveCancel()
		m.resolveCancel = nil
	}
	if m.loadPhase == pickPreparing {
		m.loadPhase = pickIdle
	}
}

func (m *pickBookModel) ensureVisible() {
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

func (m pickBookModel) visibleRows() int {
	if m.height > 0 && m.height < 12 {
		return max(m.height-6, 1)
	}
	return max(m.height-10, 3)
}

func (m pickBookModel) View() string {
	var b strings.Builder
	width := m.width
	if width <= 0 {
		width = 80
	}
	b.WriteString(ansi.Truncate(titleStyle.Render("  Pick from Calibre library"), width, "…"))
	b.WriteString("\n")
	b.WriteString(ansi.Truncate(subtitleStyle.Render("  Browse or search, then select a book (EPUB/PDF/MOBI)"), width, "…"))
	b.WriteString("\n\n")

	qLabel := "Query"
	qVal := m.query
	if m.editing && m.focus == pickFocusQuery {
		qVal = m.editBuf + "█"
	} else if strings.TrimSpace(qVal) == "" {
		qVal = "(press / to search; empty query browses)"
	}
	qCursor, qStyle := "  ", normalStyle
	if m.focus == pickFocusQuery {
		qCursor, qStyle = "▸ ", selectedStyle
	}
	b.WriteString("  " + qCursor + qStyle.Render(fmt.Sprintf("%-8s %s", qLabel, qVal)) + "\n")

	switch m.loadPhase {
	case pickSearching:
		status := m.message
		if status == "" {
			status = "Searching…"
		}
		b.WriteString(dimStyle.Render("  " + status))
		b.WriteString("\n")
	case pickPreparing:
		status := m.message
		if status == "" {
			status = "Preparing audiobook input…"
		}
		b.WriteString(dimStyle.Render("  " + status))
		b.WriteString("\n")
	}

	if len(m.books) == 0 {
		if m.loadPhase == pickIdle && m.message != "" && m.errText == "" {
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
			if i == m.cursor && m.focus == pickFocusList {
				cursor, style = "▸ ", selectedStyle
			}
			authors := strings.Join(book.Authors, ", ")
			if authors == "" {
				authors = "unknown"
			}
			line := fmt.Sprintf("%s — %s  [%s]", book.Title, authors, formatExtList(book.Formats))
			maxWidth := max(width-6, 1)
			line = ansi.Truncate(line, maxWidth, "…")
			b.WriteString("  " + cursor + style.Render(line) + "\n")
		}
	}

	if m.errText != "" {
		b.WriteString("\n")
		b.WriteString(errorStyle.Render("  " + m.errText))
		b.WriteString("\n")
	} else if m.message != "" && m.loadPhase == pickIdle && len(m.books) > 0 {
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  " + m.message))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render(ansi.Truncate("  enter select • / search • b browse • r reload • ↑/↓ • esc back", width, "…")))
	return b.String()
}
