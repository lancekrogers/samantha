package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/persona"
)

func vimKey(s string) tea.KeyMsg {
	switch s {
	case "esc":
		return tea.KeyMsg{Type: tea.KeyEsc}
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	case "backspace":
		return tea.KeyMsg{Type: tea.KeyBackspace}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
	}
}

func vimSeq(t *testing.T, v vimTextarea, keys ...string) (vimTextarea, vimTAEvent) {
	t.Helper()
	last := vimTAEventNone
	for _, k := range keys {
		var ev vimTAEvent
		v, _, ev = v.Update(vimKey(k))
		if ev != vimTAEventNone {
			last = ev
		}
	}
	return v, last
}

func newTestVim(value string) vimTextarea {
	v := newPersonaPromptArea()
	v.SetValue(value)
	v.StartInsert()
	v.Focus()
	return v
}

func TestVimTextareaModalTransitions(t *testing.T) {
	v := newTestVim("")

	// Insert mode types text.
	v, _ = vimSeq(t, v, "h", "i")
	if v.Value() != "hi" {
		t.Fatalf("insert typing = %q, want hi", v.Value())
	}
	// Esc → normal: h/i become motions/mode keys, not text.
	v, _ = vimSeq(t, v, "esc")
	if v.Mode() != vimTANormal {
		t.Fatal("esc did not enter normal mode")
	}
	v, _ = vimSeq(t, v, "h", "h")
	if v.Value() != "hi" {
		t.Fatalf("normal-mode motion typed text: %q", v.Value())
	}
	// i returns to insert at cursor (line start after hh) — typing prepends.
	v, _ = vimSeq(t, v, "i", "y", "o", " ")
	if v.Value() != "yo hi" {
		t.Fatalf("insert after normal = %q, want 'yo hi'", v.Value())
	}
}

func TestVimTextareaNormalModeEditing(t *testing.T) {
	v := newTestVim("alpha\nbravo\ncharlie")

	// G → last line; dd deletes it.
	v, _ = vimSeq(t, v, "esc", "G", "d", "d")
	if got := v.Value(); got != "alpha\nbravo" {
		t.Fatalf("dd on last line = %q, want alpha\\nbravo", got)
	}
	// gg → first line; x deletes the first rune.
	v, _ = vimSeq(t, v, "g", "g", "x")
	if got := v.Value(); !strings.HasPrefix(got, "lpha") {
		t.Fatalf("x at top = %q, want leading 'lpha'", got)
	}
	// dd in the middle keeps surrounding lines joined correctly.
	v, _ = vimSeq(t, v, "d", "d")
	if got := v.Value(); got != "bravo" {
		t.Fatalf("dd first line = %q, want bravo", got)
	}
}

func TestVimTextareaExCommands(t *testing.T) {
	v := newTestVim("body")

	// :w saves.
	v, ev := vimSeq(t, v, "esc", ":", "w", "enter")
	if ev != vimTAEventSave {
		t.Fatalf("':w' event = %d, want save", ev)
	}
	// :q cancels.
	v, ev = vimSeq(t, v, ":", "q", "enter")
	if ev != vimTAEventCancel {
		t.Fatalf("':q' event = %d, want cancel", ev)
	}
	// Unknown command is a no-op; esc aborts the ex buffer.
	v, ev = vimSeq(t, v, ":", "z", "enter")
	if ev != vimTAEventNone {
		t.Fatalf("':z' event = %d, want none", ev)
	}
	v, _ = vimSeq(t, v, ":", "w")
	if buf, active := v.ExBuffer(); !active || buf != "w" {
		t.Fatalf("ex buffer = %q active=%v", buf, active)
	}
	v, ev = vimSeq(t, v, "esc")
	if _, active := v.ExBuffer(); active || ev != vimTAEventNone {
		t.Fatal("esc did not abort the ex buffer")
	}
	// Normal-mode esc (no ex pending) requests form cancel.
	_, ev = vimSeq(t, v, "esc")
	if ev != vimTAEventCancel {
		t.Fatalf("normal esc event = %d, want cancel", ev)
	}
}

func TestPersonasPromptStepVimFlow(t *testing.T) {
	// Form-level integration: esc drops to normal (form stays open), :w
	// submits, and a normal-mode esc cancels the form.
	cfg := &config.Config{ActivePersona: "samantha", AgentName: "Samantha"}
	newForm := func() personasModel {
		m := newPersonas(cfg)
		m.listPersonas = func() ([]*persona.Profile, error) {
			return []*persona.Profile{{ID: "samantha", DisplayName: "Samantha", Prompts: persona.PromptRefs{Persona: "samantha"}}}, nil
		}
		m.loadPrompt = func(string) (string, error) { return "You are Samantha.", nil }
		m.saveName = func(id, d string) (*persona.Profile, error) { return &persona.Profile{ID: id, DisplayName: d}, nil }
		m.saveStack = func(id string, b persona.Brain, tt persona.TTS) (*persona.Profile, error) {
			return &persona.Profile{ID: id}, nil
		}
		m.usePersona = func(*config.Config, string) error { return nil }
		m.reload()
		m.width, m.height = 80, 30
		m.cursor = 0
		m.beginEdit()
		m.formStep = personaFormPrompt
		m.promptTA.StartInsert()
		return m
	}

	// esc in insert → normal, form still open.
	m := newForm()
	m, _ = m.updateForm(vimKey("esc"))
	if m.formMode == "" {
		t.Fatal("insert-mode esc must not cancel the form")
	}
	if m.promptTA.Mode() != vimTANormal {
		t.Fatal("esc did not reach the vim layer")
	}

	// :w submits (savePrompt runs).
	var saved bool
	m.savePrompt = func(id, p string) (*persona.Profile, error) {
		saved = true
		return &persona.Profile{ID: id}, nil
	}
	m, _ = m.updateForm(vimKey(":"))
	m, _ = m.updateForm(vimKey("w"))
	m, _ = m.updateForm(vimKey("enter"))
	if !saved || m.formMode != "" {
		t.Fatalf("':w' saved=%v formMode=%q, want saved+closed", saved, m.formMode)
	}

	// normal-mode esc cancels the form.
	m = newForm()
	m, _ = m.updateForm(vimKey("esc")) // → normal
	m, _ = m.updateForm(vimKey("esc")) // → cancel
	if m.formMode != "" {
		t.Fatal("normal-mode esc must cancel the form")
	}
}
