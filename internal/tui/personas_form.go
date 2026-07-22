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
	return ta
}

func (m *personasModel) resizeForm() {
	w := max(m.width-8, 24)
	m.nameInput.Width = max(w-8, 20)
	m.promptTA.SetWidth(w)
	// Prompt area uses most of the body after labels.
	h := max(m.height-12, 4)
	if h > 14 {
		h = 14
	}
	m.promptTA.SetHeight(h)
}

func (m personasModel) updateForm(msg tea.KeyMsg) (personasModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.cancelForm()
		m.message = "Edit cancelled"
		return m, nil
	case "ctrl+s":
		return m.submitForm()
	case "tab":
		if m.formStep == personaFormName {
			return m.focusPromptStep()
		}
		return m.focusNameStep()
	case "shift+tab":
		if m.formStep == personaFormPrompt {
			return m.focusNameStep()
		}
		return m.focusPromptStep()
	case "enter":
		if m.formStep == personaFormName {
			return m.focusPromptStep()
		}
		// Enter inserts newline in the prompt textarea.
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
	m.promptTA.Focus()
	return m, textarea.Blink
}

func (m *personasModel) beginCreate() tea.Cmd {
	m.formMode = "create"
	m.formStep = personaFormName
	m.editID = ""
	m.message = ""
	m.resizeForm()
	m.nameInput.SetValue("")
	m.nameInput.Focus()
	def := ""
	if m.defaultPrompt != nil {
		if text, err := m.defaultPrompt(); err == nil {
			def = text
		}
	}
	m.promptTA.SetValue(def)
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
	m.message = ""
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
	if text == "" && m.defaultPrompt != nil {
		text, _ = m.defaultPrompt()
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
		return m, nil
	}
	prompt := strings.TrimSpace(m.promptTA.Value())
	if prompt == "" {
		m.message = "Enter a system prompt (or paste the default and edit it)"
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
		m.message = fmt.Sprintf("Created and activated %s (%s) with custom system prompt", p.DisplayName, p.ID)
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
		m.message = fmt.Sprintf("Updated %s (system prompt saved)", id)
	}
	return m, nil
}
