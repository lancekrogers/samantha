package tui

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/events"
)

// The input label shows the mic glyph only while a voice turn is actively
// listening for the user.
func TestInputGlyphTracksListeningState(t *testing.T) {
	runner := &fakeTurnRunner{}
	m, _ := startedConversation(t, runner, true)

	if !strings.Contains(m.View(), "🎙 Listening") {
		t.Error("listening state must show the mic glyph on the composer")
	}

	m.handleEvent(events.UserInput{Text: "spoken"}) // listening -> responding
	if strings.Contains(m.View(), "🎙 Listening") {
		t.Error("mic glyph must drop once the turn is responding")
	}
}

func TestInputGlyphAbsentInTextOnlyMode(t *testing.T) {
	runner := &fakeTurnRunner{}
	m := sizedConversation(t, 80, 24)
	m.startConversation(conversationDeps{
		runner: runner,
		bus:    events.NewBus(),
		voice:  false,
		ctx:    context.Background(),
	})

	if strings.Contains(m.View(), "🎙") {
		t.Error("text-only mode must not show the mic glyph")
	}
}

func TestTurnMetricsRenderAsDimTrailingLine(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.handleEvent(events.TurnMetrics{
		Outcome:                 "completed",
		ModelCompleteElapsed:    400 * time.Millisecond,
		FirstAudioReadyElapsed:  600 * time.Millisecond,
		PlaybackCompleteElapsed: 2300 * time.Millisecond,
	})

	view := m.View()
	for _, want := range []string{"model 0.4s", "voice 0.6s", "spoke 2.3s"} {
		if !strings.Contains(view, want) {
			t.Errorf("metrics line missing %q", want)
		}
	}
}

// A turn with no measured milestones (e.g. canceled before transcription)
// must not leave an empty trailer in the transcript.
func TestEmptyTurnMetricsRenderNothing(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	before := len(m.transcript)
	m.handleEvent(events.TurnMetrics{Outcome: "interrupted"})
	if len(m.transcript) != before {
		t.Error("empty metrics appended a transcript line")
	}
}
