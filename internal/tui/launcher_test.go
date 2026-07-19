package tui

import (
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/session"
)

func TestLauncherDisplaysConfiguredBrainModel(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{
			name: "ollama",
			cfg:  &config.Config{BrainProvider: "ollama", OllamaModel: "llama3.2", TTSVoice: "af_heart"},
			want: "model llama3.2",
		},
		{
			name: "grok",
			cfg:  &config.Config{BrainProvider: "grok", GrokModel: "grok-build", TTSVoice: "af_heart"},
			want: "model grok-build",
		},
		{
			name: "default",
			cfg:  &config.Config{BrainProvider: "claude", TTSVoice: "af_heart"},
			want: "model default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newLauncher(tt.cfg, nil)
			m.width, m.height = 80, 24
			view := strings.ToLower(stripANSI(m.View()))
			if !strings.Contains(view, tt.want) {
				t.Fatalf("launcher view missing %q:\n%s", tt.want, view)
			}
			if !strings.Contains(view, "samantha") {
				t.Fatalf("launcher missing brand:\n%s", view)
			}
		})
	}
}

func TestLauncherDefaultsToContinueWhenSessionExists(t *testing.T) {
	saved := []session.Session{{ID: "session-123", Summary: "Fix the TUI", UpdatedAt: time.Now()}}
	m := newLauncher(&config.Config{}, nil, saved)
	if len(m.items) == 0 || m.items[0].action != actionContinue {
		t.Fatal("most recent session is not the default launcher action")
	}
	msg := m.items[0]
	if msg.sessionID != "session-123" || !strings.Contains(msg.label, "Fix the TUI") {
		t.Fatalf("continue item = %+v", msg)
	}
}

func TestLauncherOffersTailscaleAndOpensItsScreen(t *testing.T) {
	m := newLauncher(&config.Config{}, nil)
	for i, item := range m.items {
		if item.action != actionTailscale {
			continue
		}
		if !strings.Contains(item.label, "Remote") || !strings.Contains(item.label, "Tailscale") {
			t.Fatalf("Tailscale launcher label = %q", item.label)
		}
		m.cursor = i
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			t.Fatal("Tailscale launcher action returned no command")
		}
		msg, ok := cmd().(switchScreenMsg)
		if !ok || screen(msg) != screenTailscale {
			t.Fatalf("Tailscale launcher message = %#v", msg)
		}
		return
	}
	t.Fatal("launcher has no Tailscale action")
}

func TestLauncherOffersMeeting(t *testing.T) {
	m := newLauncher(&config.Config{}, nil)
	for i, item := range m.items {
		if item.action != actionMeeting {
			continue
		}
		if item.label != "Meeting" {
			t.Fatalf("meeting label = %q", item.label)
		}
		m.cursor = i
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			t.Fatal("meeting action returned no command")
		}
		msg, ok := cmd().(switchScreenMsg)
		if !ok || screen(msg) != screenMeetingSetup {
			t.Fatalf("meeting message = %#v", msg)
		}
		return
	}
	t.Fatal("launcher has no Meeting action")
}

func TestLauncherBannerSurfacesMeetingCloseError(t *testing.T) {
	m := newLauncher(&config.Config{}, nil)
	m.width, m.height = 80, 24
	m = m.withBanner("close meeting log: disk full", true)
	view := stripANSI(m.View())
	if !strings.Contains(view, "close meeting log: disk full") {
		t.Fatalf("banner missing from launcher:\n%s", view)
	}
}

func TestLauncherCompactsForSmallTerminal(t *testing.T) {
	saved := []session.Session{{
		ID: "session-123", Summary: strings.Repeat("long summary ", 10), UpdatedAt: time.Now(),
	}}
	m := newLauncher(&config.Config{}, nil, saved)
	m, _ = m.Update(tea.WindowSizeMsg{Width: 36, Height: 8})
	view := stripANSI(m.View())
	if got := len(strings.Split(view, "\n")); got > 8 {
		t.Fatalf("compact launcher rendered %d lines in 8-row terminal:\n%s", got, view)
	}
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}
