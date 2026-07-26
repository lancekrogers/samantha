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
	const marker = "LASTLINE"
	long := strings.Repeat("long wrapped line of text ", 6)
	// Marker at the start of the last line so col-0 (where G leaves the cursor)
	// paints it in the view.
	e := NewEditor(strings.Join([]string{long, long, long, long, long, marker + " " + long}, "\n"))
	e.SetSize(40, 6)

	press(e, "G") // jump to the last line
	if rows := e.rowsThroughCursor(e.Cursor()); rows > 6 {
		t.Fatalf("cursor sits at row %d of a 6-row viewport", rows)
	}
	view := rowsOf(e)
	if got := len(view); got != 6 {
		t.Fatalf("rendered %d rows after scrolling, want 6", got)
	}
	// The last line (distinctive prefix under the cursor) must actually appear.
	joined := strings.Join(view, "\n")
	if !strings.Contains(joined, marker) {
		t.Fatalf("cursor line not visible after scroll; view=%q", joined)
	}
}

func TestCursorVisibleOnSingleLineTallerThanViewport(t *testing.T) {
	// One logical line that wraps past the height: scrollRow must advance so
	// the cursor at EOL is painted inside the clipped window.
	const marker = "ZZCURSOR"
	line := strings.Repeat("abcdefghij", 20) + marker // many wraps at width 20
	e := NewEditor(line)
	e.SetSize(20, 4)
	// Move cursor to end of the line (normal mode: last character).
	press(e, "G")
	press(e, "$")
	e.EnsureCursorVisible()

	if rows := e.rowsThroughCursor(e.Cursor()); rows > 4 {
		t.Fatalf("cursor sits at row %d of a 4-row viewport", rows)
	}
	view := rowsOf(e)
	if got := len(view); got != 4 {
		t.Fatalf("rendered %d rows, want 4", got)
	}
	joined := strings.Join(view, "\n")
	if !strings.Contains(joined, marker) {
		t.Fatalf("EOL of tall line not visible; scrollRow=%d view=%q", e.scrollRow, joined)
	}
	if e.scrollRow == 0 {
		// Line is much taller than 4 rows; some mid-line scroll is required.
		t.Fatalf("scrollRow still 0 for a line taller than the viewport")
	}
}

func TestRowsThroughCursorCountsCellUnderCursor(t *testing.T) {
	// At an exact soft-wrap boundary, counting line[:col] under-counts by one
	// visual row: the character at col is the first cell of the next wrap row.
	line := strings.Repeat("a", 80) // width 40 → col 40 is boundary
	e := NewEditor(line)
	e.SetSize(40, 10)
	e.buffer.SetCursor(Position{Line: 0, Col: 40})

	got := e.cursorRowInLine(e.Cursor())
	if got != 2 {
		t.Fatalf("cursorRowInLine at col==width = %d, want 2", got)
	}
	e.buffer.SetCursor(Position{Line: 0, Col: 80})
	// At EOL past last char (clamped to len): still on the last content row
	// for a full exact fill (80/40 = 2 rows), unless insert caret adds a cell.
	got = e.cursorRowInLine(e.Cursor())
	if got != 2 {
		t.Fatalf("cursorRowInLine at EOL = %d, want 2", got)
	}
}

func TestTildePadIndentedUnderGutter(t *testing.T) {
	e := NewEditor("short")
	e.SetSize(40, 5)
	rows := rowsOf(e)
	// First row is content; rest are tildes.
	for i := 1; i < len(rows); i++ {
		if !strings.HasPrefix(rows[i], strings.Repeat(" ", gutterWidth)+"~") {
			t.Fatalf("pad row %d = %q, want gutter-indented tilde", i, rows[i])
		}
	}
}
