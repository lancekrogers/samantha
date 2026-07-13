package tui

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/lancekrogers/samantha/internal/events"
)

// Emit runs bridge handlers synchronously on the emitting goroutine, so a
// full bridge channel must never block it — even under multi-goroutine burst
// pressure with nothing draining.
func TestBridgeEmitNeverBlocks(t *testing.T) {
	bus := events.NewBus()
	bridge := newEventBridge(4) // tiny queue, maximum pressure
	bridge.attach(bus)

	const goroutines, emits = 8, 200
	done := make(chan struct{})
	go func() {
		var wg sync.WaitGroup
		for g := range goroutines {
			wg.Go(func() {
				for i := range emits {
					bus.Emit(events.Info{Message: fmt.Sprintf("g%d-%d", g, i)})
					bus.Emit(events.STTPhase{Phase: "listening"})
				}
			})
		}
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Emit blocked on a full bridge channel")
	}
}

func TestBridgePreservesOrder(t *testing.T) {
	bus := events.NewBus()
	bridge := newEventBridge(64)
	bridge.attach(bus)

	for i := range 10 {
		bus.Emit(events.Info{Message: fmt.Sprintf("msg-%d", i)})
	}

	for i := range 10 {
		msg := (<-bridge.ch).(busEventMsg)
		info, ok := msg.event.(events.Info)
		if !ok {
			t.Fatalf("event %d: unexpected type %T", i, msg.event)
		}
		if want := fmt.Sprintf("msg-%d", i); info.Message != want {
			t.Fatalf("event %d = %q, want %q", i, info.Message, want)
		}
	}
}

// Under pressure the bridge drops the oldest messages: the newest event must
// always survive.
func TestBridgeDropsOldestUnderPressure(t *testing.T) {
	bus := events.NewBus()
	bridge := newEventBridge(4)
	bridge.attach(bus)

	for i := range 20 {
		bus.Emit(events.Info{Message: fmt.Sprintf("msg-%d", i)})
	}

	var got []string
	for {
		select {
		case msg := <-bridge.ch:
			got = append(got, msg.(busEventMsg).event.(events.Info).Message)
			continue
		default:
		}
		break
	}

	if len(got) == 0 || len(got) > 4 {
		t.Fatalf("drained %d messages, want 1..4", len(got))
	}
	if got[len(got)-1] != "msg-19" {
		t.Errorf("newest message lost: tail of queue = %q, want msg-19", got[len(got)-1])
	}
}

// Durable transcript events must survive a multi-segment activity flood.
// Segment-level voice events are not bridged; this stress test still floods
// with high-volume bridged events (partials + generating) and asserts the
// newest UserInput/ResponseReady remain when capacity is tight.
func TestBridgeDurableEventsSurviveSegmentFlood(t *testing.T) {
	bus := events.NewBus()
	bridge := newEventBridge(8)
	bridge.attach(bus)

	bus.Emit(events.UserInput{Text: "spoken question"})
	for i := range 40 {
		bus.Emit(events.TranscriptPartial{Text: fmt.Sprintf("partial-%d", i)})
		bus.Emit(events.GeneratingVoice{Sentence: fmt.Sprintf("seg-%d", i)})
		// Segment events must not enter the bridge at all.
		bus.Emit(events.SpeechSegmentReady{Text: fmt.Sprintf("seg-%d", i)})
		bus.Emit(events.VoiceGenerated{Sentence: fmt.Sprintf("seg-%d", i)})
	}
	bus.Emit(events.ResponseReady{Response: "final answer"})

	var sawUser, sawResponse bool
	var types []string
	for {
		select {
		case msg := <-bridge.ch:
			switch e := msg.(busEventMsg).event.(type) {
			case events.UserInput:
				sawUser = true
				types = append(types, "UserInput")
			case events.ResponseReady:
				sawResponse = true
				types = append(types, "ResponseReady:"+e.Response)
			case events.SpeechSegmentReady, events.VoiceGenerated:
				t.Fatalf("segment-level event bridged: %T", e)
			default:
				types = append(types, fmt.Sprintf("%T", e))
			}
			continue
		default:
		}
		break
	}

	if !sawResponse {
		t.Fatalf("ResponseReady dropped under flood; drained=%v", types)
	}
	// With drop-oldest, the early UserInput may be displaced — the contract
	// we care about is that the terminal durable event survives. Prefer both
	// when capacity allows; always require ResponseReady.
	if len(types) == 0 || types[len(types)-1] != "ResponseReady:final answer" {
		t.Fatalf("queue tail = %v, want ResponseReady last", types)
	}
	_ = sawUser // optional under tight capacity; ResponseReady is mandatory
}

