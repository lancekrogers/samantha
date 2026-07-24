package tui

import (
	"fmt"
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
	// Edit submits always persist the stack too; stub so the test never
	// touches a real personas dir (absent on CI, present on dev machines).
	m.saveStack = func(id string, b persona.Brain, tt persona.TTS) (*persona.Profile, error) {
		return &persona.Profile{ID: id, Brain: b, TTS: tt}, nil
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

func TestPersonasCreateViaNKey(t *testing.T) {
	cfg := &config.Config{ActivePersona: "samantha", AgentName: "Samantha"}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{{ID: "samantha", DisplayName: "Samantha"}}, nil
	}
	m.defaultPrompt = func() (string, error) { return "You are {agent_name}.", nil }
	m.reload()
	m.width, m.height = 80, 24
	// Cursor still on first persona — n must open create without scrolling.
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	if m.formMode != "create" {
		t.Fatalf("formMode = %q, want create", m.formMode)
	}
	if !strings.Contains(m.promptTA.Value(), "agent_name") {
		t.Fatalf("default system prompt not loaded: %q", m.promptTA.Value())
	}
}

func TestPersonasFormSavesWithoutRelyingOnCtrlS(t *testing.T) {
	// Regression: many terminals swallow ctrl+s (XOFF). Save must also work via
	// keys that Bubble Tea actually receives (ctrl+j, alt+s, f2).
	cases := []struct {
		name string
		msg  tea.KeyMsg
	}{
		{"ctrl+j", tea.KeyMsg{Type: tea.KeyCtrlJ}},
		{"alt+s", tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}, Alt: true}},
		{"f2", tea.KeyMsg{Type: tea.KeyF2}},
		{"ctrl+s", tea.KeyMsg{Type: tea.KeyCtrlS}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.msg.String(); got != tc.name {
				t.Fatalf("KeyMsg.String() = %q, want %q", got, tc.name)
			}
			cfg := &config.Config{ActivePersona: "samantha", AgentName: "Samantha"}
			m := newPersonas(cfg)
			m.listPersonas = func() ([]*persona.Profile, error) {
				return []*persona.Profile{{ID: "samantha", DisplayName: "Samantha"}}, nil
			}
			m.defaultPrompt = func() (string, error) { return "You are {agent_name}.", nil }
			var gotOpts persona.CreateOpts
			m.createPersona = func(c *config.Config, opts persona.CreateOpts) (*persona.Profile, error) {
				gotOpts = opts
				return &persona.Profile{ID: "buddy", DisplayName: opts.DisplayName}, nil
			}
			m.reload()
			m.width, m.height = 80, 24
			m.beginCreate()
			m.nameInput.SetValue("Buddy")
			m.promptTA.SetValue("You are {agent_name}, buddy.")
			m.formStep = personaFormPrompt
			m, _ = m.updateForm(tc.msg)
			if m.formMode != "" {
				t.Fatalf("form still open after %s (save not handled)", tc.name)
			}
			if gotOpts.DisplayName != "Buddy" {
				t.Fatalf("name = %q", gotOpts.DisplayName)
			}
			if !strings.Contains(gotOpts.SystemPrompt, "buddy") {
				t.Fatalf("prompt = %q", gotOpts.SystemPrompt)
			}
		})
	}
}

func TestPersonasFormKeepsSystemPromptVisibleOnShortHeight(t *testing.T) {
	cfg := &config.Config{ActivePersona: "samantha", AgentName: "Samantha"}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{{ID: "samantha", DisplayName: "Samantha"}}, nil
	}
	m.defaultPrompt = func() (string, error) { return "You are {agent_name}.", nil }
	m.reload()
	// Short terminal used to pad-truncate the form and hide the prompt field.
	m.width, m.height = 80, 14
	m.beginCreate()
	view := stripANSI(m.View())
	if !strings.Contains(view, "System prompt") {
		t.Fatalf("system prompt field missing at height 14:\n%s", view)
	}
	if !strings.Contains(view, "ctrl+j") && !strings.Contains(view, "alt+s") {
		t.Fatalf("save help missing:\n%s", view)
	}
}

