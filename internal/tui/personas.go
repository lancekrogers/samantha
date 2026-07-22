package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/persona"
)

const (
	personasCreateLabel = "+ Create new persona…"
	personaFormName     = 0
	personaFormPrompt   = 1
)

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

func newPersonaPromptArea() textarea.Model {
	ta := textarea.New()
	ta.Placeholder = "System prompt / personality for this voice agent…"
	ta.CharLimit = 8000
	ta.SetWidth(60)
	ta.SetHeight(8)
	ta.ShowLineNumbers = false
	return ta
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

func (m personasModel) View() string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	var b strings.Builder
	b.WriteString(headerStyle.Render("  Personas"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(ansi.Truncate("  Switch, create, or edit voice agents (system prompt included)", width, "…")))
	b.WriteString("\n")
	if m.height == 0 || m.height >= 10 {
		b.WriteString(dimStyle.Render(strings.Repeat("─", max(width, 1))))
		b.WriteString("\n")
	}

	listRows := m.visibleRows()
	if m.formMode != "" {
		for _, line := range m.formLines(listRows) {
			b.WriteString(line)
			b.WriteString("\n")
		}
	} else {
		for _, line := range m.listLines(listRows) {
			b.WriteString(line)
			b.WriteString("\n")
		}
	}

	if m.message != "" {
		b.WriteString(ansi.Truncate("  "+statusStyle.Render(m.message), width, "…"))
		b.WriteString("\n")
	} else {
		b.WriteString("\n")
	}
	help := "  ↑/↓ navigate • enter switch/create • e edit • esc back"
	if m.formMode != "" {
		help = "  tab fields • ctrl+s save & activate • enter next field (name) / newline (prompt) • esc cancel"
	}
	b.WriteString(dimStyle.Render(ansi.Truncate(help, width, "…")))
	return b.String()
}

func (m personasModel) listLines(listRows int) []string {
	if m.loadErr != "" {
		return padLines([]string{"  error loading personas: " + m.loadErr}, listRows)
	}
	active := ""
	if m.cfg != nil {
		active = persona.ActiveID(m.cfg)
	}
	total := m.listLen()
	start := m.offset
	end := min(start+listRows, total)
	lines := make([]string, 0, listRows)
	for i := start; i < end; i++ {
		if i == len(m.items) {
			lines = append(lines, m.row(i, personasCreateLabel))
			continue
		}
		p := m.items[i]
		mark := ""
		if p != nil && p.ID == active {
			mark = " ✓"
		}
		lines = append(lines, m.row(i, personaListLabel(p)+mark))
	}
	return padLines(lines, listRows)
}

func (m personasModel) formLines(listRows int) []string {
	title := "  Create a new voice agent"
	if m.formMode == "edit" {
		title = "  Edit persona " + m.editID
	}
	slug := persona.Slugify(m.nameInput.Value())
	if slug == "" {
		slug = "persona"
	}
	nameMark, promptMark := " ", " "
	if m.formStep == personaFormName {
		nameMark = "▸"
	} else {
		promptMark = "▸"
	}
	lines := []string{
		title,
		"",
		fmt.Sprintf("%s Name", nameMark),
		m.nameInput.View(),
	}
	if m.formMode == "create" {
		lines = append(lines, dimStyle.Render(fmt.Sprintf("  id will be: %s", slug)))
	}
	lines = append(lines,
		"",
		fmt.Sprintf("%s System prompt  (supports {agent_name})", promptMark),
		m.promptTA.View(),
		"",
		dimStyle.Render("  ctrl+s save · tab switch fields · esc cancel"),
	)
	return padLines(lines, listRows)
}

func (m personasModel) row(i int, label string) string {
	width := m.width
	if width <= 0 {
		width = 80
	}
	prefix := "  "
	style := dimStyle
	if i == m.cursor {
		prefix = "▸ "
		style = selectedStyle
	}
	return style.Render(ansi.Truncate(prefix+label, width, "…"))
}

func padLines(lines []string, n int) []string {
	for len(lines) < n {
		lines = append(lines, "")
	}
	if len(lines) > n {
		return lines[:n]
	}
	return lines
}