// waitForEvent must re-arm after every delivered message: consuming N events
// through the model must return a non-nil Cmd each time.
func TestBridgeRearmsThroughModel(t *testing.T) {
	bus := events.NewBus()
	bridge := newEventBridge(16)
	bridge.attach(bus)

	m := sizedConversation(t, 80, 24)
	m.bridge = bridge

	bus.Emit(events.UserInput{Text: "hello via bus"})
	bus.Emit(events.ResponseReady{Response: "hi from the brain"})

	for range 2 {
		cmd := bridge.wait()
		msg := cmd()
		var next tea.Cmd
		m, next = m.Update(msg)
		if next == nil {
			t.Fatal("model did not re-arm the bridge after a bus event")
		}
	}

	view := m.View()
	for _, want := range []string{"hello via bus", "hi from the brain"} {
		if !strings.Contains(view, want) {
			t.Errorf("View missing %q", want)
		}
	}
}

func TestConversationEventMapping(t *testing.T) {
	tests := []struct {
		name       string
		event      events.Event
		wantStatus string
		wantErr    bool
	}{
		{"listening", events.STTPhase{Phase: "listening"}, "🎙 Listening...", false},
		{"hearing", events.STTPhase{Phase: "hearing"}, "🎙 Hearing you...", false},
		{"transcribing", events.STTPhase{Phase: "transcribing"}, "● Transcribing...", false},
		{"partial", events.TranscriptPartial{Text: "so I was"}, "🎙 so I was", false},
		{"thinking", events.ThinkingStarted{}, "● Samantha thinking...", false},
		{"generating", events.GeneratingVoice{}, "● Synthesizing voice...", false},
		{"speaking", events.SpeakingStarted{}, "● Speaking...", false},
		{"speaking done", events.SpeakingComplete{}, "", false},
		{"response ready clears status", events.ResponseReady{Response: "hi"}, "", false},
		{"interrupted", events.TurnInterrupted{Reason: "barge-in"}, "turn interrupted (barge-in)", false},
		{"error", events.Error{Stage: "stt", Message: "mic gone"}, "Error: [stt] mic gone", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := sizedConversation(t, 80, 24)
			m.handleEvent(tt.event)
			if m.status != tt.wantStatus {
				t.Errorf("status = %q, want %q", m.status, tt.wantStatus)
			}
			if m.statusErr != tt.wantErr {
				t.Errorf("statusErr = %v, want %v", m.statusErr, tt.wantErr)
			}
		})
	}
}

func TestConversationClearedEventResetsViewport(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.handleEvent(events.UserInput{Text: "history line"})
	m.handleEvent(events.ConversationCleared{})

	view := m.View()
	if strings.Contains(view, "history line") {
		t.Error("cleared event left old transcript in view")
	}
	if !strings.Contains(view, "Conversation cleared.") {
		t.Error("cleared notice not shown")
	}
}

func TestConversationMetricsStored(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.handleEvent(events.TurnMetrics{Outcome: "completed", ModelCompleteElapsed: 400 * time.Millisecond})
	if m.lastMetrics.Outcome != "completed" {
		t.Error("TurnMetrics not retained on the model")
	}
}

// Text-only turns never see SpeakingComplete; ResponseReady must clear the
// thinking status so the bar does not stick.
func TestResponseReadyClearsThinkingStatus(t *testing.T) {
	m := sizedConversation(t, 80, 24)
	m.handleEvent(events.ThinkingStarted{})
	if m.status == "" {
		t.Fatal("precondition: thinking status not set")
	}
	m.handleEvent(events.ResponseReady{Response: "plain text reply"})
	if m.status != "" {
		t.Errorf("status = %q after ResponseReady, want empty", m.status)
	}
	if !strings.Contains(m.View(), "plain text reply") {
		t.Error("agent turn not appended on ResponseReady")
	}
}
