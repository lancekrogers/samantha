package audio

import (
	"context"
	"testing"
	"time"
)

func TestPlaybackSegmentFullBufferReadiness(t *testing.T) {
	segment := newPlaybackSegment()
	segment.setReadyFrames(0)
	segment.append([]int16{1, 2, 3})

	select {
	case <-segment.ready:
		t.Fatal("segment became ready before input completed")
	default:
	}

	segment.finishInput(nil)

	select {
	case <-segment.ready:
	case <-time.After(time.Second):
		t.Fatal("segment did not become ready after input completed")
	}

	if err := segment.waitReady(context.Background()); err != nil {
		t.Fatalf("waitReady() error = %v", err)
	}
}

func TestPlaybackSegmentThresholdReadiness(t *testing.T) {
	segment := newPlaybackSegment()
	segment.setReadyFrames(3)
	segment.append([]int16{1, 2})

	select {
	case <-segment.ready:
		t.Fatal("segment became ready before threshold")
	default:
	}

	segment.append([]int16{3})

	select {
	case <-segment.ready:
	case <-time.After(time.Second):
		t.Fatal("segment did not become ready at threshold")
	}
}