func TestPersonasCreateNameOnlyUsesStarterPrompt(t *testing.T) {
	cfg := &config.Config{ActivePersona: "samantha", AgentName: "Samantha"}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{{ID: "samantha", DisplayName: "Samantha"}}, nil
	}
	m.defaultPrompt = func() (string, error) { return "You are {agent_name}, default.", nil }
	m.starterPrompt = func() (string, error) { return "You are {agent_name}, starter.", nil }
	var gotOpts persona.CreateOpts
	m.createPersona = func(c *config.Config, opts persona.CreateOpts) (*persona.Profile, error) {
		gotOpts = opts
		return &persona.Profile{ID: "named", DisplayName: opts.DisplayName}, nil
	}
	m.reload()
	m.beginCreate()
	m.nameInput.SetValue("Named Only")
	m.promptTA.SetValue("") // empty — should fall back to the starter seed
	m, _ = m.submitForm()
	if m.formMode != "" {
		t.Fatal("form should close")
	}
	if !strings.Contains(gotOpts.SystemPrompt, "starter") {
		t.Fatalf("expected starter prompt, got %q", gotOpts.SystemPrompt)
	}
}

func TestPersonasCreateSeedsStarterNotFullDefault(t *testing.T) {
	cfg := &config.Config{ActivePersona: "samantha", AgentName: "Samantha"}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{{ID: "samantha", DisplayName: "Samantha"}}, nil
	}
	m.defaultPrompt = func() (string, error) { return "You are {agent_name}, the full built-in identity.", nil }
	m.starterPrompt = func() (string, error) { return "You are {agent_name}, starter.", nil }
	m.reload()
	m.beginCreate()
	if got := m.promptTA.Value(); !strings.Contains(got, "starter") || strings.Contains(got, "built-in") {
		t.Fatalf("create should seed the starter, got %q", got)
	}
}

func TestPersonasEditFallbackKeepsFullDefault(t *testing.T) {
	// A persona whose prompt fails to load must fall back to the full default,
	// never the starter — otherwise saving would downgrade the identity.
	cfg := &config.Config{ActivePersona: "samantha", AgentName: "Samantha"}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{{ID: "samantha", DisplayName: "Samantha"}}, nil
	}
	m.loadPrompt = func(string) (string, error) { return "", fmt.Errorf("boom") }
	m.defaultPrompt = func() (string, error) { return "You are {agent_name}, the full built-in identity.", nil }
	m.starterPrompt = func() (string, error) { return "You are {agent_name}, starter.", nil }
	m.reload()
	m.cursor = 0
	m.beginEdit()
	if got := m.promptTA.Value(); !strings.Contains(got, "built-in") {
		t.Fatalf("edit fallback should be the full default, got %q", got)
	}
}

func TestPersonasFormStackRoundTrip(t *testing.T) {
	// The stack step prefills from the profile's brain/TTS and saves through
	// UpdateStack — the only write path for a persona's model/voice
	// (Settings writes global defaults only, WI-c8884d §5.2).
	cfg := &config.Config{ActivePersona: "research"}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{{
			ID: "research", DisplayName: "Research",
			Brain:   persona.Brain{Provider: "ollama", Model: "llama3"},
			TTS:     persona.TTS{Provider: "kokoro", Voice: "af_heart"},
			Prompts: persona.PromptRefs{Persona: "research"},
		}}, nil
	}
	m.loadPrompt = func(string) (string, error) { return "You are Research.", nil }
	m.reload()
	m.width, m.height = 80, 30
	m.cursor = 0
	m.beginEdit()

	if got := stackBrainProviders()[m.brainProviderIdx]; got != "ollama" {
		t.Fatalf("prefilled brain provider = %q, want ollama", got)
	}
	if m.brainModelInput.Value() != "llama3" || m.voiceInput.Value() != "af_heart" {
		t.Fatalf("prefill = model %q voice %q", m.brainModelInput.Value(), m.voiceInput.Value())
	}

	var gotBrain persona.Brain
	var gotTTS persona.TTS
	m.saveName = func(id, display string) (*persona.Profile, error) {
		return &persona.Profile{ID: id, DisplayName: display}, nil
	}
	m.savePrompt = func(id, p string) (*persona.Profile, error) {
		return &persona.Profile{ID: id}, nil
	}
	m.saveStack = func(id string, b persona.Brain, tt persona.TTS) (*persona.Profile, error) {
		gotBrain, gotTTS = b, tt
		return &persona.Profile{ID: id, Brain: b, TTS: tt}, nil
	}
	m.usePersona = func(*config.Config, string) error { return nil }

	m.brainModelInput.SetValue("qwen2.5:14b")
	m.voiceInput.SetValue("Ryan")
	m, _ = m.submitForm()

	if gotBrain.Provider != "ollama" || gotBrain.Model != "qwen2.5:14b" {
		t.Fatalf("saved brain = %+v", gotBrain)
	}
	if gotTTS.Provider != "kokoro" || gotTTS.Voice != "Ryan" {
		t.Fatalf("saved tts = %+v", gotTTS)
	}
	if m.formMode != "" {
		t.Fatal("form should close after save")
	}
}
