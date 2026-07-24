package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/internal/tts"
)

const (
	personaFormName   = 0
	personaFormPrompt = 1
	personaFormStack  = 2
)

// Stack step rows.
const (
	stackRowBrainProvider = 0
	stackRowBrainModel    = 1
	stackRowTTSProvider   = 2
	stackRowVoice         = 3
	stackRowCount         = 4
)

// stackDefaultLabel is provider index 0: inherit the app-level default.
const stackDefaultLabel = "(default)"

// stackBrainProviders lists selectable brain providers for the form.
func stackBrainProviders() []string {
	out := []string{stackDefaultLabel}
	for _, spec := range brain.Providers() {
		out = append(out, spec.Name)
	}
	return out
}

// stackTTSProviders lists selectable TTS providers for the form.
func stackTTSProviders() []string {
	out := []string{stackDefaultLabel}
	for _, spec := range tts.Providers() {
		out = append(out, spec.Name)
	}
	return out
}

// providerIndex maps a stored provider name onto its list index (0 = default).
func providerIndex(list []string, name string) int {
	name = strings.TrimSpace(name)
	if name == "" {
		return 0
	}
	for i, item := range list {
		if strings.EqualFold(item, name) {
			return i
		}
	}
	return 0
}

// providerAt returns the provider name for a list index; "" for the default.
func providerAt(list []string, idx int) string {
	if idx <= 0 || idx >= len(list) {
		return ""
	}
	return list[idx]
}

func newPersonaStackInput(prompt, placeholder string) textinput.Model {
	ti := textinput.New()
	ti.Prompt = "  " + prompt
	ti.Placeholder = placeholder
	ti.CharLimit = 64
	ti.Width = 40
	return ti
}

func newPersonaPromptArea() textarea.Model {
	ta := textarea.New()
	ta.Placeholder = "System prompt / personality for this voice agent…"
	ta.CharLimit = 8000
	ta.SetWidth(60)
	ta.SetHeight(8)
	ta.ShowLineNumbers = false
	// Match conversation: insert newline with ctrl+j / ctrl+enter variants only if
	// we route those to the textarea. We intercept save keys before Update.
	return ta
}

func (m *personasModel) resizeForm() {
	w := max(m.width-8, 24)
	m.nameInput.Width = max(w-8, 20)
	m.promptTA.SetWidth(w)
	// Leave room for chrome (title, name field, labels, help) so the prompt
	// textarea is never clipped out of the form body.
	h := max(m.height-14, 3)
	if h > 12 {
		h = 12
	}
	m.promptTA.SetHeight(h)
}

// isPersonaFormSaveKey reports keys that commit the create/edit form.
// ctrl+s alone is unreliable: many terminals still implement software flow
// control (XOFF) and swallow it before Bubble Tea sees it. Prefer ctrl+j /
// ctrl+enter / alt+s / f2 — those actually reach the program.
func isPersonaFormSaveKey(key string) bool {
	switch key {
	case "ctrl+s", "ctrl+j", "ctrl+enter", "alt+enter", "alt+s", "f2":
		return true
	default:
		return false
	}
}

func (m personasModel) updateForm(msg tea.KeyMsg) (personasModel, tea.Cmd) {
	key := msg.String()
	switch {
	case key == "esc":
		m.cancelForm()
		m.message = "Edit cancelled"
		return m, nil
	case isPersonaFormSaveKey(key):
		return m.submitForm()
	case key == "tab":
		switch m.formStep {
		case personaFormName:
			return m.focusPromptStep()
		case personaFormPrompt:
			return m.focusStackStep()
		default:
			return m.focusNameStep()
		}
	case key == "shift+tab":
		switch m.formStep {
		case personaFormPrompt:
			return m.focusNameStep()
		case personaFormStack:
			return m.focusPromptStep()
		default:
			return m.focusStackStep()
		}
	case key == "enter":
		if m.formStep == personaFormName {
			// Name done → prompt. Save is intentionally not Enter (multi-line
			// prompt needs Enter for newlines).
			return m.focusPromptStep()
		}
		// Enter inserts newline in the prompt textarea (fall through);
		// on the stack step it advances rows.
		if m.formStep == personaFormStack {
			m.setStackRow((m.stackRow + 1) % stackRowCount)
			return m, nil
		}
	}
	if m.formStep == personaFormStack {
		return m.updateStackStep(msg)
	}

	var cmd tea.Cmd
	if m.formStep == personaFormName {
		m.nameInput, cmd = m.nameInput.Update(msg)
	} else {
		m.promptTA, cmd = m.promptTA.Update(msg)
	}
	return m, cmd
}

