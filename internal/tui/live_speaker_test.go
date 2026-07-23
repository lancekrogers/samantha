package tui

import (
	"testing"

	"github.com/lancekrogers/samantha/internal/events"
	"github.com/lancekrogers/samantha/internal/speaker"
)

type tuiLiveSpeakerFake struct {
	stats   speaker.LiveStats
	enabled []bool
}

func TestConversationVoiceTurnUsesCurrentSpeakerColor(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	fake := &tuiLiveSpeakerFake{stats: speaker.LiveStats{
		Status: speaker.LiveHealthy, LastLabel: "speaker-2",
	}}
	m.liveSpeaker = fake
	m.handleEvent(events.UserInput{Text: "the launch is ready"})

	view := stripANSI(m.View())
	if !contains(view, "speaker-2") || !contains(view, "the launch is ready") {
		t.Fatalf("conversation did not colorize the live speaker turn:\n%s", view)
	}
	if contains(view, "› You") {
		t.Fatalf("voice turn used generic user label:\n%s", view)
	}
}

func (f *tuiLiveSpeakerFake) Stats() speaker.LiveStats { return f.stats }

func (f *tuiLiveSpeakerFake) SetEnabled(enabled bool) {
	f.enabled = append(f.enabled, enabled)
	if enabled {
		f.stats.Status = speaker.LiveRunning
	} else {
		f.stats.Status = speaker.LiveDisabled
	}
}

func TestLiveSpeakerStatusLabelsCoverSafeStates(t *testing.T) {
	for _, tc := range []struct {
		status speaker.LiveStatus
		want   string
	}{
		{speaker.LiveDisabled, "speakers off"},
		{speaker.LiveUnavailable, "speakers unavailable"},
		{speaker.LiveRunning, "speakers starting"},
		{speaker.LiveDegraded, "speakers degraded"},
		{speaker.LiveHealthy, "speakers healthy"},
		{speaker.LiveClosed, "speakers closed"},
	} {
		if got := liveSpeakerStatusLabel(tc.status); got != tc.want {
			t.Errorf("status %q = %q, want %q", tc.status, got, tc.want)
		}
	}
}

func TestConversationLiveSpeakerControlsDoNotExposeIdentity(t *testing.T) {
	m := sizedConversation(t, 100, 24)
	fake := &tuiLiveSpeakerFake{stats: speaker.LiveStats{Status: speaker.LiveHealthy, QueueDepth: 1, Capacity: 4, Dropped: 2}}
	m.liveSpeaker = fake
	m.liveSpeakerStats = fake.stats
	m.liveSpeakerStatsKnown = true

	if got := liveSpeakerStatusDetail(fake.stats); got != "speakers healthy · queue 1/4 · dropped 2" {
		t.Fatalf("status detail = %q", got)
	}
	if view := stripANSI(m.View()); !contains(view, "speakers healthy") {
		t.Fatalf("conversation view missing live speaker status:\n%s", view)
	}

	m.configureLiveSpeakers([]string{"off"})
	if len(fake.enabled) != 1 || fake.enabled[0] {
		t.Fatalf("enabled calls = %v, want one disable", fake.enabled)
	}
	m.configureLiveSpeakers([]string{"on"})
	if len(fake.enabled) != 2 || !fake.enabled[1] {
		t.Fatalf("enabled calls = %v, want disable then enable", fake.enabled)
	}
}

func TestSpeakersSlashCommandParsesOptionalControl(t *testing.T) {
	command, args, found, slash := parseSlashCommand("/speakers off")
	if !found || !slash || command.id != commandSpeakers || len(args) != 1 || args[0] != "off" {
		t.Fatalf("parsed command = %+v args=%v found=%v slash=%v", command, args, found, slash)
	}
}

func contains(value, want string) bool {
	for i := 0; i+len(want) <= len(value); i++ {
		if value[i:i+len(want)] == want {
			return true
		}
	}
	return false
}
