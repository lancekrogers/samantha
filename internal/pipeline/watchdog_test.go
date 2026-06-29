package pipeline

import (
	"testing"
	"time"
)

func TestPlaybackStalled(t *testing.T) {
	base := time.Now()

	cases := []struct {
		name       string
		synthStart time.Time
		started    bool
		now        time.Time
		want       bool
	}{
		{"synthesis not started", time.Time{}, false, base.Add(time.Hour), false},
		{"playback already started", base, true, base.Add(time.Hour), false},
		{"within timeout", base, false, base.Add(playbackStallTimeout - time.Millisecond), false},
		{"exactly at timeout", base, false, base.Add(playbackStallTimeout), true},
		{"well past timeout", base, false, base.Add(time.Minute), true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := playbackStalled(tc.synthStart, tc.started, tc.now, playbackStallTimeout)
			if got != tc.want {
				t.Fatalf("playbackStalled() = %v, want %v", got, tc.want)
			}
		})
	}
}
