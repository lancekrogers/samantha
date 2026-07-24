package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/tui/vim"
)

func promptKeys(t *testing.T, p promptEditor, keys ...string) (promptEditor, promptEvent) {
	t.Helper()
	ev := promptEventNone
	for _, k := range keys {
		var msg tea.KeyMsg
		switch k {
		case "esc":
			msg = tea.KeyMsg{Type: tea.KeyEscape}
		case "enter":
			msg = tea.KeyMsg{Type: tea.KeyEnter}
		default:
			msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		p, _, ev = p.Update(msg)
	}
	return p, ev
}

func TestPromptEditorInsertTypingAndEscape(t *testing.T) {
	p := newPromptEditor()
	p.StartInsert()
	p, _ = promptKeys(t, p, "h", "i", "esc")
	if p.Mode() != vim.ModeNormal {
		t.Fatalf("mode = %v, want NORMAL after esc", p.Mode())
	}
	if p.Value() != "hi" {
		t.Fatalf("value = %q", p.Value())
	}
}

func TestPromptEditorDeleteLineAndUndo(t *testing.T) {
	p := newPromptEditor()
	p.SetValue("first line\nsecond line")
	p, _ = promptKeys(t, p, "d", "d")
	if p.Value() != "second line" {
		t.Fatalf("after dd value = %q", p.Value())
	}
	p, _ = promptKeys(t, p, "u")
	if p.Value() != "first line\nsecond line" {
		t.Fatalf("after undo value = %q", p.Value())
	}
}

func TestPromptEditorVisualYankPaste(t *testing.T) {
	p := newPromptEditor()
	p.SetValue("abc")
	// yy + p duplicates the line.
	p, _ = promptKeys(t, p, "y", "y", "p")
	if p.Value() != "abc\nabc" {
		t.Fatalf("after yy p value = %q", p.Value())
	}
}

func TestPromptEditorExCommandsMapToFormEvents(t *testing.T) {
	cases := []struct {
		keys []string
		want promptEvent
	}{
		{[]string{":", "w", "q", "enter"}, promptEventSave},
		{[]string{":", "w", "enter"}, promptEventSave},
		{[]string{":", "q", "!", "enter"}, promptEventCancel},
		{[]string{":", "q", "enter"}, promptEventCancel},
		{[]string{"enter"}, promptEventSave}, // normal-mode enter = :wq (camp pattern)
	}
	for _, tc := range cases {
		p := newPromptEditor()
		p.SetValue("body")
		_, ev := promptKeys(t, p, tc.keys...)
		if ev != tc.want {
			t.Fatalf("keys %v → event %v, want %v", tc.keys, ev, tc.want)
		}
	}
}

func TestPromptEditorEscCancelsOnlyWhenIdle(t *testing.T) {
	p := newPromptEditor()
	p.SetValue("some text")

	// Pending operator: esc clears it, no cancel.
	p, ev := promptKeys(t, p, "d", "esc")
	if ev != promptEventNone {
		t.Fatalf("esc with pending d → %v, want none", ev)
	}
	// Visual mode: esc drops to normal, no cancel.
	p, ev = promptKeys(t, p, "v", "esc")
	if ev != promptEventNone {
		t.Fatalf("esc in visual → %v, want none", ev)
	}
	// Idle normal mode: esc cancels the form.
	_, ev = promptKeys(t, p, "esc")
	if ev != promptEventCancel {
		t.Fatalf("idle esc → %v, want cancel", ev)
	}
}

func TestPromptEditorInsertArrows(t *testing.T) {
	p := newPromptEditor()
	p.SetValue("ab\ncd")
	p.StartInsert()
	p, _, _ = p.Update(tea.KeyMsg{Type: tea.KeyRight})
	p, _, _ = p.Update(tea.KeyMsg{Type: tea.KeyDown})
	p, _, _ = p.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'X'}})
	if p.Value() != "ab\ncXd" {
		t.Fatalf("value = %q, want cursor moved by arrows before insert", p.Value())
	}
}

func TestPromptEditorSetValueResetsUndoHistory(t *testing.T) {
	p := newPromptEditor()
	p.SetValue("persona A secret")
	p, _ = promptKeys(t, p, "d", "d")
	p.SetValue("persona B")
	p, _ = promptKeys(t, p, "u")
	if strings.Contains(p.Value(), "secret") {
		t.Fatalf("undo resurrected prior persona content: %q", p.Value())
	}
}

func TestPromptEditorViewShowsLineNumbersAndContent(t *testing.T) {
	p := newPromptEditor()
	p.SetValue("hello world")
	view := stripANSI(p.View())
	if !strings.Contains(view, "hello world") || !strings.Contains(view, "1") {
		t.Fatalf("view missing content or line numbers:\n%s", view)
	}
}
