package tui

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// conversationModel is a placeholder for the voice conversation TUI.
// The actual voice loop runs outside bubbletea after the TUI exits.
type conversationModel struct{}

func (m conversationModel) Update(msg tea.Msg) (conversationModel, tea.Cmd) {
	return m, nil
}

func (m conversationModel) View() string {
	var b strings.Builder
	b.WriteString(titleStyle.Render("  Conversation"))
	b.WriteString("\n\n")
	b.WriteString("  Starting voice pipeline...\n")
	return b.String()
}