// updateStackStep routes keys inside the model/voice step: ↑/↓ move rows,
// ←/→ cycle providers on the provider rows, everything else types into the
// focused text row.
func (m personasModel) updateStackStep(msg tea.KeyMsg) (personasModel, tea.Cmd) {
	key := msg.String()
	switch key {
	case "up":
		m.setStackRow((m.stackRow + stackRowCount - 1) % stackRowCount)
		return m, nil
	case "down":
		m.setStackRow((m.stackRow + 1) % stackRowCount)
		return m, nil
	case "left", "right":
		delta := 1
		if key == "left" {
			delta = -1
		}
		switch m.stackRow {
		case stackRowBrainProvider:
			list := stackBrainProviders()
			m.brainProviderIdx = (m.brainProviderIdx + delta + len(list)) % len(list)
		case stackRowTTSProvider:
			list := stackTTSProviders()
			m.ttsProviderIdx = (m.ttsProviderIdx + delta + len(list)) % len(list)
		}
		if m.stackRow == stackRowBrainProvider || m.stackRow == stackRowTTSProvider {
			return m, nil
		}
	}

	var cmd tea.Cmd
	switch m.stackRow {
	case stackRowBrainModel:
		m.brainModelInput, cmd = m.brainModelInput.Update(msg)
	case stackRowVoice:
		m.voiceInput, cmd = m.voiceInput.Update(msg)
	}
	return m, cmd
}

// setStackRow moves focus between stack rows, blurring/focusing text inputs.
func (m *personasModel) setStackRow(row int) {
	m.stackRow = row
	m.brainModelInput.Blur()
	m.voiceInput.Blur()
	switch row {
	case stackRowBrainModel:
		m.brainModelInput.Focus()
	case stackRowVoice:
		m.voiceInput.Focus()
	}
}

func (m personasModel) focusStackStep() (personasModel, tea.Cmd) {
	m.formStep = personaFormStack
	m.nameInput.Blur()
	m.promptTA.Blur()
	m.setStackRow(stackRowBrainProvider)
	m.message = "Model & voice · ←/→ pick provider · ↑/↓ rows · (default) inherits Settings"
	return m, nil
}

// formBrain returns the stack step's brain selection ("" fields = inherit).
func (m *personasModel) formBrain() persona.Brain {
	return persona.Brain{
		Provider: providerAt(stackBrainProviders(), m.brainProviderIdx),
		Model:    strings.TrimSpace(m.brainModelInput.Value()),
	}
}

// formTTS returns the stack step's TTS selection ("" fields = inherit).
func (m *personasModel) formTTS() persona.TTS {
	return persona.TTS{
		Provider: providerAt(stackTTSProviders(), m.ttsProviderIdx),
		Voice:    strings.TrimSpace(m.voiceInput.Value()),
	}
}

// prefillStack seeds the stack step from a profile (nil = defaults).
func (m *personasModel) prefillStack(p *persona.Profile) {
	if p == nil {
		m.brainProviderIdx = 0
		m.ttsProviderIdx = 0
		m.brainModelInput.SetValue("")
		m.voiceInput.SetValue("")
		return
	}
	m.brainProviderIdx = providerIndex(stackBrainProviders(), p.Brain.Provider)
	m.ttsProviderIdx = providerIndex(stackTTSProviders(), p.TTS.Provider)
	m.brainModelInput.SetValue(strings.TrimSpace(p.Brain.Model))
	m.voiceInput.SetValue(strings.TrimSpace(p.TTS.Voice))
}

func (m *personasModel) cancelForm() {
	m.formMode = ""
	m.formStep = personaFormName
	m.editID = ""
	m.nameInput.Blur()
	m.nameInput.SetValue("")
	m.promptTA.Blur()
	m.promptTA.SetValue("")
	m.prefillStack(nil)
	m.brainModelInput.Blur()
	m.voiceInput.Blur()
	m.stackRow = stackRowBrainProvider
}

func (m personasModel) focusNameStep() (personasModel, tea.Cmd) {
	m.formStep = personaFormName
	m.promptTA.Blur()
	m.nameInput.Focus()
	return m, textinput.Blink
}

func (m personasModel) focusPromptStep() (personasModel, tea.Cmd) {
	if strings.TrimSpace(m.nameInput.Value()) == "" {
		m.message = "Enter a display name first"
		return m, nil
	}
	m.formStep = personaFormPrompt
	m.nameInput.Blur()
	// Ensure a usable draft: empty prompt body gets the default identity.
	if strings.TrimSpace(m.promptTA.Value()) == "" {
		if text := m.resolveDefaultPrompt(); text != "" {
			m.promptTA.SetValue(text)
		}
	}
	m.promptTA.Focus()
	m.message = "Edit the system prompt · ctrl+j / alt+s / f2 to save (ctrl+s if your terminal allows)"
	return m, textarea.Blink
}

