package netapi

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"sync"
	"sync/atomic"

	"github.com/lancekrogers/samantha/internal/audio"
)

// Audio format constants for the WebSocket wire protocol. PCM s16le mono is
// the smallest common denominator a phone browser or ffplay can play without
// a demuxer.
const (
	audioWireFormat   = "pcm_s16le"
	audioWireChannels = 1
	// maxAudioChunkFrames caps one envelope so a single base64 payload stays
	// small on slow mobile links (~32 KiB raw ≈ ~43 KiB base64).
	maxAudioChunkFrames = 16 * 1024
)

// AudioFanout is an audio.Engine that tees TTS PCM to:
//  1. an optional local speaker (real Player, or nil to mute the host), and
//  2. any WebSocket clients that opted into audio_output mode "stream".
//
// It is the Phase 3 seam: pipeline.Player stays an Engine; no pipeline changes.
type AudioFanout struct {
	local audio.Engine
	hub   *hub

	// segmentID is a monotonic counter so clients can group chunks that
	// belong to one PlayStream call.
	segmentID atomic.Uint64
}

// NewAudioFanout builds a fanout. local may be nil (host speaker muted).
// Call AttachHub before the first PlayStream so stream clients receive chunks.
func NewAudioFanout(local audio.Engine) *AudioFanout {
	return &AudioFanout{local: local}
}

// AttachHub wires the server's connection hub. Safe to call once before serve.
func (a *AudioFanout) AttachHub(h *hub) {
	a.hub = h
}

// PlayStream drains the TTS stream, pushes wire chunks to stream clients, and
// optionally forwards a teed copy to the local speaker. Matches the Engine
// contract: returns only after the first frames are ready (or the stream fails).
func (a *AudioFanout) PlayStream(ctx context.Context, stream *audio.PCMStream) (*audio.Playback, error) {
	if stream == nil {
		return nil, errors.New("nil pcm stream")
	}

	sampleRate, err := stream.WaitReady(ctx)
	if err != nil {
		return nil, err
	}

	segID := a.segmentID.Add(1)

	var localStream *audio.PCMStream
	// localReady delivers the local Playback (or a start error). PlayStream
	// on the real Player blocks until its initial buffer fills, so it must
	// run in its own goroutine while we keep writing frames into localStream.
	// Nil when host speaker is muted — waitLocalDone must not block on it.
	var localReady chan localStart
	if a.local != nil {
		localStream = audio.NewPCMStream(ctx)
		if err := localStream.SetSampleRate(sampleRate); err != nil {
			return nil, err
		}
		localReady = make(chan localStart, 1)
		go func() {
			pb, err := a.local.PlayStream(ctx, localStream)
			localReady <- localStart{playback: pb, err: err}
		}()
	}

	started := make(chan struct{})
	done := make(chan audio.PlaybackResult, 1)
	seg := &fanoutSegment{started: started, done: done}

	go a.pump(ctx, stream, localStream, localReady, sampleRate, segID, seg)

	// Wait until first chunk is ready or the pump fails/finishes empty.
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-started:
	case result := <-done:
		if result.Err != nil {
			return nil, result.Err
		}
		if result.Interrupted {
			return nil, context.Canceled
		}
		return nil, errors.New("pcm stream produced no samples")
	}

	return audio.NewPlayback(started, done), nil
}

type localStart struct {
	playback *audio.Playback
	err      error
}

