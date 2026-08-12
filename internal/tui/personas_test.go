package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
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
	m.loadPromptForProfile = func(p *persona.Profile) (string, error) {
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

func TestPersonasEditFallbackInjectsNothing(t *testing.T) {
	// Supersedes TestPersonasEditFallbackKeepsFullDefault: that test asserted a
	// failed load seeds the full built-in identity, which is exactly the silent
	// wrong-identity injection this branch removes. Its intent — never let a
	// failed load downgrade or replace a persona's identity — is preserved more
	// strictly here: the editor stays empty and says so, and saving is the
	// user's explicit act rather than an accidental overwrite.
	cfg := &config.Config{ActivePersona: "uncle-fu", AgentName: "Uncle Fu"}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{{ID: "uncle-fu", DisplayName: "Uncle Fu"}}, nil
	}
	m.loadPromptForProfile = func(*persona.Profile) (string, error) { return "", fmt.Errorf("boom") }
	m.defaultPrompt = func() (string, error) { return "You are {agent_name}, the full built-in identity.", nil }
	m.starterPrompt = func() (string, error) { return "You are {agent_name}, starter.", nil }
	m.reload()
	m.width, m.height = 80, 30
	m.cursor = 0
	m.beginEdit()

	if got := m.promptTA.Value(); got != "" {
		t.Fatalf("edit fallback injected a prompt body: %q", got)
	}
	if !strings.Contains(m.message, "Could not load system prompt") {
		t.Fatalf("message = %q, want the load failure surfaced", m.message)
	}
}

func TestPersonasEditDoesNotInjectSamanthaForWrongRef(t *testing.T) {
	// Regression: stale prompts.persona (e.g. TTS voice "Uncle_Fu") used to
	// resolve as a miss and the editor filled in the embedded samantha default.
	cfg := &config.Config{ActivePersona: "uncle-fu"}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{{
			ID: "uncle-fu", DisplayName: "uncle fu",
			Prompts: persona.PromptRefs{Persona: "Uncle_Fu", Turn: "samantha"},
		}}, nil
	}
	m.loadPromptForProfile = func(p *persona.Profile) (string, error) {
		if p.ID == "uncle-fu" {
			return "You are Uncle Fu, private prompt.", nil
		}
		return "", fmt.Errorf("unexpected id %q", p.ID)
	}
	m.defaultPrompt = func() (string, error) {
		return "You are {agent_name}, the samantha default — must not appear.", nil
	}
	m.reload()
	m.width, m.height = 80, 28
	m.cursor = 0
	m.beginEdit()
	if strings.Contains(m.promptTA.Value(), "samantha default") {
		t.Fatalf("editor injected samantha default: %q", m.promptTA.Value())
	}
	if !strings.Contains(m.promptTA.Value(), "Uncle Fu, private") {
		t.Fatalf("editor missing private prompt: %q", m.promptTA.Value())
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
	m.loadPromptForProfile = func(*persona.Profile) (string, error) { return "You are Research.", nil }
	m.reload()
	m.width, m.height = 80, 30
	m.cursor = 0
	m.beginEdit()

	if got := stackBrainProviders()[m.brainProviderIdx]; got != "ollama" {
		t.Fatalf("prefilled brain provider = %q, want ollama", got)
	}
	if m.brainModelInput.Value() != "llama3" || m.selectedVoice != "af_heart" {
		t.Fatalf("prefill = model %q voice %q", m.brainModelInput.Value(), m.selectedVoice)
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
	// Voice is a catalog picker, not free text: switch TTS to qwen3-tts and
	// select Ryan with ←/→ rather than typing a string.
	m.ttsProviderIdx = providerIndex(stackTTSProviders(), "qwen3-tts")
	m.selectedVoice = "Ryan"
	m, _ = m.submitForm()

	if gotBrain.Provider != "ollama" || gotBrain.Model != "qwen2.5:14b" {
		t.Fatalf("saved brain = %+v", gotBrain)
	}
	if gotTTS.Provider != "qwen3-tts" || gotTTS.Voice != "Ryan" {
		t.Fatalf("saved tts = %+v", gotTTS)
	}
	if m.formMode != "" {
		t.Fatal("form should close after save")
	}
}

func TestPersonasStackVoiceIsSelectable(t *testing.T) {
	// Voice must be cycled with ←/→ like providers — not a free-text field.
	cfg := &config.Config{TTSProvider: "kokoro", TTSVoice: "af_heart"}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{{
			ID: "research", DisplayName: "Research",
			TTS: persona.TTS{Provider: "kokoro", Voice: "af_heart"},
		}}, nil
	}
	m.loadPromptForProfile = func(*persona.Profile) (string, error) { return "hi", nil }
	m.reload()
	m.width, m.height = 80, 30
	m.cursor = 0
	m.beginEdit()
	m, _ = m.focusStackStep()

	// Land on the Voice row and cycle away from af_heart.
	m.setStackRow(stackRowVoice)
	before := m.selectedVoice
	if before != "af_heart" {
		t.Fatalf("selectedVoice = %q, want af_heart from profile", before)
	}
	view := stripANSI(m.View())
	if !strings.Contains(view, "‹ af_heart ›") {
		t.Fatalf("voice row should render as selectable, got:\n%s", view)
	}
	m, _ = m.updateStackStep(tea.KeyMsg{Type: tea.KeyRight})
	if m.selectedVoice == before {
		t.Fatal("←/→ on Voice row did not change the selection")
	}
	// Typing must not mutate the voice selection (no free-text field).
	typed := m.selectedVoice
	m, _ = m.updateStackStep(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'x'}})
	if m.selectedVoice != typed {
		t.Fatalf("typing changed voice to %q; voice is select-only", m.selectedVoice)
	}

	// Changing TTS provider drops a voice that is not in the new catalog.
	m.selectedVoice = "af_bella"
	m.setStackRow(stackRowTTSProvider)
	// Cycle until qwen3-tts is selected.
	for i := 0; i < len(stackTTSProviders()); i++ {
		if providerAt(stackTTSProviders(), m.ttsProviderIdx) == "qwen3-tts" {
			break
		}
		m, _ = m.updateStackStep(tea.KeyMsg{Type: tea.KeyRight})
	}
	if providerAt(stackTTSProviders(), m.ttsProviderIdx) != "qwen3-tts" {
		t.Fatal("failed to select qwen3-tts provider")
	}
	if m.selectedVoice != "" {
		t.Fatalf("kokoro voice should clear under qwen3-tts, got %q", m.selectedVoice)
	}
	// Qwen catalog is selectable.
	m.setStackRow(stackRowVoice)
	m, _ = m.updateStackStep(tea.KeyMsg{Type: tea.KeyRight})
	if m.selectedVoice == "" {
		t.Fatal("expected a qwen preset after cycling voice")
	}
	list := m.stackVoiceList()
	found := false
	for _, name := range list {
		if name == m.selectedVoice {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("selected voice %q not in list %v", m.selectedVoice, list)
	}
}

