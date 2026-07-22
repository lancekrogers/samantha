package tui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/persona"
)

func TestPersonasScreenListsAndSwitches(t *testing.T) {
	cfg := &config.Config{
		ActivePersona: "samantha",
		AgentName:     "Samantha",
		TTSProvider:   "kokoro",
		TTSVoice:      "af_heart",
	}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{
			{ID: "samantha", DisplayName: "Samantha", TTS: persona.TTS{Provider: "kokoro", Voice: "af_heart"}},
			{ID: "festival", DisplayName: "Festival", TTS: persona.TTS{Provider: "kokoro", Voice: "af_bella"}},
		}, nil
	}
	m.reload()
	m.width, m.height = 80, 24

	view := stripANSI(m.View())
	if !strings.Contains(view, "Personas") || !strings.Contains(view, "Festival") {
		t.Fatalf("personas view missing rows:\n%s", view)
	}
	if !strings.Contains(view, personasCreateLabel) {
		t.Fatalf("personas view missing create row:\n%s", view)
	}

	var used string
	m.usePersona = func(c *config.Config, id string) error {
		used = id
		c.ActivePersona = id
		c.AgentName = "Festival"
		return nil
	}
	m.cursor = 1
	m.selectCurrent()
	if used != "festival" {
		t.Fatalf("usePersona = %q", used)
	}
	if !strings.Contains(m.message, "Festival") {
		t.Fatalf("message = %q", m.message)
	}
}

func TestPersonasScreenCreateForm(t *testing.T) {
	cfg := &config.Config{ActivePersona: "samantha", AgentName: "Samantha"}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{
			{ID: "samantha", DisplayName: "Samantha"},
		}, nil
	}
	m.reload()
	m.width, m.height = 80, 24
	m.cursor = len(m.items) // create row
	m.selectCurrent()
	if !m.creating {
		t.Fatal("expected create form")
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "Create a new voice agent") {
		t.Fatalf("create form missing:\n%s", view)
	}

	var created string
	m.createPersona = func(c *config.Config, name string) (*persona.Profile, error) {
		created = name
		c.ActivePersona = "research-buddy"
		c.AgentName = name
		return &persona.Profile{ID: "research-buddy", DisplayName: name}, nil
	}
	m.create.SetValue("Research Buddy")
	m, _ = m.updateCreate(tea.KeyMsg{Type: tea.KeyEnter})
	if created != "Research Buddy" {
		t.Fatalf("created = %q", created)
	}
	if m.creating {
		t.Fatal("form should close")
	}
}

func TestLauncherOffersPersonas(t *testing.T) {
	m := newLauncher(&config.Config{AgentName: "Samantha"}, nil)
	for i, item := range m.items {
		if item.action != actionPersonas {
			continue
		}
		if item.label != "Personas" {
			t.Fatalf("label = %q", item.label)
		}
		if !strings.Contains(item.hint, "Samantha") {
			t.Fatalf("hint = %q, want active name", item.hint)
		}
		m.cursor = i
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		msg, ok := cmd().(switchScreenMsg)
		if !ok || screen(msg) != screenPersonas {
			t.Fatalf("message = %#v", msg)
		}
		return
	}
	t.Fatal("launcher missing Personas action")
}
