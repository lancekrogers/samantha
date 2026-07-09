package tui

import (
	"fmt"
	"strconv"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/config"
)

// audiobook field indices.
const (
	abFieldInput = iota
	abFieldOutDir
	abFieldVoice
	abFieldSpeed
	abFieldResume
	abFieldAudioFormat
	abFieldGenerate
	abFieldBack
	abFieldCount
)

type audiobookModel struct {
	cfg      *config.Config
	cursor   int
	editing  bool
	editBuf  string
	input    string
	outDir   string
	voice    string
	speed    string
	resume   bool
	audioFmt string
	command  string
	message  string
	errText  string
}

func newAudiobook(cfg *config.Config) audiobookModel {
	voice := ""
	if cfg != nil {
		voice = cfg.TTSVoice
	}
	return audiobookModel{
		cfg:    cfg,
		voice:  voice,
		speed:  "1",
		resume: true,
	}
}

func (m audiobookModel) Update(msg tea.Msg) (audiobookModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		key := msg.String()
		if m.editing {
			return m.handleEdit(key)
		}
		switch key {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			if m.cursor < abFieldCount-1 {
				m.cursor++
			}
		case "enter", " ":
			return m.activate()
		case "esc", "b":
			return m, func() tea.Msg { return switchScreenMsg(screenLauncher) }
		case "q":
			return m, func() tea.Msg { return quitMsg{} }
		}
	}
	return m, nil
}

func (m audiobookModel) handleEdit(key string) (audiobookModel, tea.Cmd) {
	switch key {
	case "enter":
		m.applyEdit()
		m.editing = false
		m.editBuf = ""
	case "esc":
		m.editing = false
		m.editBuf = ""
	case "backspace":
		if len(m.editBuf) > 0 {
			m.editBuf = m.editBuf[:len(m.editBuf)-1]
		}
	default:
		if len(key) == 1 {
			m.editBuf += key
		}
	}
	return m, nil
}

func (m *audiobookModel) applyEdit() {
	switch m.cursor {
	case abFieldInput:
		m.input = strings.TrimSpace(m.editBuf)
	case abFieldOutDir:
		m.outDir = strings.TrimSpace(m.editBuf)
	case abFieldVoice:
		m.voice = strings.TrimSpace(m.editBuf)
	case abFieldSpeed:
		m.speed = strings.TrimSpace(m.editBuf)
	case abFieldAudioFormat:
		m.audioFmt = strings.TrimSpace(m.editBuf)
	}
	m.command = ""
	m.errText = ""
	m.message = ""
}

func (m audiobookModel) activate() (audiobookModel, tea.Cmd) {
	switch m.cursor {
	case abFieldInput, abFieldOutDir, abFieldVoice, abFieldSpeed, abFieldAudioFormat:
		m.editing = true
		m.editBuf = m.fieldValue(m.cursor)
	case abFieldResume:
		m.resume = !m.resume
		m.command = ""
		m.message = ""
	case abFieldGenerate:
		cmd, err := m.generateCommand()
		if err != nil {
			m.errText = err.Error()
			m.command = ""
			m.message = ""
		} else {
			m.errText = ""
			m.command = cmd
			m.message = "Command generated (not executed). Copy and run in a shell."
		}
	case abFieldBack:
		return m, func() tea.Msg { return switchScreenMsg(screenLauncher) }
	}
	return m, nil
}

func (m audiobookModel) fieldValue(i int) string {
	switch i {
	case abFieldInput:
		return m.input
	case abFieldOutDir:
		return m.outDir
	case abFieldVoice:
		return m.voice
	case abFieldSpeed:
		return m.speed
	case abFieldAudioFormat:
		return m.audioFmt
	default:
		return ""
	}
}

// GenerateAudiobookCommand builds a shell-quoted samantha audiobook create line.
// It never executes the command.
func GenerateAudiobookCommand(input, outDir, voice, speed string, resume bool, audioFormat string) (string, error) {
	if strings.TrimSpace(input) == "" {
		return "", fmt.Errorf("input path is required")
	}
	if strings.TrimSpace(outDir) == "" {
		return "", fmt.Errorf("output directory is required")
	}
	if speed != "" {
		if _, err := strconv.ParseFloat(speed, 64); err != nil {
			return "", fmt.Errorf("speed must be a number")
		}
	}
	parts := []string{"samantha", "audiobook", "create", shellQuote(input), "--out-dir", shellQuote(outDir)}
	if resume {
		parts = append(parts, "--resume")
	}
	if v := strings.TrimSpace(voice); v != "" {
		parts = append(parts, "--voice", shellQuote(v))
	}
	if s := strings.TrimSpace(speed); s != "" {
		if f, err := strconv.ParseFloat(s, 64); err == nil && f > 0 {
			parts = append(parts, "--speed", shellQuote(s))
		}
	}
	if af := strings.TrimSpace(audioFormat); af != "" {
		parts = append(parts, "--audio-format", shellQuote(af))
	}
	return strings.Join(parts, " "), nil
}

func (m audiobookModel) generateCommand() (string, error) {
	return GenerateAudiobookCommand(m.input, m.outDir, m.voice, m.speed, m.resume, m.audioFmt)
}

// shellQuote quotes s for POSIX shells when needed.
func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if !strings.ContainsAny(s, " \t\n'\"\\$`!*?[]{}();<>|&") {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func (m audiobookModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("  Create audiobook"))
	b.WriteString("\n")
	b.WriteString(subtitleStyle.Render("  Generate a shell command (does not run it)"))
	b.WriteString("\n\n")

	rows := []struct {
		label string
		value string
	}{
		{"Input path", displayOr(m.input, "(required)")},
		{"Output dir", displayOr(m.outDir, "(required)")},
		{"Voice", displayOr(m.voice, "(config default)")},
		{"Speed", displayOr(m.speed, "1")},
		{"Resume", map[bool]string{true: "on", false: "off"}[m.resume]},
		{"Audio format", displayOr(m.audioFmt, "(none)")},
		{"Generate command", ""},
		{"Back to launcher", ""},
	}
	for i, row := range rows {
		cursor := "  "
		style := normalStyle
		if i == m.cursor {
			cursor = "▸ "
			style = selectedStyle
		}
		line := row.label
		if row.value != "" {
			val := row.value
			if m.editing && i == m.cursor {
				val = m.editBuf + "█"
			}
			line = fmt.Sprintf("%-14s %s", row.label, val)
		}
		b.WriteString("  " + cursor + style.Render(line) + "\n")
	}

	if m.errText != "" {
		b.WriteString("\n")
		b.WriteString(errorStyle.Render("  " + m.errText))
		b.WriteString("\n")
	}
	if m.message != "" {
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("  " + m.message))
		b.WriteString("\n")
	}
	if m.command != "" {
		b.WriteString("\n")
		b.WriteString(selectedStyle.Render("  " + m.command))
		b.WriteString("\n")
	}

	b.WriteString("\n")
	if m.editing {
		b.WriteString(dimStyle.Render("  type to edit • enter save • esc cancel"))
	} else {
		b.WriteString(dimStyle.Render("  ↑/↓ navigate • enter edit/toggle/generate • b back • q quit"))
	}
	b.WriteString("\n")
	return b.String()
}

func displayOr(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}
