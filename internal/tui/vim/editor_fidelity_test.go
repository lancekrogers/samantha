package vim

import (
	"strings"
	"testing"
	"unicode/utf8"

	tea "github.com/charmbracelet/bubbletea"
)

// press feeds keys to the editor: each rune of a plain string is one key, and
// "esc"/"enter" name the special keys.
func press(e *Editor, keys ...string) {
	for _, k := range keys {
		switch k {
		case "esc":
			e.Update(tea.KeyMsg{Type: tea.KeyEscape})
		case "enter":
			e.Update(tea.KeyMsg{Type: tea.KeyEnter})
		default:
			for _, r := range k {
				e.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
			}
		}
	}
}

func TestVisualLineDeletesWholeLines(t *testing.T) {
	e := NewEditor("alpha beta\nsecond line\nthird")
	press(e, "lll", "V", "d")
	if got := e.Content(); got != "second line\nthird" {
		t.Fatalf("Vd = %q, want the first line gone", got)
	}
}

func TestVisualLineSpansMultipleLinesAndYanksLinewise(t *testing.T) {
	e := NewEditor("one\ntwo\nthree\nfour")
	press(e, "V", "j", "y")
	if got := e.buffer.Yank(); got != "one\ntwo\n" {
		t.Fatalf("Vjy yank = %q, want linewise one+two", got)
	}
	press(e, "V", "j", "d")
	if got := e.Content(); got != "three\nfour" {
		t.Fatalf("Vjd = %q", got)
	}
}

func TestVisualCharwiseStillInclusive(t *testing.T) {
	e := NewEditor("abcdef")
	press(e, "v", "ll", "d") // select a..c
	if got := e.Content(); got != "def" {
		t.Fatalf("v ll d = %q, want def", got)
	}
}

func TestUndoAfterInsertSession(t *testing.T) {
	e := NewEditor("hello")
	press(e, "A", " world", "esc")
	if got := e.Content(); got != "hello world" {
		t.Fatalf("setup = %q", got)
	}
	press(e, "u")
	if got := e.Content(); got != "hello" {
		t.Fatalf("u after insert = %q, want hello", got)
	}
	press(e, "ctrl+r")
}

func TestUndoIgnoresEmptyInsertSession(t *testing.T) {
	e := NewEditor("keep")
	press(e, "x")        // one real change: "eep"
	press(e, "i", "esc") // insert session that typed nothing
	press(e, "u")        // must undo the x, not the no-op
	if got := e.Content(); got != "keep" {
		t.Fatalf("u = %q, want keep (empty insert must not consume the undo)", got)
	}
}

func TestUndoTreatsChangeAsOneBlock(t *testing.T) {
	e := NewEditor("foo bar")
	press(e, "cw", "baz", "esc")
	if got := e.Content(); got != "bazbar" {
		t.Fatalf("cw = %q", got)
	}
	press(e, "u")
	if got := e.Content(); got != "foo bar" {
		t.Fatalf("u after cw = %q, want the whole change reverted", got)
	}
}

func TestCountsWithLeadingOne(t *testing.T) {
	lines := make([]string, 0, 14)
	for i := range 14 {
		lines = append(lines, string(rune('a'+i)))
	}
	content := strings.Join(lines, "\n")

	e := NewEditor(content)
	press(e, "10j")
	if got := e.Cursor().Line; got != 10 {
		t.Fatalf("10j line = %d, want 10", got)
	}

	e = NewEditor(content)
	press(e, "11j")
	if got := e.Cursor().Line; got != 11 {
		t.Fatalf("11j line = %d, want 11", got)
	}

	e = NewEditor(content)
	press(e, "12G")
	if got := e.Cursor().Line; got != 11 {
		t.Fatalf("12G line = %d, want 11 (1-indexed)", got)
	}

	e = NewEditor(content)
	press(e, "G")
	if got := e.Cursor().Line; got != 13 {
		t.Fatalf("bare G line = %d, want last", got)
	}

	e = NewEditor(content)
	press(e, "1G")
	if got := e.Cursor().Line; got != 0 {
		t.Fatalf("1G line = %d, want 0", got)
	}
}

func TestZeroIsLineStartNotCount(t *testing.T) {
	e := NewEditor("abcdef")
	press(e, "lll", "0")
	if got := e.Cursor().Col; got != 0 {
		t.Fatalf("0 col = %d, want line start", got)
	}
}

func TestLinewiseOperatorMotions(t *testing.T) {
	e := NewEditor("aaaa\nbbbb\ncccc")
	press(e, "ll", "dj")
	if got := e.Content(); got != "cccc" {
		t.Fatalf("dj = %q, want both lines gone", got)
	}

	e = NewEditor("aaaa\nbbbb\ncccc")
	press(e, "j", "dk")
	if got := e.Content(); got != "cccc" {
		t.Fatalf("dk = %q, want both lines gone", got)
	}

	e = NewEditor("aaaa\nbbbb\ncccc")
	press(e, "yj", "G", "p")
	if got := e.Content(); got != "aaaa\nbbbb\ncccc\naaaa\nbbbb" {
		t.Fatalf("yj then p = %q", got)
	}
}

