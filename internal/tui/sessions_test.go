package tui

import (
	"fmt"
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

func TestSessionsVisibleRowsUsesTerminalHeight(t *testing.T) {
	saved := make([]session.Session, 20)
	for i := range saved {
		saved[i] = session.Session{
			ID: fmt.Sprintf("s%d", i), Summary: fmt.Sprintf("chat %d", i),
			Turns: []brain.Turn{{Role: "user", Content: "hi"}}, UpdatedAt: time.Now(),
		}
	}
	m := newSessions(saved)
	// Zero height used to hard-cap at 3 visible rows.
	if got := m.visibleRows(); got < 10 {
		t.Fatalf("visibleRows with unset height = %d, want a full default list", got)
	}
	m, _ = m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if got := m.visibleRows(); got != 18 {
		t.Fatalf("visibleRows at height 24 = %d, want 18", got)
	}
	view := stripANSI(m.View())
	// At least more than the old 3-row cap should appear.
	shown := 0
	for i := 0; i < 20; i++ {
		if strings.Contains(view, fmt.Sprintf("chat %d", i)) {
			shown++
		}
	}
	if shown < 10 {
		t.Fatalf("sessions view only shows %d entries at 24-row terminal:\n%s", shown, view)
	}
}
