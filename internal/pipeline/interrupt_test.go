package pipeline

import (
	"context"
	"sync/atomic"
	"testing"
	"time"
)

// alwaysPlaying is a minimal playbackState that reports playback is active.
type alwaysPlaying struct{ playing atomic.Bool }

func (a *alwaysPlaying) IsPlaying() bool { return a.playing.Load() }

// newTestController wires a controller with small thresholds and an injected
// capture/vad/playback for deterministic tests.
func newTestController(capture captureMonitor, vad voiceDetector, playback playbackState, minSpeech int) *interruptController {
	return &interruptController{
		capture:    capture,
		vad:        vad,
		playback:   playback,
		armDelay:   bargeInArmDelay,
		minSpeech:  minSpeech,
		bufferSize: bargeInBufferSize,
	}
}

// armed returns an armAt that has already elapsed (barge-in armed now).
func armedNow() *atomic.Int64 {
	var armAt atomic.Int64
	armAt.Store(time.Now().Add(-time.Second).UnixNano())
	return &armAt
}

func TestInterruptControllerDisabledWhenMissingCollaborators(t *testing.T) {
	capture := newFakeCapture()
	vad := &fakeVAD{}
	playing := &alwaysPlaying{}
	playing.playing.Store(true)

	cases := map[string]*interruptController{
		"no capture":  newTestController(nil, vad, playing, 3),
		"no vad":      newTestController(capture, nil, playing, 3),
		"no playback": newTestController(capture, vad, nil, 3),
	}
	for name, c := range cases {
		if ch := c.watch(context.Background(), armedNow()); ch != nil {
			t.Errorf("%s: watch returned non-nil channel, want nil (disabled)", name)
		}
	}
	if got := len(capture.subs); got != 0 {
		t.Fatalf("capture subscriptions = %d, want 0 when disabled", got)
	}
}

func TestInterruptControllerFiresOnSustainedSpeech(t *testing.T) {
	capture := newFakeCapture()
	vad := &fakeVAD{}
	playing := &alwaysPlaying{}
	playing.playing.Store(true)

	c := newTestController(capture, vad, playing, 3)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := c.watch(ctx, armedNow())
	if out == nil {
		t.Fatal("watch returned nil with all collaborators present")
	}

	// Three consecutive speech chunks meet the threshold and trip barge-in.
	for range 3 {
		capture.Publish([]float32{0.9, 0.9, 0.9})
		time.Sleep(5 * time.Millisecond)
	}

	select {
	case req := <-out:
		if req.Reason != "barge_in" {
			t.Fatalf("interruptRequest.Reason = %q, want barge_in", req.Reason)
		}
		if req.At.IsZero() {
			t.Fatal("interruptRequest.At is zero")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("controller did not fire after sustained speech")
	}
}

func TestInterruptControllerDoesNotFireBelowThreshold(t *testing.T) {
	capture := newFakeCapture()
	vad := &fakeVAD{}
	playing := &alwaysPlaying{}
	playing.playing.Store(true)

	c := newTestController(capture, vad, playing, 5)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := c.watch(ctx, armedNow())

	// Two speech chunks then silence — below the 5-chunk threshold, and the
	// silence resets the counter, so no barge-in.
	capture.Publish([]float32{0.9, 0.9})
	time.Sleep(5 * time.Millisecond)
	capture.Publish([]float32{0.9, 0.9})
	time.Sleep(5 * time.Millisecond)
	capture.Publish([]float32{0.0, 0.0}) // silence resets
	time.Sleep(5 * time.Millisecond)

	select {
	case <-out:
		t.Fatal("controller fired below the speech threshold")
	case <-time.After(120 * time.Millisecond):
	}
}

func TestInterruptControllerHonorsArmDelay(t *testing.T) {
	capture := newFakeCapture()
	vad := &fakeVAD{}
	playing := &alwaysPlaying{}
	playing.playing.Store(true)

	c := newTestController(capture, vad, playing, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Not yet armed: armAt is far in the future, so speech is ignored.
	var armAt atomic.Int64
	armAt.Store(time.Now().Add(time.Hour).UnixNano())
	out := c.watch(ctx, &armAt)

	for range 4 {
		capture.Publish([]float32{0.9, 0.9})
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-out:
		t.Fatal("controller fired while disarmed (inside arm delay)")
	case <-time.After(80 * time.Millisecond):
	}

	// Now arm and send sustained speech — it must fire.
	armAt.Store(time.Now().Add(-time.Second).UnixNano())
	for range 2 {
		capture.Publish([]float32{0.9, 0.9})
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-out:
	case <-time.After(2 * time.Second):
		t.Fatal("controller did not fire after arming")
	}
}

func TestInterruptControllerDisarmedWhenNotPlaying(t *testing.T) {
	capture := newFakeCapture()
	vad := &fakeVAD{}
	playing := &alwaysPlaying{} // playing=false

	c := newTestController(capture, vad, playing, 2)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := c.watch(ctx, armedNow())

	for range 4 {
		capture.Publish([]float32{0.9, 0.9})
		time.Sleep(5 * time.Millisecond)
	}
	select {
	case <-out:
		t.Fatal("controller fired while no playback was active")
	case <-time.After(80 * time.Millisecond):
	}
}

func TestInterruptControllerUnsubscribesOnCancel(t *testing.T) {
	capture := newFakeCapture()
	vad := &fakeVAD{}
	playing := &alwaysPlaying{}
	playing.playing.Store(true)

	c := newTestController(capture, vad, playing, 3)
	ctx, cancel := context.WithCancel(context.Background())
	_ = c.watch(ctx, armedNow())

	waitForCond(t, func() bool { return capture.subCount() == 1 }, time.Second,
		"controller did not subscribe to capture")

	cancel()

	waitForCond(t, func() bool { return capture.subCount() == 0 }, time.Second,
		"controller did not unsubscribe from capture after cancellation")
	if vad.clearedCount() == 0 {
		t.Fatal("controller did not clear the VAD on exit")
	}
}