func (a *AudioFanout) pump(
	ctx context.Context,
	src *audio.PCMStream,
	local *audio.PCMStream,
	localReady <-chan localStart,
	sampleRate int,
	segID uint64,
	seg *fanoutSegment,
) {
	var (
		first         bool
		localPlayback *audio.Playback
		localFailed   bool
	)

	// Non-blocking poll so we notice local start without stalling the drain.
	takeLocal := func() {
		if localReady == nil || localPlayback != nil || localFailed {
			return
		}
		select {
		case start := <-localReady:
			if start.err != nil {
				localFailed = true
				return
			}
			localPlayback = start.playback
		default:
		}
	}

	waitLocalDone := func(fallback audio.PlaybackResult) audio.PlaybackResult {
		takeLocal()
		// Local PlayStream may still be blocked on its initial buffer — wait.
		if localPlayback == nil && localReady != nil && !localFailed {
			select {
			case start := <-localReady:
				if start.err != nil {
					return fallback
				}
				localPlayback = start.playback
			case <-ctx.Done():
				return audio.PlaybackResult{Interrupted: true, Err: ctx.Err()}
			}
		}
		if localPlayback == nil {
			return fallback
		}
		select {
		case r := <-localPlayback.Done():
			return r
		case <-ctx.Done():
			return audio.PlaybackResult{Interrupted: true, Err: ctx.Err()}
		}
	}

	finish := func(result audio.PlaybackResult, hadAudio bool) {
		if hadAudio {
			a.emitAudioEnd(segID, result)
		}
		seg.finish(result, hadAudio)
	}

	for {
		takeLocal()
		select {
		case <-ctx.Done():
			if local != nil {
				local.CloseWithError(ctx.Err())
			}
			finish(waitLocalDone(audio.PlaybackResult{Interrupted: true, Err: ctx.Err()}), first)
			return

		case frames, ok := <-src.Frames():
			if !ok {
				if local != nil {
					local.CloseWithError(src.Err())
				}
				fallback := audio.PlaybackResult{Err: src.Err()}
				finish(waitLocalDone(fallback), first)
				return
			}
			if len(frames) == 0 {
				continue
			}

			a.emitAudioChunks(segID, sampleRate, frames)
			if local != nil && !localFailed {
				if err := local.Write(frames); err != nil && ctx.Err() == nil {
					// Local write failed mid-stream: keep remote chunks going.
					local.CloseWithError(err)
					local = nil
					localFailed = true
				}
			}

			if !first {
				first = true
				seg.markStarted()
			}
		}
	}
}

func (a *AudioFanout) emitAudioChunks(segID uint64, sampleRate int, frames []float32) {
	if a.hub == nil {
		return
	}
	for len(frames) > 0 {
		n := len(frames)
		if n > maxAudioChunkFrames {
			n = maxAudioChunkFrames
		}
		pcm := float32ToPCM16LE(frames[:n])
		msg, err := marshalAudioChunk(segID, sampleRate, pcm)
		if err == nil {
			a.hub.broadcastAudio(msg)
		}
		frames = frames[n:]
	}
}

func (a *AudioFanout) emitAudioEnd(segID uint64, result audio.PlaybackResult) {
	if a.hub == nil {
		return
	}
	reason := "complete"
	if result.Interrupted {
		reason = "interrupted"
	} else if result.Err != nil {
		reason = "error"
	}
	msg, err := marshalAudioEnd(segID, reason)
	if err == nil {
		a.hub.broadcastAudio(msg)
	}
}

// Stop interrupts local playback when present.
func (a *AudioFanout) Stop() {
	if a.local != nil {
		a.local.Stop()
	}
}

// IsPlaying reports local speaker state (remote clients are not "playing" here).
func (a *AudioFanout) IsPlaying() bool {
	if a.local == nil {
		return false
	}
	return a.local.IsPlaying()
}

// Close releases the local engine.
func (a *AudioFanout) Close() error {
	if a.local == nil {
		return nil
	}
	return a.local.Close()
}

type fanoutSegment struct {
	started  chan struct{}
	done     chan audio.PlaybackResult
	once     sync.Once
	doneOnce sync.Once
}

func (s *fanoutSegment) markStarted() {
	s.once.Do(func() { close(s.started) })
}

// finish delivers the terminal result. When hadAudio is false, only done is
// signaled so PlayStream can surface "no samples" instead of a fake start.
func (s *fanoutSegment) finish(result audio.PlaybackResult, hadAudio bool) {
	s.doneOnce.Do(func() {
		if hadAudio {
			s.markStarted()
		}
		s.done <- result
		close(s.done)
	})
}

func float32ToPCM16LE(samples []float32) []byte {
	out := make([]byte, len(samples)*2)
	for i, sample := range samples {
		if sample > 1 {
			sample = 1
		} else if sample < -1 {
			sample = -1
		}
		v := int16(sample * float32(math.MaxInt16))
		binary.LittleEndian.PutUint16(out[i*2:], uint16(v))
	}
	return out
}

func marshalAudioChunk(segID uint64, sampleRate int, pcm []byte) ([]byte, error) {
	return json.Marshal(map[string]any{
		"type":        "audio_chunk",
		"segment_id":  segID,
		"format":      audioWireFormat,
		"sample_rate": sampleRate,
		"channels":    audioWireChannels,
		"data":        base64.StdEncoding.EncodeToString(pcm),
	})
}

func marshalAudioEnd(segID uint64, reason string) ([]byte, error) {
	return json.Marshal(map[string]any{
		"type":       "audio_end",
		"segment_id": segID,
		"reason":     reason,
	})
}