func (m *personasModel) resolveDefaultPrompt() string {
	if m.defaultPrompt == nil {
		return ""
	}
	text, err := m.defaultPrompt()
	if err != nil {
		return ""
	}
	return text
}

func (m *personasModel) beginCreate() tea.Cmd {
	m.formMode = "create"
	m.formStep = personaFormName
	m.editID = ""
	m.message = "Name the agent, then edit the system prompt · save with ctrl+j / alt+s / f2"
	m.resizeForm()
	m.nameInput.SetValue("")
	m.nameInput.Focus()
	m.promptTA.SetValue(m.resolveDefaultPrompt())
	m.promptTA.Blur()
	m.prefillStack(nil) // "(default)" = clone current globals at create time
	return textinput.Blink
}

func (m *personasModel) beginEdit() tea.Cmd {
	if m.loadErr != "" || m.cursor < 0 || m.cursor >= len(m.items) {
		return nil
	}
	p := m.items[m.cursor]
	if p == nil {
		return nil
	}
	m.formMode = "edit"
	m.formStep = personaFormName
	m.editID = p.ID
	m.message = "Edit name + system prompt (used by the brain as the real persona system prompt)"
	m.resizeForm()
	m.nameInput.SetValue(p.DisplayName)
	m.nameInput.Focus()
	promptName := p.Prompts.Persona
	if promptName == "" {
		promptName = p.ID
	}
	text := ""
	if m.loadPrompt != nil {
		if got, err := m.loadPrompt(promptName); err == nil {
			text = got
		}
	}
	if text == "" {
		text = m.resolveDefaultPrompt()
	}
	m.promptTA.SetValue(text)
	m.promptTA.Blur()
	m.prefillStack(p)
	return textinput.Blink
}

func (m personasModel) submitForm() (personasModel, tea.Cmd) {
	name := strings.TrimSpace(m.nameInput.Value())
	if name == "" {
		m.message = "Enter a display name"
		m.formStep = personaFormName
		m.nameInput.Focus()
		return m, textinput.Blink
	}
	prompt := strings.TrimSpace(m.promptTA.Value())
	if prompt == "" {
		// Name-only create/edit still needs a real identity document for the brain.
		prompt = strings.TrimSpace(m.resolveDefaultPrompt())
	}
	if prompt == "" {
		m.message = "Enter a system prompt (this is the persona identity the brain loads)"
		m.formStep = personaFormPrompt
		m.promptTA.Focus()
		return m, textarea.Blink
	}

	switch m.formMode {
	case "create":
		create := m.createPersona
		if create == nil {
			create = persona.CreateAndUseWithOpts
		}
		p, err := create(m.cfg, persona.CreateOpts{
			DisplayName:  name,
			SystemPrompt: prompt,
			Brain:        m.formBrain(),
			TTS:          m.formTTS(),
		})
		if err != nil {
			m.message = fmt.Sprintf("Failed to create: %v", err)
			return m, nil
		}
		m.cancelForm()
		m.reload()
		for i, item := range m.items {
			if item != nil && item.ID == p.ID {
				m.cursor = i
				break
			}
		}
		m.ensureVisible()
		m.message = fmt.Sprintf("Created %s (%s) · system prompt → prompts/persona/%s.yaml · restart chat to apply", p.DisplayName, p.ID, p.ID)
	case "edit":
		if m.saveName != nil {
			if _, err := m.saveName(m.editID, name); err != nil {
				m.message = fmt.Sprintf("Failed to save name: %v", err)
				return m, nil
			}
		}
		if m.savePrompt != nil {
			if _, err := m.savePrompt(m.editID, prompt); err != nil {
				m.message = fmt.Sprintf("Failed to save prompt: %v", err)
				return m, nil
			}
		}
		if m.saveStack != nil {
			if _, err := m.saveStack(m.editID, m.formBrain(), m.formTTS()); err != nil {
				m.message = fmt.Sprintf("Failed to save model/voice: %v", err)
				return m, nil
			}
		}
		// If this is the active persona, refresh display name / prompt ref on cfg.
		if m.cfg != nil && persona.ActiveID(m.cfg) == m.editID {
			if m.usePersona != nil {
				_ = m.usePersona(m.cfg, m.editID)
			}
		}
		id := m.editID
		m.cancelForm()
		m.reload()
		m.message = fmt.Sprintf("Updated %s · prompts/persona/%s.yaml · start a new chat for the brain to load it", id, id)
	}
	return m, nil
}
