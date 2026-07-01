package cmd

import "github.com/charmbracelet/lipgloss"

// CLI output palette, matching internal/ui and internal/tui so command output
// reads consistently with the TUI and the fang-styled help. lipgloss disables
// color automatically when stdout is not a TTY, so piped output stays plain.
var (
	titleStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	sectionStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("39"))
	dimStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	keyStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("252"))
	valueStyle   = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("205"))
	activeStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("40"))
	okStyle      = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("40"))
	failStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("196"))
)