// Placeholder completion must be reachable through the form, not just by driving
// promptEditor directly: updateForm claimed tab for field navigation, so `{`+Tab
// jumped to the Model & voice step instead of completing.
func TestPersonasPromptStepTabCompletesPlaceholder(t *testing.T) {
	cfg := &config.Config{ActivePersona: "samantha", AgentName: "Samantha"}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{{ID: "samantha", DisplayName: "Samantha"}}, nil
	}
	m.starterPrompt = func() (string, error) { return "", nil }
	m.defaultPrompt = func() (string, error) { return "", nil }
	m.reload()
	m.width, m.height = 80, 30
	m.beginCreate()
	m.nameInput.SetValue("Tabby")
	m, _ = m.focusPromptStep()
	m.promptTA.SetValue("")
	m.promptTA.StartInsert()

	for _, r := range "You are {" {
		m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyTab})
	if m.formStep != personaFormPrompt {
		t.Fatalf("tab left the prompt step (step=%d); completion is unreachable", m.formStep)
	}
	if !strings.Contains(m.promptTA.Value(), "{agent_name") {
		t.Fatalf("tab did not complete the placeholder: %q", m.promptTA.Value())
	}
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(m.promptTA.Value(), "{agent_name}") {
		t.Fatalf("enter did not close the token: %q", m.promptTA.Value())
	}
}

// With nothing to complete, tab still moves between fields.
func TestPersonasPromptStepTabStillNavigatesFields(t *testing.T) {
	cfg := &config.Config{ActivePersona: "samantha", AgentName: "Samantha"}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{{ID: "samantha", DisplayName: "Samantha"}}, nil
	}
	m.starterPrompt = func() (string, error) { return "You are {agent_name}.", nil }
	m.reload()
	m.width, m.height = 80, 30
	m.beginCreate()
	m.nameInput.SetValue("Tabby")
	m, _ = m.focusPromptStep()

	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyTab})
	if m.formStep != personaFormStack {
		t.Fatalf("tab with no open brace should advance to the stack step, got %d", m.formStep)
	}
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.formStep != personaFormPrompt {
		t.Fatalf("shift+tab should return to the prompt step, got %d", m.formStep)
	}
}

