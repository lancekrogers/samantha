package vim

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func rowsOf(e *Editor) []string { return strings.Split(e.View(DefaultViewConfig()), "\n") }

func TestViewRendersExactlyHeightRowsWhenWrapping(t *testing.T) {
	long := strings.Repeat("wrapped paragraph text ", 12) // far wider than the editor
	cases := []struct{ name, content string }{
		{"short lines", "one\ntwo\nthree"},
		{"one long line", long},
		{"many long lines", strings.Join([]string{long, long, long, long, long, long}, "\n")},
		{"empty buffer", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEditor(tc.content)
			e.SetSize(40, 8)
			if got := len(rowsOf(e)); got != 8 {
				t.Fatalf("rendered %d rows, want exactly the 8-row height", got)
			}
		})
	}
}

func TestViewRowsNeverExceedWidth(t *testing.T) {
	e := NewEditor(strings.Repeat("overflowing content ", 20))
	e.SetSize(40, 6)
	for i, row := range rowsOf(e) {
		if w := lipgloss.Width(row); w > 40+gutterWidth {
			t.Fatalf("row %d width %d exceeds %d (gutter+content)", i, w, 40+gutterWidth)
		}
	}
}

func TestWrappedContinuationRowsAreIndentedUnderTheGutter(t *testing.T) {
	// Regression: the gutter was written once before the wrapped block, so
	// continuation rows started at column 0 and text misaligned.
	e := NewEditor(strings.Repeat("abcdefghij ", 12))
	e.SetSize(30, 6)
	rows := rowsOf(e)
	if len(rows) < 2 {
		t.Fatalf("expected the line to wrap, got %d rows", len(rows))
	}
	if strings.HasPrefix(rows[1], " ") == false {
		t.Fatalf("continuation row is not indented under the gutter: %q", rows[1])
	}
	first := len(rows[0]) - len(strings.TrimLeft(rows[0], " "))
	cont := len(rows[1]) - len(strings.TrimLeft(rows[1], " "))
	if cont < gutterWidth {
		t.Fatalf("continuation indent %d < gutter width %d (first row indent %d)", cont, gutterWidth, first)
	}
}

func TestCursorStaysVisibleWithWrappedLines(t *testing.T) {
	// Earlier lines that wrap consume rows; scrolling by logical index alone
	// pushed the cursor below the viewport.
	long := strings.Repeat("long wrapped line of text ", 6)
	e := NewEditor(strings.Join([]string{long, long, long, long, long, long}, "\n"))
	e.SetSize(40, 6)

	press(e, "G") // jump to the last line
	if rows := e.rowsThroughCursor(e.Cursor()); rows > 6 {
		t.Fatalf("cursor sits at row %d of a 6-row viewport", rows)
	}
	if got := len(rowsOf(e)); got != 6 {
		t.Fatalf("rendered %d rows after scrolling, want 6", got)
	}
}
