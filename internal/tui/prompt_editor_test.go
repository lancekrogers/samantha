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
		case "tab":
			msg = tea.KeyMsg{Type: tea.KeyTab}
		case "shift-tab":
			msg = tea.KeyMsg{Type: tea.KeyShiftTab}
		case "backspace":
			msg = tea.KeyMsg{Type: tea.KeyBackspace}
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
		// :w alone accepts the field (next wizard step); it must not submit the form.
		{[]string{":", "w", "enter"}, promptEventAccept},
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

func TestPromptEditorPlaceholderTabComplete(t *testing.T) {
	p := newPromptEditor()
	p.StartInsert()
	// Type "You are {" then Tab → preview agent_name, Enter → close brace.
	p, _ = promptKeys(t, p, "Y", "o", "u", " ", "a", "r", "e", " ", "{", "tab", "enter")
	if !strings.Contains(p.Value(), "{agent_name}") {
		t.Fatalf("value = %q, want {agent_name} inserted", p.Value())
	}
	if p.phActive {
		t.Fatal("completion should clear after enter")
	}
}

func TestPromptEditorPlaceholderTabAfterPartial(t *testing.T) {
	p := newPromptEditor()
	p.StartInsert()
	p, _ = promptKeys(t, p, "{", "a", "g", "tab", "enter")
	if p.Value() != "{agent_name}" {
		t.Fatalf("value = %q", p.Value())
	}
}

func TestPromptEditorPlaceholderEscCancelsPreview(t *testing.T) {
	p := newPromptEditor()
	p.StartInsert()
	p, _ = promptKeys(t, p, "H", "i", " ", "{", "tab")
	if !strings.Contains(p.Value(), "{agent_name") {
		t.Fatalf("preview missing: %q", p.Value())
	}
	p, ev := promptKeys(t, p, "esc")
	if ev != promptEventNone {
		t.Fatalf("esc during completion canceled form: %v", ev)
	}
	if p.Mode() != vim.ModeInsert {
		t.Fatalf("mode = %v, want insert after esc cancel completion", p.Mode())
	}
	// Preview name dropped; opening brace remains.
	if p.Value() != "Hi {" {
		t.Fatalf("value = %q, want brace kept without preview name", p.Value())
	}
}

func TestPromptEditorViewColorizesKnownPlaceholder(t *testing.T) {
	p := newPromptEditor()
	p.SetValue("You are {agent_name}.")
	view := p.View()
	// Raw view must still contain the token text; style is ANSI-wrapped.
	if !strings.Contains(stripANSI(view), "{agent_name}") {
		t.Fatalf("view missing token:\n%s", stripANSI(view))
	}
	// modeline in insert mentions tab variables when not completing.
	p.StartInsert()
	if !strings.Contains(stripANSI(p.modeline()), "tab variables") {
		t.Fatalf("modeline = %q", stripANSI(p.modeline()))
	}
	// Completing shows the selected variable in the modeline.
	p, _ = promptKeys(t, p, "{", "tab")
	if !strings.Contains(stripANSI(p.modeline()), "agent_name") {
		t.Fatalf("completion modeline = %q", stripANSI(p.modeline()))
	}
}

func TestOpenBracePrefix(t *testing.T) {
	col, partial, ok := openBracePrefix("You are {ag", 11)
	if !ok || col != 8 || partial != "ag" {
		t.Fatalf("got col=%d partial=%q ok=%v", col, partial, ok)
	}
	if _, _, ok := openBracePrefix("done {agent_name} more", 20); ok {
		t.Fatal("closed token should not open completion")
	}
	if _, _, ok := openBracePrefix("no brace", 4); ok {
		t.Fatal("expected no open brace")
	}
}

func TestPromptEditorEscKeepsTypedPartial(t *testing.T) {
	p := newPromptEditor()
	p.StartInsert()
	p, _ = promptKeys(t, p, "{", "a", "g", "tab")
	if !strings.Contains(p.Value(), "{agent_name") {
		t.Fatalf("preview missing: %q", p.Value())
	}
	p, ev := promptKeys(t, p, "esc")
	if ev != promptEventNone {
		t.Fatalf("esc during completion canceled the form: %v", ev)
	}
	if p.Value() != "{ag" {
		t.Fatalf("value = %q, want the typed prefix kept", p.Value())
	}
}

func TestPlaceholderTokensMatchResolverGrammar(t *testing.T) {
	line := "You are {agent_name}, not {planet} nor {bad-name}."
	spans := placeholderTokens(line)
	if len(spans) != 2 {
		t.Fatalf("spans = %+v, want the two well-formed tokens", spans)
	}
	if got := line[spans[0].Start:spans[0].End]; got != "{agent_name}" {
		t.Fatalf("first span = %q", got)
	}
	if spans[0].Name != "agent_name" {
		t.Fatalf("first name = %q", spans[0].Name)
	}
	if spans[1].Name != "planet" {
		t.Fatalf("second name = %q", spans[1].Name)
	}
}

func TestPromptEditorCompletionActiveGate(t *testing.T) {
	p := newPromptEditor()
	p.SetValue("plain text")
	if p.completionActive() {
		t.Fatal("normal mode must not claim tab")
	}
	p.StartInsert()
	if p.completionActive() {
		t.Fatal("insert mode with no open brace must not claim tab")
	}
	p, _ = promptKeys(t, p, "{")
	if !p.completionActive() {
		t.Fatal("an open brace with candidates should claim tab")
	}
	p, _ = promptKeys(t, p, "z", "z")
	if p.completionActive() {
		t.Fatal("an open brace with no matching candidate must not claim tab")
	}
}
