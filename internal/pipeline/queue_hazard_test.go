package pipeline

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/events"
)

// waitForCond polls cond until it is true or the deadline elapses.
func waitForCond(t *testing.T, cond func() bool, within time.Duration, msg string) {
	t.Helper()
	deadline := time.After(within)
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatal(msg)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

// TestWatchPlaybackAbandonsSendWhenLoopDone is the direct regression for the
// blocking-send hazard: a watcher whose event channel has no reader must still
// return once loopDone closes, instead of blocking forever on the send.
func TestWatchPlaybackAbandonsSendWhenLoopDone(t *testing.T) {
	loopDone := make(chan struct{})
	out := make(chan playbackEvent) // unbuffered, deliberately never read
	started := make(chan struct{})
	done := make(chan audio.PlaybackResult, 1)
	playback := audio.NewPlayback(started, done)
	var audioStarted atomic.Bool

	finished := make(chan struct{})
	go func() {
		watchPlayback(loopDone, "hi", time.Now(), playback, &audioStarted, out)
		close(finished)
	}()

	// Drive the playback to done so the watcher tries to emit playbackFinished;
	// with no reader on out it must block on the send.
	done <- audio.PlaybackResult{}
	time.Sleep(30 * time.Millisecond)
	select {
	case <-finished:
		t.Fatal("watchPlayback returned before loopDone closed; the send did not block as expected")
	default:
	}

	// Closing loopDone must release the wedged send and let the watcher exit.
	close(loopDone)
	select {
	case <-finished:
	case <-time.After(time.Second):
		t.Fatal("watchPlayback did not return after loopDone closed — blocking send leaked")
	}
}

func TestRunTurnCancellationUnderFullQueueReturnsPromptly(t *testing.T) {
	bus := events.NewBus()
	// Playbacks never finish on their own, so the queue saturates and stays full.
	player := newFakePlayer(10 * time.Second)
	defer player.Close()
	ttsProvider := &fakeTTS{delay: time.Millisecond}

	p := &Pipeline{
		STT:    &fakeSTT{text: "hello"},
		Brain:  &fakeBrain{chunks: []string{"One. Two. Three. Four. Five. Six."}},
		TTS:    ttsProvider,
		Player: player,
		Events: bus,
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := p.RunTurn(ctx)
		done <- err
	}()

	// Wait until the playback queue is full (voiceQueueDepth segments synthesized
	// and enqueued), then cancel.
	waitForCond(t, func() bool { return len(ttsProvider.CallTimes()) >= voiceQueueDepth },
		2*time.Second, "playback queue did not fill")
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("RunTurn() error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RunTurn did not return promptly after cancellation under a full queue")
	}
}

func TestRunTurnBargeInUnderFullQueueReturnsPromptly(t *testing.T) {
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

	// Long playbacks keep multiple segments pending so the queue is full when the
	// barge-in arrives.
	player := newFakePlayer(10 * time.Second)
	defer player.Close()
	capture := newFakeCapture()
	vad := &fakeVAD{}

	p := &Pipeline{
		STT:        &fakeSTT{text: "hello"},
		Brain:      &fakeBrain{chunks: []string{"One. Two. Three. Four. Five. Six."}},
		TTS:        &fakeTTS{delay: time.Millisecond},
		Player:     player,
		Capture:    capture,
		VAD:        &fakeVAD{},
		BargeInVAD: vad,
		Events:     bus,
	}

	done := make(chan error, 1)
	go func() {
		_, err := p.RunTurn(context.Background())
		done <- err
	}()

	select {
	case <-player.StartedSignal():
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for playback to start")
	}

	time.Sleep(bargeInArmDelay + 80*time.Millisecond)
	for range bargeInMinSpeechChunks {
		capture.Publish([]float32{0.9, 0.9, 0.9})
		time.Sleep(60 * time.Millisecond)
	}

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("RunTurn() error = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("barge-in under a full queue did not terminate promptly")
	}

	select {
	case <-responseSeen:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for ResponseReady")
	}
	if !response.Interrupted {
		t.Fatal("ResponseReady.Interrupted = false, want true")
	}
}
