package audio

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"
)

// The playback readiness gate (setReadyFrames / maybeReadyLocked / waitReady)
// is the path every spoken sentence passes through. These tests pin its
// semantics: full-buffer playback for batch-generated audio, an optional
// streaming threshold, the input-done fallback, and append/writeTo locking
// exercised under -race.

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

func TestPlaybackSegmentZeroThresholdWaitsForCompleteInput(t *testing.T) {
	// Kokoro generates a complete sentence before publishing PCM. A zero
	// threshold keeps the device callback away from that buffer until the pump
	// has copied the whole sentence, preventing mid-sentence underruns.
	s := newPlaybackSegment()
	s.setReadyFrames(0)

	s.append([]int16{1})
	if segmentReady(s) {
		t.Fatal("segment ready before input completed with a zero threshold")
	}

	s.finishInput(nil)
	if !segmentReady(s) {
		t.Fatal("segment not ready after input completed with a zero threshold")
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

// TestFinalizeSegmentPlaysPartialAudioOnStreamError pins the pumpSegment
// finalize path: a stream that fails partway through (e.g. a cancelled turn)
// must still hand off whatever audio it already produced instead of
// discarding the buffered utterance, with the failure surfacing afterward on
// the segment's terminal result.
func TestFinalizeSegmentPlaysPartialAudioOnStreamError(t *testing.T) {
	s := newPlaybackSegment()
	s.setReadyFrames(0)

	wantErr := errors.New("stream failed mid-utterance")
	samples := []float32{0.1, 0.2, 0.3, 0.4}
	finalizeSegment(s, nil, nil, samples, 24_000, 24_000, wantErr)

	if !segmentReady(s) {
		t.Fatal("segment not ready after finalizeSegment on a failed stream")
	}
	if pending := s.pendingLocked(); pending != len(samples) {
		t.Fatalf("pending samples = %d, want %d; partial audio must still be queued for playback", pending, len(samples))
	}

	out := make([]byte, len(samples)*2)
	written, finished := s.writeTo(out, len(samples))
	if written != len(samples) || !finished {
		t.Fatalf("writeTo() = (%d, %v), want (%d, true)", written, finished, len(samples))
	}
	s.complete()

	select {
	case result := <-s.doneCh:
		if !errors.Is(result.Err, wantErr) {
			t.Fatalf("PlaybackResult.Err = %v, want %v", result.Err, wantErr)
		}
	default:
		t.Fatal("segment did not deliver a terminal result after playing its partial audio")
	}
}

// TestFinalizeSegmentResamplesAndSucceeds pins the happy path: a clean stream
// resamples to the device's output rate and finishes with no error.
func TestFinalizeSegmentResamplesAndSucceeds(t *testing.T) {
	s := newPlaybackSegment()
	s.setReadyFrames(0)

	samples := []float32{0, 0.25, 0.5, 0.75, 1, 0.75, 0.5, 0.25}
	finalizeSegment(s, nil, nil, samples, 24_000, 48_000, nil)

	if !segmentReady(s) {
		t.Fatal("segment not ready after finalizeSegment on a clean stream")
	}
	if pending := s.pendingLocked(); pending != 2*len(samples) {
		t.Fatalf("pending samples = %d, want %d after resampling 24kHz to 48kHz", pending, 2*len(samples))
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