// :w must accept the prompt and land on Model & voice — not submit the form.
// Submitting closed the wizard and made the next step unreachable.
func TestPersonasColonWAdvancesToStackStep(t *testing.T) {
	cfg := &config.Config{ActivePersona: "samantha", AgentName: "Samantha"}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{{ID: "samantha", DisplayName: "Samantha"}}, nil
	}
	created := false
	m.createPersona = func(*config.Config, persona.CreateOpts) (*persona.Profile, error) {
		created = true
		return &persona.Profile{ID: "x"}, nil
	}
	m.starterPrompt = func() (string, error) { return "You are {agent_name}.", nil }
	m.reload()
	m.width, m.height = 80, 30
	m.beginCreate()
	m.nameInput.SetValue("Buddy")
	m, _ = m.focusPromptStep()
	// INSERT → NORMAL, then :w <enter>
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyEscape})
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{':'}},
		{Type: tea.KeyRunes, Runes: []rune{'w'}},
		{Type: tea.KeyEnter},
	} {
		m, _ = m.updateForm(k)
	}
	if created {
		t.Fatal(":w submitted the form; it should only advance to Model & voice")
	}
	if m.formMode != "create" {
		t.Fatalf("formMode = %q, want create still open", m.formMode)
	}
	if m.formStep != personaFormStack {
		t.Fatalf("formStep = %d, want stack (Model & voice)", m.formStep)
	}
	// Stack must accept input after leaving the vim editor.
	before := m.brainProviderIdx
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRight})
	if m.brainProviderIdx == before {
		t.Fatal("stack step did not respond to ←/→ after :w")
	}
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyDown})
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if m.brainModelInput.Value() != "q" {
		t.Fatalf("model input = %q, want typed char after :w", m.brainModelInput.Value())
	}
}

// :wq still commits the whole form from the prompt step.
func TestPersonasColonWQSubmitsForm(t *testing.T) {
	cfg := &config.Config{ActivePersona: "samantha", AgentName: "Samantha"}
	m := newPersonas(cfg)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{{ID: "samantha", DisplayName: "Samantha"}}, nil
	}
	var gotOpts persona.CreateOpts
	m.createPersona = func(c *config.Config, opts persona.CreateOpts) (*persona.Profile, error) {
		gotOpts = opts
		return &persona.Profile{ID: "buddy", DisplayName: opts.DisplayName}, nil
	}
	m.starterPrompt = func() (string, error) { return "You are {agent_name}.", nil }
	m.reload()
	m.width, m.height = 80, 30
	m.beginCreate()
	m.nameInput.SetValue("Buddy")
	m.promptTA.SetValue("You are {agent_name}, buddy.")
	m, _ = m.focusPromptStep()
	m, _ = m.updateForm(tea.KeyMsg{Type: tea.KeyEscape})
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyRunes, Runes: []rune{':'}},
		{Type: tea.KeyRunes, Runes: []rune{'w'}},
		{Type: tea.KeyRunes, Runes: []rune{'q'}},
		{Type: tea.KeyEnter},
	} {
		m, _ = m.updateForm(k)
	}
	if m.formMode != "" {
		t.Fatal(":wq should submit and close the form")
	}
	if gotOpts.DisplayName != "Buddy" {
		t.Fatalf("name = %q", gotOpts.DisplayName)
	}
}

// The persona form sizes the prompt box from the editor height, so a wrapped
// editor used to render more rows than budgeted and push the Model & voice box
// and the help line off screen. Measured before the fix: 46 rows into 30.
func TestPersonasFormFitsTerminalWithLongPromptLines(t *testing.T) {
	paragraph := strings.Repeat("This is a long unwrapped paragraph of persona guidance a real user would write. ", 3)
	body := strings.Join([]string{paragraph, "", paragraph, "", paragraph, "", paragraph}, "\n")

	for _, size := range []struct{ w, h int }{{100, 40}, {80, 30}, {80, 24}, {60, 20}} {
		cfg := &config.Config{ActivePersona: "samantha", AgentName: "Samantha"}
		m := newPersonas(cfg)
		m.listPersonas = func() ([]*persona.Profile, error) {
			return []*persona.Profile{{ID: "samantha", DisplayName: "Samantha"}}, nil
		}
		m.starterPrompt = func() (string, error) { return body, nil }
		m.defaultPrompt = func() (string, error) { return body, nil }
		m.reload()
		m.width, m.height = size.w, size.h
		m.beginCreate()
		m, _ = m.focusPromptStep()

		if got := len(m.formLines()); got > size.h {
			t.Errorf("%dx%d: form is %d rows, exceeds the %d available",
				size.w, size.h, got, size.h)
		}
	}
}
