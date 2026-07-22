package tui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	ansi "github.com/charmbracelet/x/ansi"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/persona"
)

const personasCreateLabel = "+ Create new persona…"

// personasModel is the main-menu screen for listing, switching, and creating
// voice agent personas.
type personasModel struct {
	cfg    *config.Config
	width  int
	height int
	cursor int
	offset int

	items   []*persona.Profile
	loadErr string
	message string

	creating bool
	create   textinput.Model

	listPersonas  func() ([]*persona.Profile, error)
	usePersona    func(*config.Config, string) error
	createPersona func(*config.Config, string) (*persona.Profile, error)
}

func newPersonas(cfg *config.Config) personasModel {
	m := personasModel{
		cfg:           cfg,
		listPersonas:  persona.List,
		usePersona:    persona.Use,
		createPersona: persona.CreateAndUse,
		create:        newPersonaCreateInput(),
	}
	m.reload()
	return m
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
		m.create.Width = max(m.width-16, 20)
		m.ensureVisible()

	case tea.KeyMsg:
		if m.creating {
			return m.updateCreate(msg)
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
		case "esc", "q":
			return m, func() tea.Msg { return switchScreenMsg(screenLauncher) }
		}
		m.ensureVisible()
	}
	return m, nil
}

func (m personasModel) updateCreate(msg tea.KeyMsg) (personasModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.creating = false
		m.create.Blur()
		m.create.SetValue("")
		m.message = "Create cancelled"
		return m, nil
	case "enter":
		name := strings.TrimSpace(m.create.Value())
		if name == "" {
			m.message = "Enter a display name for the new persona"
			return m, nil
		}
		create := m.createPersona
		if create == nil {
			create = persona.CreateAndUse
		}
		p, err := create(m.cfg, name)
		if err != nil {
			m.message = fmt.Sprintf("Failed to create persona: %v", err)
			return m, nil
		}
		m.creating = false
		m.create.Blur()
		m.create.SetValue("")
		m.reload()
		for i, item := range m.items {
			if item != nil && item.ID == p.ID {
				m.cursor = i
				break
			}
		}
		m.message = fmt.Sprintf("Created and activated %s (%s)", p.DisplayName, p.ID)
		return m, nil
	}
	var cmd tea.Cmd
	m.create, cmd = m.create.Update(msg)
	return m, cmd
}

func (m *personasModel) selectCurrent() tea.Cmd {
	if m.loadErr != "" {
		return nil
	}
	if len(m.items) == 0 || m.cursor == len(m.items) {
		m.creating = true
		m.message = ""
		m.create.SetValue("")
		m.create.Width = max(m.width-16, 20)
		m.create.Focus()
		return nil
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
	m.message = fmt.Sprintf("Active persona: %s", name)
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
	b.WriteString(dimStyle.Render(ansi.Truncate("  Switch or create voice agents", width, "…")))
	b.WriteString("\n")
	if m.height == 0 || m.height >= 10 {
		b.WriteString(dimStyle.Render(strings.Repeat("─", max(width, 1))))
		b.WriteString("\n")
	}

	listRows := m.visibleRows()
	if m.creating {
		for _, line := range m.createLines(listRows) {
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
	help := "  ↑/↓ navigate • enter switch/create • esc back"
	if m.creating {
		help = "  type a display name • enter create & activate • esc cancel"
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

func (m personasModel) createLines(listRows int) []string {
	slug := persona.Slugify(m.create.Value())
	if slug == "" {
		slug = "persona"
	}
	lines := []string{
		"  Create a new voice agent",
		"",
		m.create.View(),
		dimStyle.Render(fmt.Sprintf("  id will be: %s", slug)),
		"",
		dimStyle.Render("  Clones current TTS provider/voice; edit Settings → Voice after create."),
	}
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
