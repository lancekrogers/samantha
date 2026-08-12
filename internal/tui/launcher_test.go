package tui

import (
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/persona"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/session"
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

func TestLauncherDisplaysConfiguredTTSProviderAndModel(t *testing.T) {
	m := newLauncher(&config.Config{
		BrainProvider: "claude",
		TTSProvider:   "qwen3-tts",
		QwenTTSModel:  "/opt/qwen/models/1.7b",
		TTSVoice:      "af_heart",
	}, nil)
	m.width, m.height = 100, 24
	view := strings.ToLower(stripANSI(m.View()))
	if !strings.Contains(view, "tts qwen3-tts") || !strings.Contains(view, "1.7b") {
		t.Fatalf("launcher missing TTS provider/model badge:\n%s", view)
	}
	if !strings.Contains(view, "voice model-native") || strings.Contains(view, "voice af_heart") {
		t.Fatalf("launcher should identify Qwen's model-native voice:\n%s", view)
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

func TestLauncherOffersRemoteAndOpensItsScreen(t *testing.T) {
	m := newLauncher(&config.Config{}, nil)
	for i, item := range m.items {
		if item.action != actionRemote {
			continue
		}
		if !strings.Contains(strings.ToLower(item.label), "device") {
			t.Fatalf("remote launcher label = %q", item.label)
		}
		m.cursor = i
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			t.Fatal("remote launcher action returned no command")
		}
		msg, ok := cmd().(switchScreenMsg)
		if !ok || screen(msg) != screenRemote {
			t.Fatalf("remote launcher message = %#v", msg)
		}
		return
	}
	t.Fatal("launcher has no Remote action")
}

func TestLauncherOffersLibrary(t *testing.T) {
	m := newLauncher(&config.Config{}, nil)
	for i, item := range m.items {
		if item.action != actionLibrary {
			continue
		}
		if item.label != "Library" {
			t.Fatalf("library label = %q", item.label)
		}
		m.cursor = i
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd == nil {
			t.Fatal("library action returned no command")
		}
		msg, ok := cmd().(switchScreenMsg)
		if !ok || screen(msg) != screenLibrary {
			t.Fatalf("library launcher message = %#v", msg)
		}
		return
	}
	t.Fatal("launcher has no Library action")
}

func TestLauncherMeetingSplitsStartAndSettings(t *testing.T) {
	// Design WI-c8884d §2.6: Meeting is a submenu — Start meeting keeps the
	// happy path, Meeting settings opens Settings on the Meeting section
	// without starting a recording.
	m := newLauncher(&config.Config{}, nil)
	for i, item := range m.items {
		if item.action != actionMeeting {
			continue
		}
		if item.label != "Meeting" {
			t.Fatalf("meeting label = %q", item.label)
		}
		m.cursor = i
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if len(m.submenu) != 2 {
			t.Fatalf("meeting submenu = %d items, want start + settings", len(m.submenu))
		}

		// Row 0: Start meeting → meeting setup (unchanged happy path).
		start := m
		start.submenuCursor = 0
		start, cmd := start.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if start.submenu != nil {
			t.Fatal("submenu should close on selection")
		}
		msg, ok := cmd().(switchScreenMsg)
		if !ok || screen(msg) != screenMeetingSetup {
			t.Fatalf("start meeting message = %#v", msg)
		}

		// Row 1: Meeting settings → Settings landed on the Meeting section.
		m.submenuCursor = 1
		m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		open, ok := cmd().(openSettingsSectionMsg)
		if !ok || open.section != sectionMeeting {
			t.Fatalf("meeting settings message = %#v", cmd())
		}
		return
	}
	t.Fatal("launcher has no Meeting action")
}

func TestLauncherNewConversationOpensPersonaPicker(t *testing.T) {
	// Design WI-c8884d §2.4: New conversation always establishes the session
	// persona — the picker pre-selects the active persona as the explicit
	// default and offers create.
	cfg := &config.Config{ActivePersona: "bob"}
	m := newLauncher(cfg, nil)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return []*persona.Profile{
			{ID: "ada", DisplayName: "Ada", TTS: persona.TTS{Provider: "kokoro", Voice: "af_heart"}},
			{ID: "bob", DisplayName: "Bob"},
		}, nil
	}
	for i, item := range m.items {
		if item.action != actionNew {
			continue
		}
		m.cursor = i
		m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if cmd != nil {
			t.Fatal("new conversation must open the picker, not start immediately")
		}
		if len(m.submenu) != 3 {
			t.Fatalf("picker = %d rows, want 2 personas + create", len(m.submenu))
		}
		if m.submenuCursor != 1 {
			t.Fatalf("picker cursor = %d, want active persona pre-selected", m.submenuCursor)
		}

		// Pick Ada → conversation starts bound to her.
		m.submenuCursor = 0
		m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		start, ok := cmd().(startPipelineMsg)
		if !ok || start.personaID != "ada" {
			t.Fatalf("picker start message = %#v", cmd())
		}

		// Create row routes to the Personas editor.
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // reopen main-menu New (cursor unchanged)
		if m.submenu == nil {
			t.Fatal("picker did not reopen")
		}
		m.submenuCursor = len(m.submenu) - 1
		m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if msg, ok := cmd().(switchScreenMsg); !ok || screen(msg) != screenPersonas {
			t.Fatalf("create row message = %#v", cmd())
		}

		// Esc closes without starting anything.
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		if m.submenu != nil || cmd != nil {
			t.Fatal("esc should close the picker quietly")
		}
		return
	}
	t.Fatal("launcher has no New conversation action")
}

func TestLauncherPickerFallsBackWhenListingFails(t *testing.T) {
	m := newLauncher(&config.Config{}, nil)
	m.listPersonas = func() ([]*persona.Profile, error) {
		return nil, fmt.Errorf("personas dir unreadable")
	}
	for i, item := range m.items {
		if item.action != actionNew {
			continue
		}
		m.cursor = i
		m, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		if m.submenu != nil {
			t.Fatal("failed listing must not open an empty picker")
		}
		if _, ok := cmd().(startPipelineMsg); !ok {
			t.Fatalf("fallback message = %#v, want default start", cmd())
		}
		return
	}
	t.Fatal("launcher has no New conversation action")
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

func TestOpenSettingsSectionLandsOnMeeting(t *testing.T) {
	app := NewApp(&config.Config{})
	model, _ := app.Update(openSettingsSectionMsg{section: sectionMeeting})
	got := model.(App)
	if got.screen != screenSettings {
		t.Fatalf("screen = %d, want settings", got.screen)
	}
	if got.settings.section != sectionMeeting {
		t.Fatalf("settings section = %d, want meeting", got.settings.section)
	}
}
