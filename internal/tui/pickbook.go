package tui

import (
	"context"
	"fmt"
	"strings"
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

// calibreResultsMsg folds an async library search into the model.
type calibreResultsMsg struct {
	books []calibre.Book
	err   error
}

// bookPickedMsg carries a resolved EPUB/PDF path back to the audiobook form.
type bookPickedMsg struct {
	path string
}

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
	searching bool
	errText   string
	message   string
}

func newPickBook(cfg *config.Config) pickBookModel {
	m := pickBookModel{
		cfg:     cfg,
		editing: true,
		focus:   pickFocusQuery,
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

func (m pickBookModel) Update(msg tea.Msg) (pickBookModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ensureVisible()
	case calibreResultsMsg:
		m.searching = false
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
			m.message = "No books matched."
			m.focus = pickFocusQuery
		} else {
			m.message = fmt.Sprintf("%d result(s)", len(m.books))
			m.focus = pickFocusList
			m.editing = false
		}
		m.ensureVisible()
	case tea.KeyMsg:
		key := msg.String()
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
		m.errText = "enter a search query"
		return nil
	}
	m.searching = true
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

func (m pickBookModel) selectBook() (pickBookModel, tea.Cmd) {
	if m.cursor < 0 || m.cursor >= len(m.books) {
		return m, nil
	}
	b := m.books[m.cursor]
	path, _, err := m.client.BestFormatPath(b)
	if err != nil {
		m.errText = err.Error()
		return m, nil
	}
	return m, func() tea.Msg { return bookPickedMsg{path: path} }
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
	b.WriteString(ansi.Truncate(subtitleStyle.Render("  Search, then select a book (EPUB/PDF)"), width, "…"))
	b.WriteString("\n\n")

	qLabel := "Query"
	qVal := m.query
	if m.editing && m.focus == pickFocusQuery {
		qVal = m.editBuf + "█"
	} else if strings.TrimSpace(qVal) == "" {
		qVal = "(type a search, enter to run)"
	}
	qCursor, qStyle := "  ", normalStyle
	if m.focus == pickFocusQuery {
		qCursor, qStyle = "▸ ", selectedStyle
	}
	b.WriteString("  " + qCursor + qStyle.Render(fmt.Sprintf("%-8s %s", qLabel, qVal)) + "\n")

	if m.searching {
		b.WriteString(dimStyle.Render("  Searching…"))
		b.WriteString("\n")
	}

	if len(m.books) == 0 {
		if !m.searching && m.message != "" && m.errText == "" {
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
			line := fmt.Sprintf("%s — %s", book.Title, authors)
			maxWidth := max(width-6, 1)
			line = ansi.Truncate(line, maxWidth, "…")
			b.WriteString("  " + cursor + style.Render(line) + "\n")
		}
	}

	if m.errText != "" {
		b.WriteString("\n")
		b.WriteString(errorStyle.Render("  " + m.errText))
		b.WriteString("\n")
	} else if m.message != "" && !m.searching && len(m.books) > 0 {
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  " + m.message))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(dimStyle.Render(ansi.Truncate("  type query • enter search/select • ↑/↓ list • / edit query • esc back", width, "…")))
	return b.String()
}
