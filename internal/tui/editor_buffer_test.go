package tui

import "testing"

func TestEditorBufferSelectionDeleteAndUndo(t *testing.T) {
	b := newEditorBuffer("alpha beta")
	b.setCursor(6)
	b.beginSelection(false)
	b.setCursor(9)

	deleted, changed := b.deleteSelection()
	if !changed || deleted != "beta" {
		t.Fatalf("deleteSelection() = %q, %v; want beta, true", deleted, changed)
	}
	if got := b.text(); got != "alpha " {
		t.Fatalf("buffer after delete = %q, want alpha space", got)
	}
	if got := b.registerValue().text; got != "beta" {
		t.Fatalf("register = %q, want beta", got)
	}

	if !b.undoEdit() || b.text() != "alpha beta" {
		t.Fatalf("undo did not restore deleted text: %q", b.text())
	}
}

func TestEditorBufferLinewiseSelectionAndMotion(t *testing.T) {
	b := newEditorBuffer("first\nsecond\nthird")
	b.setCursor(0)
	b.beginSelection(true)
	b.move(motionDown)
	if got := b.selectedText(); got != "first\nsecond\n" {
		t.Fatalf("linewise selection = %q, want first and second lines", got)
	}

	if got := b.lineStart(8); got != 6 {
		t.Fatalf("lineStart(8) = %d, want 6", got)
	}
	b.clearSelection()
	b.setCursor(0)
	b.move(motionWordForward)
	if got := b.cursorOffset(); got != 6 {
		t.Fatalf("word motion cursor = %d, want 6", got)
	}
}

func TestEditorBufferReplaceAndRegisterPasteAreIndependent(t *testing.T) {
	b := newEditorBuffer("one two")
	b.setRegister("copy", false)
	b.replaceRange(0, 3, "a")
	if got := b.text(); got != "a two" {
		t.Fatalf("replaceRange() = %q, want a two", got)
	}
	if got := b.registerValue().text; got != "copy" {
		t.Fatalf("replaceRange changed register to %q", got)
	}
	b.insertAt(2, b.registerValue().text, false)
	if got := b.text(); got != "a copytwo" {
		t.Fatalf("register insert = %q, want a copytwo", got)
	}
}