func TestExclusiveMotionOperators(t *testing.T) {
	cases := []struct {
		name, content, keys, want string
	}{
		{"d0 stops before the cursor rune", "abcdef", "lll" + "d0", "def"},
		// vim's db deletes back to the word start, never the cursor rune itself.
		{"db stops before the cursor rune", "foo bar", "$" + "db", "foo r"},
		{"dw drops the trailing space", "foo bar", "dw", "bar"},
		{"de is inclusive", "foo bar", "de", " bar"},
		{"dl deletes one rune", "abc", "dl", "bc"},
		{"dh deletes the previous rune", "abc", "l" + "dh", "bc"},
		{"d$ deletes to end of line", "abcdef", "ll" + "d$", "ab"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEditor(tc.content)
			press(e, tc.keys)
			if got := e.Content(); got != tc.want {
				t.Fatalf("%s = %q, want %q", tc.keys, got, tc.want)
			}
		})
	}
}

func TestMultiByteEditsStayValidUTF8(t *testing.T) {
	const content = "a—b “quoted” café"
	steps := []struct {
		name string
		keys []string
	}{
		{"x on a multi-byte rune", []string{"l", "x"}},
		{"repeated x", []string{"xxxxxx"}},
		{"X backwards", []string{"$", "XXX"}},
		{"r replaces the whole rune", []string{"l", "r", "-"}},
		{"dw over multi-byte words", []string{"dw"}},
		{"de over multi-byte words", []string{"de"}},
		{"visual delete", []string{"v", "lll", "d"}},
		{"insert backspace", []string{"$", "a", "esc", "A"}},
	}
	for _, tc := range steps {
		t.Run(tc.name, func(t *testing.T) {
			e := NewEditor(content)
			press(e, tc.keys...)
			got := e.Content()
			if !utf8.ValidString(got) {
				t.Fatalf("%v produced invalid UTF-8: %q", tc.keys, got)
			}
		})
	}
}

func TestMultiByteDeleteRemovesWholeRune(t *testing.T) {
	e := NewEditor("a—b")
	press(e, "l", "x")
	if got := e.Content(); got != "ab" {
		t.Fatalf("x on em dash = %q, want ab", got)
	}
}

func TestMultiByteBackspaceRemovesWholeRune(t *testing.T) {
	e := NewEditor("a—")
	press(e, "A")
	e.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if got := e.Content(); got != "a" {
		t.Fatalf("backspace over em dash = %q, want a", got)
	}
}

func TestMultiByteCursorStepsOneRune(t *testing.T) {
	e := NewEditor("a—b")
	press(e, "l")
	if got := e.CharUnderCursor(); got != '—' {
		t.Fatalf("char under cursor = %q, want em dash", got)
	}
	press(e, "l")
	if got := e.CharUnderCursor(); got != 'b' {
		t.Fatalf("char after second l = %q, want b", got)
	}
	press(e, "h")
	if got := e.CharUnderCursor(); got != '—' {
		t.Fatalf("char after h = %q, want em dash", got)
	}
}

// CharUnderCursor is only reachable through the buffer; expose it for the test.
func (e *Editor) CharUnderCursor() rune { return e.buffer.CharUnderCursor() }

func TestXDoesNotJoinLines(t *testing.T) {
	e := NewEditor("\nsecond")
	press(e, "x")
	if got := e.Content(); got != "\nsecond" {
		t.Fatalf("x on a blank line = %q, want unchanged", got)
	}

	e = NewEditor("ab\ncd")
	press(e, "xx")
	if got := e.Content(); got != "\ncd" {
		t.Fatalf("x to end of line = %q, want the line emptied, not joined", got)
	}
	press(e, "x")
	if got := e.Content(); got != "\ncd" {
		t.Fatalf("x past end of line = %q, want unchanged", got)
	}
}

func TestDeleteKeyStillJoinsInInsertMode(t *testing.T) {
	e := NewEditor("ab\ncd")
	press(e, "$", "a") // insert at end of line 0
	e.Update(tea.KeyMsg{Type: tea.KeyDelete})
	if got := e.Content(); got != "abcd" {
		t.Fatalf("Delete at end of line = %q, want joined", got)
	}
}

func TestLinewiseChangeOpensOneLine(t *testing.T) {
	e := NewEditor("aaaa\nbbbb\ncccc")
	press(e, "cj", "new")
	if got := e.Content(); got != "new\ncccc" {
		t.Fatalf("cj = %q, want new\\ncccc", got)
	}

	// Changing every line must leave exactly one line to type on.
	e = NewEditor("aaaa\nbbbb")
	press(e, "V", "j", "c", "only")
	if got := e.Content(); got != "only" {
		t.Fatalf("Vjc = %q, want only (no stranded blank line)", got)
	}
}
