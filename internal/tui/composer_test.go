package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func keyRune(r rune) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}}
}

func TestVimCommandEnablesDynamicNormalAndInsertModes(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, false)
	m, _ = typeAndEnter(m, "/vim on")

	if !m.vimEnabled || m.vimMode != vimNormal {
		t.Fatalf("/vim on produced enabled=%v mode=%d, want NORMAL", m.vimEnabled, m.vimMode)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "NORMAL") || !strings.Contains(view, "i/a insert") {
		t.Fatalf("normal-mode UI is not dynamic:\n%s", view)
	}

	// Normal mode consumes printable commands instead of inserting them.
	m, _ = m.Update(keyRune('z'))
	if got := m.input.Value(); got != "" {
		t.Fatalf("normal-mode key inserted text: %q", got)
	}

	m, _ = m.Update(keyRune('i'))
	m, _ = m.Update(keyRune('h'))
	m, _ = m.Update(keyRune('i'))
	if got := m.input.Value(); got != "hi" {
		t.Fatalf("insert-mode draft = %q, want hi", got)
	}
	if view := stripANSI(m.View()); !strings.Contains(view, "INSERT") || !strings.Contains(view, "esc normal") {
		t.Fatalf("insert-mode UI is not dynamic:\n%s", view)
	}

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.vimMode != vimNormal {
		t.Fatal("Esc did not return to NORMAL")
	}
}

func TestVimNormalMotionsDeleteLineAndUndo(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.vimEnabled = true
	m.vimMode = vimNormal
	m.input.SetValue("alpha beta\nsecond line")
	m.moveCursorTo(0, 0)

	m, _ = m.Update(keyRune('w'))
	if got := m.cursorColumn(); got <= 0 {
		t.Fatalf("w did not move forward: column %d", got)
	}
	m, _ = m.Update(keyRune('$'))
	if got, want := m.cursorColumn(), len("alpha beta")-1; got != want {
		t.Fatalf("$ column = %d, want %d", got, want)
	}

	m, _ = m.Update(keyRune('d'))
	if m.vimPending != "d" {
		t.Fatal("first d did not enter pending operator state")
	}
	m, _ = m.Update(keyRune('d'))
	if got := m.input.Value(); got != "second line" {
		t.Fatalf("dd result = %q, want second line", got)
	}
	m, _ = m.Update(keyRune('u'))
	if got := m.input.Value(); got != "alpha beta\nsecond line" {
		t.Fatalf("undo result = %q", got)
	}
}

func TestVimInsertSessionUndoesAsOneEdit(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.vimEnabled = true
	m.vimMode = vimNormal
	m.input.SetValue("base")
	m.input.CursorEnd()

	m, _ = m.Update(keyRune('a'))
	for _, r := range " text" {
		m, _ = m.Update(keyRune(r))
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := m.input.Value(); got != "base text" {
		t.Fatalf("insert session result = %q", got)
	}
	m, _ = m.Update(keyRune('u'))
	if got := m.input.Value(); got != "base" {
		t.Fatalf("insert-session undo = %q, want base", got)
	}
}

func TestVimNormalEnterSubmitsDraft(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, false)
	m.vimEnabled = true
	m.vimMode = vimNormal
	m.input.SetValue("send from normal")

	m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil || m.turnState != turnTextRunning {
		t.Fatal("Enter in NORMAL did not dispatch draft")
	}
	_ = cmd()
	if got := runner.texts(); len(got) != 1 || got[0] != "send from normal" {
		t.Fatalf("submitted texts = %v", got)
	}
}

func TestVimCanBeDisabledFromSlashCommand(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, false)
	m.vimEnabled = true
	m.vimMode = vimInsert
	m, _ = typeAndEnter(m, "/vim off")

	if m.vimEnabled {
		t.Fatal("/vim off left Vim enabled")
	}
	m, _ = m.Update(keyRune('x'))
	if got := m.input.Value(); got != "x" {
		t.Fatalf("plain input did not resume after /vim off: %q", got)
	}
}

func TestVimNormalSlashEntersInsertMode(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.vimEnabled = true
	m.vimMode = vimNormal

	m, _ = m.Update(keyRune('/'))
	if m.vimMode != vimInsert {
		t.Fatalf("slash left Vim in mode %d, want INSERT", m.vimMode)
	}
	if got := m.input.Value(); got != "/" {
		t.Fatalf("slash input = %q, want /", got)
	}
}

func TestVimInsertSupportsExplicitNewline(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.vimEnabled = true
	m.vimMode = vimNormal
	m.input.SetValue("first")
	m.moveCursorToOffset(len([]rune("first")))

	m, _ = m.Update(keyRune('i'))
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	for _, r := range "second" {
		m, _ = m.Update(keyRune(r))
	}

	if got := m.input.Value(); got != "first\nsecond" {
		t.Fatalf("multiline draft = %q, want first\\nsecond", got)
	}
}

func TestVimVisualDeleteAndPasteUsesInternalRegister(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.vimEnabled = true
	m.vimMode = vimNormal
	m.input.SetValue("alpha beta")
	m.moveCursorToOffset(0)

	m, _ = m.Update(keyRune('w'))
	m, _ = m.Update(keyRune('v'))
	m, _ = m.Update(keyRune('$'))
	m, _ = m.Update(keyRune('d'))
	if got := m.input.Value(); got != "alpha " {
		t.Fatalf("visual delete = %q, want alpha space", got)
	}

	m, _ = m.Update(keyRune('p'))
	if got := m.input.Value(); got != "alpha beta" {
		t.Fatalf("register paste = %q, want alpha beta", got)
	}
}

func TestPlainComposerSelectAllAndPasteReplacesSelection(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.input.SetValue("old text")
	m.moveCursorToOffset(0)

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyCtrlA})
	if !m.selectionActive {
		t.Fatal("Ctrl+A did not create a selection")
	}
	m, _ = m.Update(clipboardPasteMsg{text: "new text"})
	if got := m.input.Value(); got != "new text" {
		t.Fatalf("pasted draft = %q, want new text", got)
	}
}
