package audio

import (
	"context"
	"sync"
	"testing"
	"time"
)

// The playback readiness gate (setReadyFrames / maybeReadyLocked / waitReady)
// is the path every spoken sentence passes through. These tests pin its
// semantics after a fix→revert churn left it uncovered: the streaming
// threshold, the zero-clamp, the input-done fallback, and the append/writeTo
// locking exercised under -race.

func segmentReady(s *playbackSegment) bool {
	select {
	case <-s.ready:
		return true
	default:
		return false
	}
}

func TestPlaybackSegmentThresholdReadiness(t *testing.T) {
	s := newPlaybackSegment()
	s.setReadyFrames(3)

	s.append([]int16{1})
	s.append([]int16{2})
	if segmentReady(s) {
		t.Fatal("segment ready below the frame threshold")
	}

	s.append([]int16{3})
	if !segmentReady(s) {
		t.Fatal("segment not ready after reaching the frame threshold")
	}
}

func TestPlaybackSegmentZeroThresholdClampsToOneFrame(t *testing.T) {
	// setReadyFrames(0) must mean "ready on the first frame", not "wait for the
	// whole input": readiness gated on inputDone defers playback start to full
	// synthesis and stalls streaming TTS.
	s := newPlaybackSegment()
	s.setReadyFrames(0)

	s.append([]int16{1})
	if !segmentReady(s) {
		t.Fatal("segment not ready after first frame with a zero threshold (clamp lost)")
	}
}

func TestPlaybackSegmentFinishInputBelowThresholdStillReady(t *testing.T) {
	// A short utterance that never reaches the threshold must become ready when
	// input finishes, or waitReady would block forever on short sentences.
	s := newPlaybackSegment()
	s.setReadyFrames(1000)
	s.append([]int16{1, 2, 3})
	if segmentReady(s) {
		t.Fatal("segment ready below threshold before input finished")
	}

	s.finishInput(nil)
	if !segmentReady(s) {
		t.Fatal("segment not ready after finishInput below the threshold")
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := s.waitReady(ctx); err != nil {
		t.Fatalf("waitReady() error = %v, want nil for a short finished segment", err)
	}
}

// TestPlaybackSegmentConcurrentAppendAndWriteTo drives the pump-side append and
// the device-callback read concurrently; run under -race it pins that every
// access to the shared sample buffer stays behind the segment mutex.
func TestPlaybackSegmentConcurrentAppendAndWriteTo(t *testing.T) {
	s := newPlaybackSegment()
	s.setReadyFrames(1)

	const frames = 4096
	var wg sync.WaitGroup
	wg.Add(2)

	go func() {
		defer wg.Done()
		chunk := make([]int16, 64)
		for i := range chunk {
			chunk[i] = int16(i)
		}
		for written := 0; written < frames; written += len(chunk) {
			s.append(chunk)
		}
		s.finishInput(nil)
	}()

	go func() {
		defer wg.Done()
		out := make([]byte, 128*2)
		for {
			_, finished := s.writeTo(out, 128)
			if finished {
				return
			}
		}
	}()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent append/writeTo did not finish")
	}
}
