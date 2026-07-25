package vim

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// ViewConfig configures the editor view rendering.
type ViewConfig struct {
	// Styles
	NormalText   lipgloss.Style
	CursorBlock  lipgloss.Style // For normal mode cursor (inverted)
	CursorInsert lipgloss.Style // For insert mode cursor position
	Selection    lipgloss.Style // For visual mode selection
	LineNumber   lipgloss.Style
	CommandLine  lipgloss.Style
	ShowLineNums bool

	// PlaceholderKnown styles tokens reported by Tokens whose name is in
	// KnownPlaceholders; PlaceholderUnknown styles the rest. Nil or empty
	// KnownPlaceholders means every reported token is "known".
	PlaceholderKnown   lipgloss.Style
	PlaceholderUnknown lipgloss.Style
	KnownPlaceholders  map[string]bool
	// Tokens reports the highlightable spans on one line. The editor has no
	// opinion on the token grammar — the host owns it, so the colorizer can
	// never drift from whatever actually substitutes the tokens at runtime.
	Tokens func(line string) []TokenSpan
}

// TokenSpan is a highlightable [Start,End) byte range on a line and the bare
// name it carries (used to pick the known vs unknown style).
type TokenSpan struct {
	Start int
	End   int
	Name  string
}

// DefaultViewConfig returns theme-neutral styles; hosts override the colors
// with their own palette.
func DefaultViewConfig() ViewConfig {
	return ViewConfig{
		NormalText:   lipgloss.NewStyle(),
		CursorBlock:  lipgloss.NewStyle().Reverse(true),
		CursorInsert: lipgloss.NewStyle().Underline(true),
		Selection:    lipgloss.NewStyle().Background(lipgloss.Color("8")),
		LineNumber:   lipgloss.NewStyle().Foreground(lipgloss.Color("8")),
		CommandLine:  lipgloss.NewStyle().Foreground(lipgloss.Color("7")),
		ShowLineNums: true,
		// Placeholder styles default empty so hosts opt in.
		PlaceholderKnown:   lipgloss.NewStyle().Foreground(lipgloss.Color("6")).Bold(true),
		PlaceholderUnknown: lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
	}
}

// View renders the editor content with cursor and selection highlighting.
func (e *Editor) View(cfg ViewConfig) string {
	var b strings.Builder

	lines := e.buffer.Lines()
	cursor := e.buffer.Cursor()

	startLine := min(max(e.scrollOffset, 0), max(len(lines)-1, 0))

	// Get visual selection range if applicable. V-LINE highlights whole lines,
	// matching what d/y/c will act on.
	var selStartOff, selEndOff int
	inVisual := e.state.IsVisual()
	if inVisual {
		from, to := e.visualSpan()
		selStartOff = e.lineStartOffset(from.Line) + from.Col
		selEndOff = e.lineStartOffset(to.Line) + to.Col
		if e.state.Mode == ModeVisualLine {
			selEndOff = e.lineStartOffset(to.Line) + len(lines[to.Line])
		}
	}

	// Build visual rows, not logical lines. A wrapped line occupies several rows,
	// so budgeting by logical lines let the editor render far more rows than its
	// height — the persona form sized a box for e.height and got a taller one.
	rows := make([]string, 0, e.height)
	for lineIdx := startLine; lineIdx < len(lines) && len(rows) < e.height; lineIdx++ {
		rendered := e.renderLine(lineIdx, lines[lineIdx], cursor, cfg, inVisual, selStartOff, selEndOff)
		for i, row := range wrapRows(rendered, e.width) {
			if len(rows) >= e.height {
				break
			}
			// Only the first row of a wrapped line carries its number; the rest
			// are indented to match, or continuations would start at column 0.
			rows = append(rows, e.gutter(cfg, lineIdx, i == 0)+row)
		}
	}

	// Pad to exactly height so the caller's layout budget always holds.
	for len(rows) < e.height {
		rows = append(rows, "~")
	}

	b.WriteString(strings.Join(rows, "\n"))

	return b.String()
}

