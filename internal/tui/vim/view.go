package vim

import (
	"strings"
	"unicode/utf8"

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

	// PlaceholderKnown styles complete {name} tokens whose name is in
	// KnownPlaceholders (or all tokens when KnownPlaceholders is nil/empty
	// and PlaceholderKnown is set). PlaceholderUnknown styles other tokens.
	PlaceholderKnown   lipgloss.Style
	PlaceholderUnknown lipgloss.Style
	// KnownPlaceholders maps bare placeholder names that should use the
	// known style. Nil or empty means every complete token is "known" when
	// PlaceholderKnown is non-zero (host usually supplies the catalog).
	KnownPlaceholders map[string]bool
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

	// Calculate visible range based on scroll offset
	startLine := e.scrollOffset
	endLine := min(startLine+e.height, len(lines))

	// Ensure we have at least some lines to show
	if endLine <= startLine {
		endLine = startLine + 1
	}
	if endLine > len(lines) {
		endLine = len(lines)
	}

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

	for lineIdx := startLine; lineIdx < endLine; lineIdx++ {
		if lineIdx >= len(lines) {
			break
		}
		line := lines[lineIdx]

		// Optional line numbers
		if cfg.ShowLineNums {
			lineNum := lipgloss.NewStyle().Width(4).Align(lipgloss.Right).Render(
				strings.TrimSpace(cfg.LineNumber.Render(itoa(lineIdx+1) + " ")),
			)
			b.WriteString(lineNum)
		}

		// Render line content with cursor/selection highlighting
		renderedLine := e.renderLine(lineIdx, line, cursor, cfg, inVisual, selStartOff, selEndOff)

		// Apply soft wrapping to fit within editor width
		if e.width > 0 {
			wrapStyle := lipgloss.NewStyle().Width(e.width)
			renderedLine = wrapStyle.Render(renderedLine)
		}

		b.WriteString(renderedLine)

		if lineIdx < endLine-1 {
			b.WriteString("\n")
		}
	}

	// Pad with empty lines if content is shorter than height
	for i := endLine - startLine; i < e.height; i++ {
		b.WriteString("\n~")
	}

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

	// Per-byte style for complete {name} tokens on this line.
	phStyle := placeholderStylesForLine(line, cfg)

	var result strings.Builder

	// Iterate by byte index so columns match buffer.Col (byte-based).
	for col := 0; col < len(line); {
		r, size := decodeRune(line[col:])
		char := line[col : col+size]
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
		col += size
		_ = r
	}

	// Handle cursor at end of line in insert mode
	if e.state.Mode == ModeInsert && lineIdx == cursor.Line && cursor.Col >= len(line) {
		result.WriteString(cfg.CursorInsert.Render(" "))
	}

	return result.String()
}

// placeholderStylesForLine maps each byte index inside a complete {name}
// token to the style that should paint that byte.
func placeholderStylesForLine(line string, cfg ViewConfig) map[int]lipgloss.Style {
	out := make(map[int]lipgloss.Style)
	for i := 0; i < len(line); i++ {
		if line[i] != '{' {
			continue
		}
		j := i + 1
		if j >= len(line) || !isIdentStart(line[j]) {
			continue
		}
		j++
		for j < len(line) && isIdentCont(line[j]) {
			j++
		}
		if j >= len(line) || line[j] != '}' {
			continue
		}
		name := line[i+1 : j]
		st := cfg.PlaceholderKnown
		if len(cfg.KnownPlaceholders) > 0 && !cfg.KnownPlaceholders[name] {
			st = cfg.PlaceholderUnknown
		}
		for k := i; k <= j; k++ {
			out[k] = st
		}
		i = j
	}
	return out
}

func isIdentStart(b byte) bool {
	return b == '_' || (b >= 'A' && b <= 'Z') || (b >= 'a' && b <= 'z')
}

func isIdentCont(b byte) bool {
	return isIdentStart(b) || (b >= '0' && b <= '9')
}

func decodeRune(s string) (rune, int) {
	return utf8.DecodeRuneInString(s)
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

// EnsureCursorVisible adjusts scroll offset to keep cursor visible.
func (e *Editor) EnsureCursorVisible() {
	cursor := e.buffer.Cursor()

	// Scroll up if cursor above viewport
	if cursor.Line < e.scrollOffset {
		e.scrollOffset = cursor.Line
	}

	// Scroll down if cursor below viewport
	if cursor.Line >= e.scrollOffset+e.height {
		e.scrollOffset = cursor.Line - e.height + 1
	}

	// Clamp scroll offset
	if e.scrollOffset < 0 {
		e.scrollOffset = 0
	}
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
