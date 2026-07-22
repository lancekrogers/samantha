package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/persona"
)

const personasCreateLabel = "+ Create new persona…"

// personasModel is the main-menu screen for listing, switching, creating, and
// editing voice agent personas (including system prompts).
type personasModel struct {
	cfg    *config.Config
	width  int
	height int
	cursor int
	offset int

	items   []*persona.Profile
	loadErr string
	message string

	// formMode: "" | "create" | "edit"
	formMode  string
	formStep  int // name | prompt
	editID    string
	nameInput textinput.Model
	promptTA  textarea.Model

	listPersonas  func() ([]*persona.Profile, error)
	usePersona    func(*config.Config, string) error
	createPersona func(*config.Config, persona.CreateOpts) (*persona.Profile, error)
	savePrompt    func(id, systemPrompt string) (*persona.Profile, error)
	saveName      func(id, displayName string) (*persona.Profile, error)
	loadPrompt    func(name string) (string, error)
	defaultPrompt func() (string, error)
}

func newPersonas(cfg *config.Config) personasModel {
	m := personasModel{
		cfg:           cfg,
		listPersonas:  persona.List,
		usePersona:    persona.Use,
		createPersona: persona.CreateAndUseWithOpts,
		savePrompt:    persona.UpdateSystemPrompt,
		saveName:      persona.UpdateDisplayName,
		loadPrompt:    persona.LoadSystemPrompt,
		defaultPrompt: persona.DefaultSystemPrompt,
		nameInput:     newPersonaCreateInput(),
		promptTA:      newPersonaPromptArea(),
	}
	m.reload()
	return m
}

// newPersonaCreateInput builds the name field for the create/edit form.
func newPersonaCreateInput() textinput.Model {
	ti := textinput.New()
	ti.Placeholder = "Research buddy"
	ti.CharLimit = 64
	ti.Width = 40
	ti.Prompt = "  Name: "
	return ti
}

// personaListLabel formats one row for the Personas list.
func personaListLabel(p *persona.Profile) string {
	if p == nil {
		return ""
	}
	label := p.DisplayName
	if label == "" {
		label = p.ID
	}
	if p.ID != "" && p.DisplayName != "" && !strings.EqualFold(p.DisplayName, p.ID) {
		label = fmt.Sprintf("%s (%s)", p.DisplayName, p.ID)
	}
	detail := strings.TrimSpace(p.TTS.Provider)
	if v := strings.TrimSpace(p.TTS.Voice); v != "" {
		if detail != "" {
			detail += " · " + v
		} else {
			detail = v
		}
	}
	if detail != "" {
		label += " — " + detail
	}
	return label
}

func (m *personasModel) reload() {
	m.loadErr = ""
	list := m.listPersonas
	if list == nil {
		list = persona.List
	}
	items, err := list()
	if err != nil {
		m.loadErr = err.Error()
		m.items = nil
		return
	}
	m.items = items
}

func (m personasModel) listLen() int {
	if m.loadErr != "" {
		return 1
	}
	return len(m.items) + 1 // trailing create row
}

func (m *personasModel) ensureVisible() {
	visible := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
	maxOffset := max(m.listLen()-visible, 0)
	m.offset = min(max(m.offset, 0), maxOffset)
}

func (m personasModel) visibleRows() int {
	h := m.height
	if h <= 0 {
		h = 24
	}
	if h < 10 {
		return max(h-2, 1)
	}
	return max(h-6, 1)
}

func (m personasModel) Update(msg tea.Msg) (personasModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resizeForm()
		m.ensureVisible()

	case tea.KeyMsg:
		if m.formMode != "" {
			return m.updateForm(msg)
		}
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < m.listLen()-1 {
				m.cursor++
			}
		case "enter":
			return m, m.selectCurrent()
		case "e":
			return m, m.beginEdit()
		case "esc", "q":
			return m, func() tea.Msg { return switchScreenMsg(screenLauncher) }
		}
		m.ensureVisible()

	default:
		// Keep textarea blink alive while editing the prompt.
		if m.formMode != "" && m.formStep == personaFormPrompt {
			var cmd tea.Cmd
			m.promptTA, cmd = m.promptTA.Update(msg)
			return m, cmd
		}
	}
	return m, nil
}

func (m *personasModel) selectCurrent() tea.Cmd {
	if m.loadErr != "" {
		return nil
	}
	if len(m.items) == 0 || m.cursor == len(m.items) {
		return m.beginCreate()
	}
	if m.cursor < 0 || m.cursor >= len(m.items) {
		return nil
	}
	p := m.items[m.cursor]
	if p == nil {
		return nil
	}
	use := m.usePersona
	if use == nil {
		use = persona.Use
	}
	if err := use(m.cfg, p.ID); err != nil {
		m.message = fmt.Sprintf("Failed to switch persona: %v", err)
		return nil
	}
	m.reload()
	name := p.DisplayName
	if name == "" {
		name = p.ID
	}
	m.message = fmt.Sprintf("Active persona: %s · press e to edit name/prompt", name)
	return nil
}
