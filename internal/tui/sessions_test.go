package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/session"
)

func TestSessionsCompactsForSmallTerminal(t *testing.T) {
	saved := []session.Session{{
		ID: "session-123", Summary: strings.Repeat("long summary ", 10), UpdatedAt: time.Now(),
		Turns: []brain.Turn{{Role: "user", Content: "hello"}},
	}}
	m := newSessions(saved)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 36, Height: 8})
	view := stripANSI(m.View())
	if got := len(strings.Split(view, "\n")); got > 8 {
		t.Fatalf("compact sessions rendered %d lines in 8-row terminal:\n%s", got, view)
	}
}

func TestSessionsFiltersEmptyConversations(t *testing.T) {
	saved := []session.Session{
		{ID: "empty"},
		{ID: "real", Turns: []brain.Turn{{Role: "user", Content: "hello"}}},
	}
	m := newSessions(saved)
	if len(m.sessions) != 1 || m.sessions[0].ID != "real" {
		t.Fatalf("resumable sessions = %+v", m.sessions)
	}
}
