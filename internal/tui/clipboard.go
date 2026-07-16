package tui

import (
	"github.com/atotto/clipboard"
	tea "github.com/charmbracelet/bubbletea"
)

// clipboardBackend keeps operating-system integration outside the editor
// model. Tests can provide an in-memory implementation through deps.
type clipboardBackend interface {
	ReadAll() (string, error)
	WriteAll(string) error
}

type systemClipboard struct{}

func (systemClipboard) ReadAll() (string, error) {
	return clipboard.ReadAll()
}

func (systemClipboard) WriteAll(text string) error {
	return clipboard.WriteAll(text)
}

type clipboardPasteMsg struct {
	text string
	err  error
}

func readClipboard(backend clipboardBackend) tea.Cmd {
	return func() tea.Msg {
		text, err := backend.ReadAll()
		return clipboardPasteMsg{text: text, err: err}
	}
}

func (m conversationModel) clipboard() clipboardBackend {
	if m.deps.clipboard != nil {
		return m.deps.clipboard
	}
	return systemClipboard{}
}
