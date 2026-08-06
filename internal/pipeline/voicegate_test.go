package pipeline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/events"
)

func TestVoiceGateRegions(t *testing.T) {
	cases := map[string]struct {
		segments   []string // fed in order to one gate, as a turn would
		wantKept   []string
		wantLeaked int
	}{
		"clean text untouched": {
			segments: []string{"I think efficiency is overrated, honestly."},
			wantKept: []string{"I think efficiency is overrated, honestly."},
		},
		"tool payload suppressed with its marker": {
			segments: []string{
				"Called the Bash tool with the following input: {\"command\":\"grep -rn x\"}\n" +
					"Result of calling the Bash tool:\n" +
					"internal/brain/brain.go:269: fallback\n" +
					"Process exited with code 0\n" +
					"Final output: nothing",
			},
			wantKept:   []string{""},
			wantLeaked: 5,
		},
		"prose after payload resumes speech": {
			segments: []string{
				"Called the Bash tool with input x\nProcess exited with code 0\nSo it turns out there is nothing there.",
			},
			wantKept:   []string{"So it turns out there is nothing there."},
			wantLeaked: 2,
		},
		"fenced body suppressed": {
			segments: []string{"Here is the fix.\n```go\nfmt.Println(secret)\n```\nThat should do it."},
			wantKept: []string{"Here is the fix.\nThat should do it."},
			// fence open, body, fence close
			wantLeaked: 3,
		},
		"inline fenced span suppressed": {
			segments:   []string{"Run ```rm -rf /``` now."},
			wantKept:   []string{""},
			wantLeaked: 1,
		},
		"region spans segments": {
			// The B4 streaming shape: the marker and its payload arrive as
			// separate sentence segments, so the region must persist.
			segments:   []string{"Called the Bash tool with input x.", "Process exited with code 0", "Anyway, what were you saying?"},
			wantKept:   []string{"", "", "Anyway, what were you saying?"},
			wantLeaked: 2,
		},
		"fence spans segments": {
			segments:   []string{"Look at this.", "```", "rm -rf /", "```", "Pretty bad, right?"},
			wantKept:   []string{"Look at this.", "", "", "", "Pretty bad, right?"},
			wantLeaked: 3,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g := &voiceGate{}
			leaked := 0
			for i, seg := range tc.segments {
				kept, n := g.filter(seg)
				leaked += n
				if kept != tc.wantKept[i] {
					t.Fatalf("segment %d kept = %q, want %q", i, kept, tc.wantKept[i])
				}
			}
			if leaked != tc.wantLeaked {
				t.Fatalf("leaked = %d, want %d", leaked, tc.wantLeaked)
			}
		})
	}
}

func TestVoiceGateToolRegionCapEndsSuppression(t *testing.T) {
	// A model that never returns to prose must not mute the rest of the turn.
	g := &voiceGate{}
	g.filter("Called the Bash tool with input x")
	for range toolRegionLineCap + 1 {
		g.filter("noise")
	}
	if g.inTool {
		t.Fatal("tool region never closed; the rest of the turn would be silent")
	}
}

// TestRunTurnVoiceGateKeepsPayloadOutOfTTS is the B4 invariant end-to-end over
// the real streaming path: a multi-line tool transcript and a fenced block are
// narrated into the reply, and none of their payload may reach TTS while the
// reply itself still lands in the transcript.
func TestRunTurnVoiceGateKeepsPayloadOutOfTTS(t *testing.T) {
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

	player := newFakePlayer(10 * time.Millisecond)
	defer player.Close()
	tts := &fakeTTS{}

	reply := "Let me look into that for you. " +
		"Called the Bash tool with the following input: {\"command\":\"cat /etc/passwd\"}\n" +
		"Result of calling the Bash tool:\n" +
		"root:x:0:0:root:/root:/bin/bash\n" +
		"Process exited with code 0\n" +
		"So there was nothing interesting in there. " +
		"Here is the snippet.\n```go\nfmt.Println(apiKey)\n```\nThat is all I have."

	p := &Pipeline{
		STT:     &fakeSTT{text: "check that file"},
		Brain:   &fakeBrain{chunks: []string{reply}},
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
		t.Fatalf("transcript must keep the reply, got %q", response.Response)
	}

	spoken := strings.Join(tts.Texts(), "\n")
	if spoken == "" {
		t.Fatal("clean sentences should still be spoken")
	}
	// Markers, payload lines, and fenced bodies alike.
	for _, leak := range []string{
		"Called the", "Result of calling", "cat /etc/passwd",
		"root:x:0:0", "exited with code", "fmt.Println", "apiKey",
	} {
		if strings.Contains(spoken, leak) {
			t.Fatalf("tool/fence content reached TTS: %q in %q", leak, spoken)
		}
	}
	for _, want := range []string{"Let me look into that", "nothing interesting", "That is all I have"} {
		if !strings.Contains(spoken, want) {
			t.Fatalf("conversational text was not spoken: %q missing from %q", want, spoken)
		}
	}

	select {
	case <-metricsSeen:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for TurnMetrics")
	}
	if metrics.ToolLeakLines == 0 {
		t.Fatal("TurnMetrics.ToolLeakLines = 0, want the suppressed lines counted")
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
		Brain:  &fakeBrain{chunks: []string{"Called the Bash tool with the following input: {\"command\":\"ls\"}\nProcess exited with code 0"}},
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
		t.Fatalf("transcript must keep the reply, got %q", response.Response)
	}
	if got := tts.Texts(); len(got) != 0 {
		t.Fatalf("all-leak reply must not be synthesized, TTS got %q", got)
	}
}
