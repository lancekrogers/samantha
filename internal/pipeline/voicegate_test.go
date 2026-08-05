package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/events"
)

func TestSanitizeForSpeech(t *testing.T) {
	cases := map[string]struct {
		in         string
		want       string
		wantLeaked int
	}{
		"clean text untouched": {
			in:   "I think efficiency is overrated, honestly.",
			want: "I think efficiency is overrated, honestly.",
		},
		"tool narration dropped": {
			in:         "Called the Bash tool with the following input: {\"command\":\"grep -rn x\"}\nResult of calling the Bash tool:\nok, moving on.",
			want:       "ok, moving on.",
			wantLeaked: 2,
		},
		"bare tool json dropped": {
			in:         "Sure thing.\n{\"tool\": \"read_file\", \"input\": \"a.go\"}\nDone reading.",
			want:       "Sure thing.\nDone reading.",
			wantLeaked: 1,
		},
		"fence marker dropped": {
			in:         "Here you go.\n```",
			want:       "Here you go.",
			wantLeaked: 1,
		},
		"all leak yields empty": {
			in:         "Called the write_file tool\nResult of calling the write_file tool:",
			want:       "",
			wantLeaked: 2,
		},
		"empty in empty out": {
			in: "",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, leaked := sanitizeForSpeech(tc.in)
			if got != tc.want {
				t.Fatalf("sanitizeForSpeech text = %q, want %q", got, tc.want)
			}
			if leaked != tc.wantLeaked {
				t.Fatalf("sanitizeForSpeech leaked = %d, want %d", leaked, tc.wantLeaked)
			}
		})
	}
}

// TestRunTurnVoiceGateFiltersSpeech is the B4 invariant end-to-end: tool-shaped
// lines the model narrates into its reply stay visible in the chat transcript
// but never reach TTS, and the strip count lands in the turn metrics.
func TestRunTurnVoiceGateFiltersSpeech(t *testing.T) {
	bus := events.NewBus()
	var response events.ResponseReady
	responseSeen := make(chan struct{}, 1)
	events.Subscribe(bus, func(e events.ResponseReady) {
		response = e
		select {
		case responseSeen <- struct{}{}:
		default:
		}
	})
	var metrics events.TurnMetrics
	metricsSeen := make(chan struct{}, 1)
	events.Subscribe(bus, func(e events.TurnMetrics) {
		metrics = e
		select {
		case metricsSeen <- struct{}{}:
		default:
		}
	})
	var stripped int
	events.Subscribe(bus, func(e events.VoiceGateStripped) {
		stripped += e.Lines
	})

	player := newFakePlayer(10 * time.Millisecond)
	defer player.Close()
	tts := &fakeTTS{}

	p := &Pipeline{
		STT:     &fakeSTT{text: "check that file"},
		Brain:   &fakeBrain{chunks: []string{"I'll check that for you. Called the Bash tool with the following input: {\"command\":\"grep -rn x\"}\nResult of calling the Bash tool:\nok all done here. Anything else?"}},
		TTS:     tts,
		Player:  player,
		Capture: newFakeCapture(),
		VAD:     &fakeVAD{},
		Events:  bus,
	}

	if _, err := p.RunTurn(context.Background()); err != nil {
		t.Fatalf("RunTurn() error = %v", err)
	}

	select {
	case <-responseSeen:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ResponseReady")
	}
	if !strings.Contains(response.Response, "Called the Bash tool") {
		t.Fatalf("chat transcript must keep the raw text, got %q", response.Response)
	}

	spoken := strings.Join(tts.Texts(), "\n")
	if spoken == "" {
		t.Fatal("clean sentences should still be spoken")
	}
	for _, leak := range []string{"Called the", "Result of calling", "{\"command\""} {
		if strings.Contains(spoken, leak) {
			t.Fatalf("tool syntax reached TTS: %q in %q", leak, spoken)
		}
	}

	select {
	case <-metricsSeen:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for TurnMetrics")
	}
	if metrics.ToolLeakLines != 2 {
		t.Fatalf("TurnMetrics.ToolLeakLines = %d, want 2", metrics.ToolLeakLines)
	}
	if stripped != 2 {
		t.Fatalf("VoiceGateStripped lines = %d, want 2", stripped)
	}
}

// TestRunTurnTextModeVoiceGateSkipsAllLeakReply: a reply that is entirely tool
// syntax is not spoken at all, but the turn still completes with the raw text.
func TestRunTurnTextModeVoiceGateSkipsAllLeakReply(t *testing.T) {
	bus := events.NewBus()
	var response events.ResponseReady
	responseSeen := make(chan struct{}, 1)
	events.Subscribe(bus, func(e events.ResponseReady) {
		response = e
		select {
		case responseSeen <- struct{}{}:
		default:
		}
	})

	player := newFakePlayer(10 * time.Millisecond)
	defer player.Close()
	tts := &fakeTTS{}

	p := &Pipeline{
		Brain:  &fakeBrain{chunks: []string{"Called the Bash tool with the following input: {\"command\":\"ls\"}"}},
		TTS:    tts,
		Player: player,
		Events: bus,
	}

	if err := p.RunTurnTextMode(context.Background(), "list files"); err != nil {
		t.Fatalf("RunTurnTextMode() error = %v", err)
	}

	select {
	case <-responseSeen:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ResponseReady")
	}
	if !strings.Contains(response.Response, "Called the Bash tool") {
		t.Fatalf("chat transcript must keep the raw text, got %q", response.Response)
	}
	if got := tts.Texts(); len(got) != 0 {
		t.Fatalf("all-leak reply must not be synthesized, TTS got %q", got)
	}
}
