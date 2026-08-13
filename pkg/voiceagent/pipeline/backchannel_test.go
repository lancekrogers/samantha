package pipeline

import (
	"math/rand"
	"testing"
	"time"
)

// Error cases first: every state in which a filler must NOT play. A filler on a
// fast turn is the failure mode that makes this feel scripted, and it is worse
// than the silence it was meant to cover.

func TestShouldNotPlay(t *testing.T) {
	clip := backchannelClip{phrase: "Mm-hm.", samples: []float32{0, 0}, rate: 24000}

	tests := []struct {
		name string
		b    *backchannel
	}{
		{
			// The first turn of a session has no history. Guessing on no
			// evidence is exactly what the heuristic exists to avoid, and a
			// wrong guess on turn one is the worst possible first impression.
			name: "no history yet",
			b:    &backchannel{clips: []backchannelClip{clip}},
		},
		{
			name: "turns are fast",
			b: &backchannel{clips: []backchannelClip{clip}, recent: []time.Duration{
				200 * time.Millisecond, 300 * time.Millisecond, 250 * time.Millisecond,
			}},
		},
		{
			// Exactly at the threshold is not above it.
			name: "exactly at the threshold",
			b: &backchannel{clips: []backchannelClip{clip}, recent: []time.Duration{
				backchannelThreshold, backchannelThreshold,
			}},
		},
		{
			// Every phrase failed to synthesize. Nothing to play, and the gate
			// must not pretend otherwise.
			name: "empty phrase pool",
			b:    &backchannel{recent: []time.Duration{5 * time.Second, 5 * time.Second}},
		},
		{
			// One slow outlier among fast turns must not start the agent
			// filling every turn — that is why the gate uses a median.
			name: "single slow outlier among fast turns",
			b: &backchannel{clips: []backchannelClip{clip}, recent: []time.Duration{
				200 * time.Millisecond, 10 * time.Second, 250 * time.Millisecond,
				300 * time.Millisecond, 220 * time.Millisecond,
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.b.shouldPlay() {
				t.Error("shouldPlay() = true; a filler here is worse than silence")
			}
		})
	}
}

func TestShouldPlayWhenTurnsAreConsistentlySlow(t *testing.T) {
	b := &backchannel{
		clips: []backchannelClip{{phrase: "Mm-hm.", rate: 24000}},
		recent: []time.Duration{
			2 * time.Second, 2400 * time.Millisecond, 1800 * time.Millisecond,
		},
	}
	if !b.shouldPlay() {
		t.Error("shouldPlay() = false with a 2s median; this is the case the feature exists for")
	}
}

// The rolling window must actually roll, or the gate answers with startup
// conditions forever.
func TestObserveKeepsOnlyTheRecentWindow(t *testing.T) {
	b := &backchannel{clips: []backchannelClip{{phrase: "Mm-hm."}}}

	// Fill the window with slow turns, then push it out with fast ones.
	for range backchannelWindow {
		b.observe(3 * time.Second)
	}
	if !b.shouldPlay() {
		t.Fatal("expected the gate to open after a run of slow turns")
	}
	for range backchannelWindow {
		b.observe(100 * time.Millisecond)
	}
	if b.shouldPlay() {
		t.Error("the gate stayed open after turns got fast; the window is not rolling")
	}
	if len(b.recent) != backchannelWindow {
		t.Errorf("recent holds %d entries, want %d", len(b.recent), backchannelWindow)
	}
}

// A zero or negative duration is a metric that was never stamped. Recording it
// would drag the median down and silently disable the feature.
func TestObserveIgnoresUnstampedMetrics(t *testing.T) {
	b := &backchannel{clips: []backchannelClip{{phrase: "Mm-hm."}}}
	b.observe(0)
	b.observe(-1 * time.Second)
	if len(b.recent) != 0 {
		t.Errorf("recent = %v, want empty — an unstamped metric is not a fast turn", b.recent)
	}
}

// Repetition is what turns a humanising touch into an obvious loop.
func TestPickNeverRepeatsConsecutively(t *testing.T) {
	b := &backchannel{lastIdx: -1, rnd: deterministicRand()}
	for _, p := range []string{"a", "b", "c", "d"} {
		b.clips = append(b.clips, backchannelClip{phrase: p})
	}

	prev := ""
	for i := range 200 {
		clip, ok := b.pick()
		if !ok {
			t.Fatal("pick() returned no clip from a non-empty pool")
		}
		if clip.phrase == prev {
			t.Fatalf("iteration %d: %q played twice in a row", i, clip.phrase)
		}
		prev = clip.phrase
	}
}

// With one phrase there is no alternative, so the no-repeat rule cannot be
// honoured. It must still return the clip rather than deadlock looking for a
// different one.
func TestPickWithASinglePhrase(t *testing.T) {
	b := &backchannel{lastIdx: -1, rnd: deterministicRand()}
	b.clips = []backchannelClip{{phrase: "only"}}
	for range 3 {
		clip, ok := b.pick()
		if !ok || clip.phrase != "only" {
			t.Fatalf("pick() = %q, %v; want the single clip", clip.phrase, ok)
		}
	}
}

func TestPickFromEmptyPool(t *testing.T) {
	b := &backchannel{lastIdx: -1, rnd: deterministicRand()}
	if _, ok := b.pick(); ok {
		t.Error("pick() reported a clip from an empty pool")
	}
}

func TestMedianDuration(t *testing.T) {
	tests := []struct {
		name string
		in   []time.Duration
		want time.Duration
	}{
		{"odd count", []time.Duration{3, 1, 2}, 2},
		{"even count averages the middle pair", []time.Duration{4, 1, 3, 2}, 2},
		{"single", []time.Duration{7}, 7},
		{"already sorted", []time.Duration{1, 2, 3, 4, 5}, 3},
		{"reverse sorted", []time.Duration{5, 4, 3, 2, 1}, 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := medianDuration(tt.in); got != tt.want {
				t.Errorf("medianDuration(%v) = %v, want %v", tt.in, got, tt.want)
			}
		})
	}
}

// A nil backchannel is the default state — the feature is off unless configured.
// Every entry point must tolerate it.
func TestPlayBackchannelIsANoOpWhenDisabled(t *testing.T) {
	p := &Pipeline{}
	p.playBackchannel(t.Context()) // must not panic
}

func deterministicRand() *rand.Rand { return rand.New(rand.NewSource(1)) }
