package pipeline

import (
	"context"
	"math/rand"
	"sync"
	"time"

	"github.com/lancekrogers/samantha/pkg/voiceagent/audio"
	"github.com/lancekrogers/samantha/pkg/voiceagent/events"
	"github.com/lancekrogers/samantha/pkg/voiceagent/tts"
)

// Everything about the backchannel lives in this file, on purpose.
//
// This is the item most likely to be removed on taste after dogfooding, and the
// plan says so up front. Keeping it in one file plus one config flag means
// abandoning it is a trivial revert rather than an archaeology exercise.
//
// What it does: when the agent is going to be slow to answer, play a short "mm-hm"
// so the user is not left listening to silence. Decision D010 authorized building
// it from measured numbers — the wait between a user finishing speaking and the
// agent starting to speak has a p50 of 2385 ms, three times the threshold at
// which this would have been cancelled as masking a wait that no longer happens.

// backchannelThreshold is the rolling p50 from final transcript to first audio
// above which a filler is worth playing.
//
// Below this the turn is fast enough that a filler would arrive on top of the
// real answer. **That is the failure mode that makes this feel scripted**, and it
// is worse than silence — so the gate is conservative and fast turns get nothing.
const backchannelThreshold = 900 * time.Millisecond

// backchannelWindow is how many recent turns the rolling p50 considers. Short
// enough to react when a model gets slower, long enough that one outlier does not
// start the agent filling every turn.
const backchannelWindow = 8

// backchannelPhrases are synthesized once at startup and held as PCM. Kokoro is
// warm and fast, so the cost is milliseconds per phrase.
var backchannelPhrases = []string{
	"Mm-hm.",
	"Let me think.",
	"Okay.",
	"Right.",
	"Hmm.",
	"One sec.",
}

// backchannel holds pre-synthesized filler audio and decides when to play it.
type backchannel struct {
	mu sync.Mutex

	// clips is parallel to backchannelPhrases; a phrase whose synthesis failed
	// is simply absent rather than blocking the others.
	clips []backchannelClip

	// recent is a ring of the last backchannelWindow first-audio latencies.
	recent  []time.Duration
	lastIdx int // never play the same phrase twice consecutively

	rnd *rand.Rand
}

type backchannelClip struct {
	phrase  string
	samples []float32
	rate    int
}

// newBackchannel synthesizes the phrase pool. It is called once, at startup, and
// a failure to synthesize any given phrase is not fatal — the pool simply has one
// fewer option.
func newBackchannel(ctx context.Context, synth tts.Provider, seed int64) *backchannel {
	b := &backchannel{lastIdx: -1, rnd: rand.New(rand.NewSource(seed))}
	for _, phrase := range backchannelPhrases {
		clip, err := synthesizeClip(ctx, synth, phrase)
		if err != nil {
			continue
		}
		b.clips = append(b.clips, clip)
	}
	return b
}

func synthesizeClip(ctx context.Context, synth tts.Provider, phrase string) (backchannelClip, error) {
	stream, err := synth.Synthesize(ctx, phrase)
	if err != nil {
		return backchannelClip{}, err
	}
	rate, err := stream.WaitReady(ctx)
	if err != nil {
		return backchannelClip{}, err
	}
	var samples []float32
	for frame := range stream.Frames() {
		samples = append(samples, frame...)
	}
	if err := stream.Err(); err != nil {
		return backchannelClip{}, err
	}
	return backchannelClip{phrase: phrase, samples: samples, rate: rate}, nil
}

// observe records a turn's first-audio latency so the rolling p50 tracks reality
// rather than a number fixed at startup.
func (b *backchannel) observe(d time.Duration) {
	if d <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.recent = append(b.recent, d)
	if len(b.recent) > backchannelWindow {
		b.recent = b.recent[len(b.recent)-backchannelWindow:]
	}
}

// shouldPlay reports whether recent turns have been slow enough to be worth
// covering.
//
// With no history it returns false: the first turn of a session gets no filler.
// Guessing on no evidence is exactly what the heuristic exists to avoid, and a
// wrong guess on turn one is the worst possible first impression.
func (b *backchannel) shouldPlay() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.clips) == 0 || len(b.recent) == 0 {
		return false
	}
	return medianDuration(b.recent) > backchannelThreshold
}

func medianDuration(in []time.Duration) time.Duration {
	sorted := make([]time.Duration, len(in))
	copy(sorted, in)
	for i := 1; i < len(sorted); i++ {
		for j := i; j > 0 && sorted[j] < sorted[j-1]; j-- {
			sorted[j], sorted[j-1] = sorted[j-1], sorted[j]
		}
	}
	mid := len(sorted) / 2
	if len(sorted)%2 == 1 {
		return sorted[mid]
	}
	return (sorted[mid-1] + sorted[mid]) / 2
}

// pick chooses a clip at random, never the same one twice in a row. Repetition
// is what turns a humanising touch into an obvious loop.
func (b *backchannel) pick() (backchannelClip, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	switch len(b.clips) {
	case 0:
		return backchannelClip{}, false
	case 1:
		b.lastIdx = 0
		return b.clips[0], true
	}
	idx := b.rnd.Intn(len(b.clips))
	if idx == b.lastIdx {
		idx = (idx + 1) % len(b.clips)
	}
	b.lastIdx = idx
	return b.clips[idx], true
}

// play sends a filler through the normal Player path.
//
// The filler gets its own cancelable context. The first real segment cancels it
// and stops Player immediately before enqueueing the answer, so a slow or queued
// filler can never add its duration to response latency.
func (p *Pipeline) playBackchannel(ctx context.Context) {
	if p.backchannel == nil || p.Player == nil || !p.backchannel.shouldPlay() {
		return
	}
	clip, ok := p.backchannel.pick()
	if !ok {
		return
	}

	fillerCtx, cancel := context.WithCancel(ctx)
	p.backchannelMu.Lock()
	if p.backchannelCancel != nil {
		p.backchannelCancel()
	}
	p.backchannelCancel = cancel
	p.backchannelMu.Unlock()

	stream := audio.NewPCMStream(fillerCtx)
	if err := stream.SetSampleRate(clip.rate); err != nil {
		return
	}
	go func() {
		defer stream.Close()
		_ = stream.Write(clip.samples)
	}()

	p.emit(events.BackchannelStarted{Phrase: clip.phrase})
	go func() {
		_, _ = p.Player.PlayStream(fillerCtx, stream)
	}()
}

func (p *Pipeline) stopBackchannel() {
	p.backchannelMu.Lock()
	cancel := p.backchannelCancel
	p.backchannelCancel = nil
	p.backchannelMu.Unlock()
	if cancel == nil {
		return
	}
	cancel()
	if p.Player != nil {
		p.Player.Stop()
	}
}

// EnableBackchannel synthesizes the filler pool and turns the feature on.
//
// Exported so construction happens once, at startup, where paying for six short
// syntheses is invisible — doing it lazily on the first slow turn would add
// latency to exactly the turn the feature exists to make feel faster.
func (p *Pipeline) EnableBackchannel(ctx context.Context) {
	p.ttsMu.RLock()
	provider := p.TTS
	p.ttsMu.RUnlock()
	if provider == nil {
		return
	}
	p.backchannel = newBackchannel(ctx, provider, time.Now().UnixNano())
}