// renderLine renders a single line with cursor/selection/placeholder highlighting.
func (e *Editor) renderLine(lineIdx int, line string, cursor Position, cfg ViewConfig, inVisual bool, selStartOff, selEndOff int) string {
	lineStartOffset := e.lineStartOffset(lineIdx)

	// Handle empty line with cursor
	if len(line) == 0 {
		if lineIdx == cursor.Line {
			if e.state.Mode == ModeInsert {
				return cfg.CursorInsert.Render(" ")
			}
			return cfg.CursorBlock.Render(" ")
		}
		return " " // Empty line placeholder
	}

	// Per-byte style for the host's tokens on this line.
	phStyle := tokenStylesForLine(line, cfg)

	var result strings.Builder

	// Iterate by byte index so columns match buffer.Col (byte-based).
	for col := 0; col < len(line); {
		next := stepFwd(line, col)
		char := line[col:next]
		charOffset := lineStartOffset + col

		isCursor := lineIdx == cursor.Line && col == cursor.Col
		isSelected := inVisual && charOffset >= selStartOff && charOffset <= selEndOff

		switch {
		case isCursor && e.state.Mode == ModeInsert:
			result.WriteString(cfg.CursorInsert.Render(char))
		case isCursor:
			result.WriteString(cfg.CursorBlock.Render(char))
		case isSelected:
			result.WriteString(cfg.Selection.Render(char))
		default:
			if st, ok := phStyle[col]; ok {
				result.WriteString(st.Render(char))
			} else {
				result.WriteString(cfg.NormalText.Render(char))
			}
		}
		col = next
	}

	// Handle cursor at end of line in insert mode
	if e.state.Mode == ModeInsert && lineIdx == cursor.Line && cursor.Col >= len(line) {
		result.WriteString(cfg.CursorInsert.Render(" "))
	}

	return result.String()
}

// tokenStylesForLine maps each byte index inside a host-reported token to the
// style that should paint it.
func tokenStylesForLine(line string, cfg ViewConfig) map[int]lipgloss.Style {
	if cfg.Tokens == nil {
		return nil
	}
	spans := cfg.Tokens(line)
	if len(spans) == 0 {
		return nil
	}
	out := make(map[int]lipgloss.Style, len(spans)*8)
	for _, span := range spans {
		st := cfg.PlaceholderKnown
		if len(cfg.KnownPlaceholders) > 0 && !cfg.KnownPlaceholders[span.Name] {
			st = cfg.PlaceholderUnknown
		}
		for k := max(span.Start, 0); k < min(span.End, len(line)); k++ {
			out[k] = st
		}
	}
	return out
}

// lineStartOffset calculates the absolute offset at the start of a line.
func (e *Editor) lineStartOffset(lineIdx int) int {
	offset := 0
	lines := e.buffer.Lines()
	for i := 0; i < lineIdx && i < len(lines); i++ {
		offset += len(lines[i]) + 1 // +1 for newline
	}
	return offset
}

// gutter renders the line-number column for one visual row. Continuation rows
// of a wrapped line get blank padding of the same width so text stays aligned.
func (e *Editor) gutter(cfg ViewConfig, lineIdx int, first bool) string {
	if !cfg.ShowLineNums {
		return ""
	}
	if !first {
		return strings.Repeat(" ", gutterWidth)
	}
	return lipgloss.NewStyle().Width(gutterWidth).Align(lipgloss.Right).Render(
		strings.TrimSpace(cfg.LineNumber.Render(itoa(lineIdx+1) + " ")),
	)
}

// gutterWidth is the line-number column width. Hosts subtract it when sizing
// the editor (see resizeForm in the persona form).
const gutterWidth = 4

// wrapRows soft-wraps one rendered line into visual rows. Styling is already
// applied, so lipgloss wraps on display width and leaves the ANSI intact.
func wrapRows(rendered string, width int) []string {
	if width <= 0 {
		return []string{rendered}
	}
	return strings.Split(lipgloss.NewStyle().Width(width).Render(rendered), "\n")
}

// visualRows is how many rows a logical line occupies once wrapped.
func (e *Editor) visualRows(lineIdx int) int {
	lines := e.buffer.Lines()
	if lineIdx < 0 || lineIdx >= len(lines) {
		return 1
	}
	return len(wrapRows(lines[lineIdx], e.width))
}

// EnsureCursorVisible adjusts scroll offset to keep the cursor visible, counting
// wrapped rows — comparing logical line indices alone let the cursor sit below
// the viewport whenever earlier lines wrapped.
func (e *Editor) EnsureCursorVisible() {
	cursor := e.buffer.Cursor()

	if cursor.Line < e.scrollOffset {
		e.scrollOffset = cursor.Line
	}
	if e.scrollOffset < 0 {
		e.scrollOffset = 0
	}

	// Advance the top line until the cursor's own row fits in the window.
	for e.scrollOffset < cursor.Line && e.rowsThroughCursor(cursor) > e.height {
		e.scrollOffset++
	}
}

// rowsThroughCursor counts visual rows from the top of the viewport through the
// row the cursor sits on.
func (e *Editor) rowsThroughCursor(cursor Position) int {
	rows := 0
	for i := e.scrollOffset; i < cursor.Line; i++ {
		rows += e.visualRows(i)
	}
	line := e.buffer.CurrentLine()
	col := min(max(cursor.Col, 0), len(line))
	return rows + len(wrapRows(line[:col], e.width))
}

// itoa is a simple int to string conversion.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var digits []byte
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	return string(digits)
}
