package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/persona"
)

const (
	personaFormName   = 0
	personaFormPrompt = 1
)

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
		if m.formStep == personaFormName {
			return m.focusPromptStep()
		}
		return m.focusNameStep()
	case key == "shift+tab":
		if m.formStep == personaFormPrompt {
			return m.focusNameStep()
		}
		return m.focusPromptStep()
	case key == "enter":
		if m.formStep == personaFormName {
			// Name done → prompt. Save is intentionally not Enter (multi-line
			// prompt needs Enter for newlines).
			return m.focusPromptStep()
		}
		// Enter inserts newline in the prompt textarea (fall through).
	}

	var cmd tea.Cmd
	if m.formStep == personaFormName {
		m.nameInput, cmd = m.nameInput.Update(msg)
	} else {
		m.promptTA, cmd = m.promptTA.Update(msg)
	}
	return m, cmd
}

func (m *personasModel) cancelForm() {
	m.formMode = ""
	m.formStep = personaFormName
	m.editID = ""
	m.nameInput.Blur()
	m.nameInput.SetValue("")
	m.promptTA.Blur()
	m.promptTA.SetValue("")
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
		p, err := create(m.cfg, persona.CreateOpts{DisplayName: name, SystemPrompt: prompt})
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
