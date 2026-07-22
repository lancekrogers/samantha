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

func TestPersonasScreenCreateWithSystemPrompt(t *testing.T) {
	cfg := &config.Config{ActivePersona: "samantha", AgentName: "Samantha"}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{
			{ID: "samantha", DisplayName: "Samantha"},
		}, nil
	}
	m.defaultPrompt = func() (string, error) { return "You are {agent_name}.", nil }
	m.reload()
	m.width, m.height = 80, 28
	m.cursor = len(m.items) // create row
	m.selectCurrent()
	if m.formMode != "create" {
		t.Fatal("expected create form")
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "Create a new voice agent") || !strings.Contains(view, "System prompt") {
		t.Fatalf("create form missing prompt field:\n%s", view)
	}

	var gotOpts persona.CreateOpts
	m.createPersona = func(c *config.Config, opts persona.CreateOpts) (*persona.Profile, error) {
		gotOpts = opts
		c.ActivePersona = "research-buddy"
		c.AgentName = opts.DisplayName
		return &persona.Profile{ID: "research-buddy", DisplayName: opts.DisplayName}, nil
	}
	m.nameInput.SetValue("Research Buddy")
	m.promptTA.SetValue("You are {agent_name}, a research agent.")
	m, _ = m.submitForm()
	if gotOpts.DisplayName != "Research Buddy" {
		t.Fatalf("name = %q", gotOpts.DisplayName)
	}
	if !strings.Contains(gotOpts.SystemPrompt, "research agent") {
		t.Fatalf("prompt = %q", gotOpts.SystemPrompt)
	}
	if m.formMode != "" {
		t.Fatal("form should close after save")
	}
}

func TestPersonasScreenEditSystemPrompt(t *testing.T) {
	cfg := &config.Config{ActivePersona: "samantha", AgentName: "Samantha"}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{
			{ID: "samantha", DisplayName: "Samantha", Prompts: persona.PromptRefs{Persona: "samantha"}},
		}, nil
	}
	m.loadPrompt = func(name string) (string, error) {
		return "You are {agent_name}, original.", nil
	}
	m.reload()
	m.width, m.height = 80, 28
	m.cursor = 0
	m.beginEdit()
	if m.formMode != "edit" || m.editID != "samantha" {
		t.Fatalf("edit mode = %q id = %q", m.formMode, m.editID)
	}
	if !strings.Contains(m.promptTA.Value(), "original") {
		t.Fatalf("prompt not loaded: %q", m.promptTA.Value())
	}

	var savedPrompt string
	m.saveName = func(id, display string) (*persona.Profile, error) {
		return &persona.Profile{ID: id, DisplayName: display}, nil
	}
	m.savePrompt = func(id, systemPrompt string) (*persona.Profile, error) {
		savedPrompt = systemPrompt
		return &persona.Profile{ID: id, Prompts: persona.PromptRefs{Persona: id}}, nil
	}
	m.promptTA.SetValue("You are {agent_name}, revised.")
	m, _ = m.submitForm()
	if !strings.Contains(savedPrompt, "revised") {
		t.Fatalf("saved prompt = %q", savedPrompt)
	}
	if m.formMode != "" {
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
