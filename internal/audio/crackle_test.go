package audio

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/gen2brain/malgo"
)

func TestExpandMonoS16LEStereoDuplicates(t *testing.T) {
	// Two mono frames: 0x0100 and 0x0200 as little-endian int16 values 1 and 2.
	mono := []byte{0x01, 0x00, 0x02, 0x00}
	out := make([]byte, 2*2*2) // frames * channels * 2
	expandMonoS16LE(mono, 2, 2, out)
	// L0 R0 L1 R1
	want := []byte{0x01, 0x00, 0x01, 0x00, 0x02, 0x00, 0x02, 0x00}
	for i := range want {
		if out[i] != want[i] {
			t.Fatalf("stereo expand = %v, want %v", out, want)
		}
	}
}

func TestExpandMonoS16LEMultiChannelFrontOnly(t *testing.T) {
	mono := []byte{0x10, 0x00}
	out := make([]byte, 1*8*2)
	expandMonoS16LE(mono, 1, 8, out)
	// Front L/R carry mono; remaining 6 channels silent.
	if out[0] != 0x10 || out[1] != 0x00 || out[2] != 0x10 || out[3] != 0x00 {
		t.Fatalf("front L/R = %v, want mono duplicated", out[:4])
	}
	for i := 4; i < len(out); i++ {
		if out[i] != 0 {
			t.Fatalf("channel tail byte %d = %d, want 0 (silence on non-front buses)", i, out[i])
		}
	}
}

func TestChoosePlaybackChannelsPrefersStereo(t *testing.T) {
	if got := choosePlaybackChannels(8); got != 2 {
		t.Fatalf("choosePlaybackChannels(8) = %d, want 2", got)
	}
	if got := choosePlaybackChannels(2); got != 2 {
		t.Fatalf("choosePlaybackChannels(2) = %d, want 2", got)
	}
	if got := choosePlaybackChannels(1); got != 1 {
		t.Fatalf("choosePlaybackChannels(1) = %d, want 1", got)
	}
}

func TestPickPlaybackFormatPrefersStereo44100(t *testing.T) {
	// Among advertised rates, 44.1 beats 48 after the 48 kHz path regressed.
	rate, ch := pickPlaybackFormat([]malgo.DataFormat{
		{Channels: 8, SampleRate: 44100},
		{Channels: 8, SampleRate: 48000},
	}, 24_000)
	if rate != 44100 {
		t.Fatalf("rate = %d, want 44100", rate)
	}
	if ch != 8 {
		t.Fatalf("channels = %d, want 8 from advertised formats", ch)
	}
	rate, ch = pickPlaybackFormat([]malgo.DataFormat{
		{Channels: 8, SampleRate: 44100},
		{Channels: 2, SampleRate: 44100},
	}, 24_000)
	if rate != 44100 || ch != 2 {
		t.Fatalf("pickPlaybackFormat = %d/%d, want 44100/2", rate, ch)
	}
}

func TestResampleLinearDoesNotOvershoot(t *testing.T) {
	// Cubic overshoot clipped to int16 and crackled; linear must stay within
	// the local sample pair extrema.
	src := []float32{-0.5, 0.9, -0.4, 0.8}
	out := resample(src, 24_000, 48_000)
	lo, hi := src[0], src[0]
	for _, s := range src {
		if s < lo {
			lo = s
		}
		if s > hi {
			hi = s
		}
	}
	for i, s := range out {
		if s < lo-1e-5 || s > hi+1e-5 {
			t.Fatalf("out[%d]=%v outside source range [%v, %v] (overshoot)", i, s, lo, hi)
		}
	}
}

// synthSine builds a continuous mono tone. Richer HF content (higher f) makes
// resampling defects more audible — matching the post-Kokoro-v1 observation.
func synthSine(sampleRate, n int, freq, amp float64) []float32 {
	out := make([]float32, n)
	for i := range out {
		out[i] = float32(amp * math.Sin(2*math.Pi*freq*float64(i)/float64(sampleRate)))
	}
	return out
}

// synthSpeechLike builds a multi-tone signal with speech-band energy so the
// crackle detector exercises envelope-relative impulse classification rather
// than pure sinusoid math.
func synthSpeechLike(sampleRate, n int) []float32 {
	out := make([]float32, n)
	for i := range out {
		t := float64(i) / float64(sampleRate)
		// Fundamental + formant-ish overtones + gentle amplitude envelope.
		env := 0.55 + 0.45*math.Sin(2*math.Pi*3*t)
		s := 0.55*math.Sin(2*math.Pi*220*t) +
			0.30*math.Sin(2*math.Pi*880*t) +
			0.15*math.Sin(2*math.Pi*2200*t)
		out[i] = float32(env * s * 0.85)
	}
	return out
}

func TestAnalyzeFloat32CleanSineHasNoCrackle(t *testing.T) {
	samples := synthSine(24_000, 24_000, 440, 0.5)
	m := AnalyzeFloat32(samples, CrackleThresholds{})
	if m.HasCrackle(CrackleThresholds{}) {
		t.Fatalf("clean sine reported crackle: %+v", m)
	}
	if m.ImpulseCount != 0 {
		t.Fatalf("ImpulseCount = %d, want 0", m.ImpulseCount)
	}
	if m.MidSilenceRuns != 0 {
		t.Fatalf("MidSilenceRuns = %d, want 0", m.MidSilenceRuns)
	}
}

func TestAnalyzeFloat32DetectsInjectedClick(t *testing.T) {
	samples := synthSine(24_000, 4_000, 440, 0.4)
	// Inject a full-scale bipolar impulse — the classic digital click.
	samples[2000] = 1
	samples[2001] = -1
	m := AnalyzeFloat32(samples, CrackleThresholds{})
	if !m.HasCrackle(CrackleThresholds{}) {
		t.Fatalf("injected click not detected: %+v", m)
	}
	if m.ImpulseCount < 1 {
		t.Fatalf("ImpulseCount = %d, want ≥ 1", m.ImpulseCount)
	}
}

func TestAnalyzeFloat32DetectsMidSpeechSilenceHole(t *testing.T) {
	samples := synthSine(24_000, 4_000, 440, 0.5)
	// Zero out ~2 ms in the middle (48 samples at 24 kHz).
	for i := 2000; i < 2048; i++ {
		samples[i] = 0
	}
	m := AnalyzeFloat32(samples, CrackleThresholds{})
	if !m.HasCrackle(CrackleThresholds{}) {
		t.Fatalf("mid-speech silence not detected: %+v", m)
	}
	if m.MidSilenceRuns != 1 {
		t.Fatalf("MidSilenceRuns = %d, want 1", m.MidSilenceRuns)
	}
	if m.MidSilenceFrames < 40 {
		t.Fatalf("MidSilenceFrames = %d, want ≥ 40", m.MidSilenceFrames)
	}
}

func TestAnalyzeFloat32IgnoresLeadingAndTrailingSilence(t *testing.T) {
	tone := synthSine(24_000, 1_000, 440, 0.5)
	samples := make([]float32, 0, 1_000+200)
	samples = append(samples, make([]float32, 100)...)
	samples = append(samples, tone...)
	samples = append(samples, make([]float32, 100)...)
	m := AnalyzeFloat32(samples, CrackleThresholds{})
	if m.MidSilenceRuns != 0 || m.HasCrackle(CrackleThresholds{}) {
		t.Fatalf("leading/trailing silence flagged as crackle: %+v", m)
	}
}

// TestWholeUtteranceResampleIsClean pins invariant #4 from the audio
// corruption runbook: batch-generated PCM is resampled once across the full
// utterance. A continuous speech-like signal must remain free of software
// crackle after both the preferred 24→48 path and the legacy 24→44.1 path.
func TestWholeUtteranceResampleIsClean(t *testing.T) {
	src := synthSpeechLike(24_000, 24_000)
	for _, outRate := range []int{48_000, 44_100} {
		out := resample(src, 24_000, outRate)
		pcm := float32ToPCM16(out)
		m := AnalyzeInt16(pcm, CrackleThresholds{})
		if m.HasCrackle(CrackleThresholds{}) {
			t.Fatalf("whole-utterance resample to %d Hz reported crackle: %+v", outRate, m)
		}
	}
}

// TestChunkBoundaryGlitchIsDetected documents invariant #4 from the audio
// corruption runbook: independently handling synth chunks can introduce
// discontinuities at 2,048-frame joins. The detector must fail that signal so
// a regression that reintroduces boundary glitches cannot slip through as
// "clean."
func TestChunkBoundaryGlitchIsDetected(t *testing.T) {
	const (
		inRate  = 24_000
		outRate = 44_100
		chunk   = 2_048
		chunks  = 6
	)
	src := synthSpeechLike(inRate, chunk*chunks)
	// Whole-utterance resample is the production path and must stay clean.
	good := resampleLinear(src, inRate, outRate)
	if m := AnalyzeFloat32(good, CrackleThresholds{}); m.HasCrackle(CrackleThresholds{}) {
		t.Fatalf("reference whole-utterance resample is unclean: %+v", m)
	}

	// Simulate a broken path that inserts a bipolar click at every chunk
	// boundary after conversion — the audible shape of a reset interpolator
	// or a dropped/duplicated edge sample under rich HF content.
	var bad []float32
	for off := 0; off < len(src); off += chunk {
		end := off + chunk
		if end > len(src) {
			end = len(src)
		}
		piece := resampleLinear(src[off:end], inRate, outRate)
		if len(bad) > 0 && len(piece) > 1 {
			piece[0] = 1
			piece[1] = -1
		}
		bad = append(bad, piece...)
	}
	m := AnalyzeFloat32(bad, CrackleThresholds{})
	if !m.HasCrackle(CrackleThresholds{}) {
		t.Fatalf("chunk-boundary glitches not detected: %+v", m)
	}
	if m.ImpulseCount < chunks-1 {
		t.Fatalf("ImpulseCount = %d, want ≥ %d (one glitch per interior boundary)", m.ImpulseCount, chunks-1)
	}
}

// TestFinalizeSegmentThenCallbackHasNoCrackle is the package-level integration
// of the production path: multi-chunk TTS collection → single finalize
// (resample + PCM16) → device-sized callback drain. The reconstructed callback
// buffer must be free of software crackle and mid-speech silence.
func TestFinalizeSegmentThenCallbackHasNoCrackle(t *testing.T) {
	const (
		inRate     = 24_000
		outRate    = 48_000 // preferred Studio Display path for Kokoro
		chunk      = 2_048
		chunks     = 12
		frameCount = 256 // typical miniaudio callback size
	)
	src := synthSpeechLike(inRate, chunk*chunks)

	// Mimic pumpSegment: collect every synth chunk, then finalize once.
	var collected []float32
	for off := 0; off < len(src); off += chunk {
		end := off + chunk
		if end > len(src) {
			end = len(src)
		}
		// Fresh allocation per chunk, as PCMStream.Write does.
		piece := append([]float32(nil), src[off:end]...)
		collected = append(collected, piece...)
	}

	segment := newPlaybackSegment()
	segment.setReadyFrames(0)
	finalizeSegment(segment, nil, nil, collected, inRate, outRate, nil)

	if !segmentReady(segment) {
		t.Fatal("segment not ready after finalizeSegment")
	}

	pcm, partials, silenceFrames := reconstructCallbackPCM(segment, frameCount)
	// Only the final callback may be partial (short tail). Mid-utterance
	// partials with silence are underruns.
	if silenceFrames > frameCount {
		t.Fatalf("silence frames in callback reconstruction = %d (partials=%d), want ≤ one final tail", silenceFrames, partials)
	}

	m := AnalyzePCM16LE(pcm, CrackleThresholds{})
	if m.HasCrackle(CrackleThresholds{}) {
		t.Fatalf("finalize+callback path reported crackle: metrics=%+v partials=%d silenceFrames=%d", m, partials, silenceFrames)
	}
	if m.MidSilenceRuns != 0 {
		t.Fatalf("MidSilenceRuns = %d, want 0 (mid-speech underrun)", m.MidSilenceRuns)
	}
}

// TestEarlyReadyAllowsMidSpeechUnderrun pins why setReadyFrames(0) exists for
// batch TTS: if the device callback is allowed to consume a partial buffer
// while the producer is still generating, scheduler delay inserts silence
// holes that sound like crackle/chop.
func TestEarlyReadyAllowsMidSpeechUnderrun(t *testing.T) {
	segment := newPlaybackSegment()
	// Streaming-style threshold: become ready after the first chunk.
	segment.setReadyFrames(512)

	const (
		chunk      = 512
		frameCount = 256
		holeFrames = frameCount * 4
	)
	first := synthSine(24_000, chunk, 440, 0.5)
	second := synthSine(24_000, chunk, 440, 0.5)

	// Producer publishes only the first chunk, then stalls.
	segment.append(float32ToPCM16(first))
	if !segmentReady(segment) {
		t.Fatal("segment should be ready after reaching the streaming threshold")
	}

	// Consumer drains everything currently buffered (simulating a fast
	// callback while the synth is stalled).
	out := make([]byte, frameCount*2)
	for {
		written, finished := segment.writeTo(out, frameCount)
		if finished {
			t.Fatal("segment finished before second chunk and finishInput")
		}
		if written == 0 {
			break
		}
	}

	// Underrun: callback would emit silence here.
	for range holeFrames / frameCount {
		clearBytes(out)
		written, _ := segment.writeTo(out, frameCount)
		if written != 0 {
			t.Fatalf("expected underrun silence, got %d frames", written)
		}
	}

	// Producer resumes and finishes; remainder must still be playable.
	segment.append(float32ToPCM16(second))
	segment.finishInput(nil)
	for {
		written, finished := segment.writeTo(out, frameCount)
		if finished {
			break
		}
		if written == 0 {
			t.Fatal("stuck with no frames after finishInput")
		}
	}

	// The audible defect is first-chunk audio, a mid-speech hole, then the
	// second chunk. Score that reconstructed timeline.
	combined := append([]float32(nil), first...)
	combined = append(combined, make([]float32, holeFrames)...)
	combined = append(combined, second...)

	m := AnalyzeFloat32(combined, CrackleThresholds{})
	if !m.HasCrackle(CrackleThresholds{}) {
		t.Fatalf("early-ready underrun path should score as crackle: %+v", m)
	}
	if m.MidSilenceRuns < 1 {
		t.Fatalf("MidSilenceRuns = %d, want ≥ 1 for the simulated underrun hole", m.MidSilenceRuns)
	}
}

// TestPumpSegmentFullBufferPreventsCallbackCrackle drives the real
// pumpSegment + PCMStream path (without opening a hardware device) by using
// finalizeSegment through the same collection pattern pumpSegment uses, and
// asserts the readiness gate holds until the full utterance is buffered.
func TestPumpSegmentFullBufferPreventsCallbackCrackle(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	stream := NewPCMStream(ctx)
	if err := stream.SetSampleRate(24_000); err != nil {
		t.Fatalf("SetSampleRate: %v", err)
	}

	const (
		chunk  = 2_048
		chunks = 10
	)
	src := synthSpeechLike(24_000, chunk*chunks)

	// Produce chunks asynchronously the way Kokoro's stream writer does.
	go func() {
		for off := 0; off < len(src); off += chunk {
			end := off + chunk
			if end > len(src) {
				end = len(src)
			}
			if err := stream.Write(src[off:end]); err != nil {
				stream.CloseWithError(err)
				return
			}
		}
		stream.Close()
	}()

	// Exercise pumpSegment's collection logic without ensureDevice: wait for
	// rate, buffer every frame, finalize once at 24 kHz → 44.1 kHz.
	segment := newPlaybackSegment()
	segment.setReadyFrames(0)

	inputRate, err := stream.WaitReady(ctx)
	if err != nil {
		t.Fatalf("WaitReady: %v", err)
	}
	const outputRate = 48_000
	var samples []float32
	for {
		select {
		case <-ctx.Done():
			t.Fatalf("context done while collecting: %v", ctx.Err())
		case frames, ok := <-stream.Frames():
			if !ok {
				finalizeSegment(segment, nil, nil, samples, inputRate, outputRate, stream.Err())
				goto drained
			}
			// Critical: do not become ready / do not resample per chunk.
			if !segmentReady(segment) {
				// good — still buffering
			} else {
				t.Fatal("segment became ready before stream closed (setReadyFrames(0) violated)")
			}
			samples = append(samples, frames...)
		}
	}
drained:

	if !segmentReady(segment) {
		t.Fatal("segment not ready after full buffer finalize")
	}

	pcm, _, silenceFrames := reconstructCallbackPCM(segment, 256)
	// At most one partial final callback (silenceFrames < one period).
	if silenceFrames > 256 {
		t.Fatalf("unexpected silence in reconstruction: %d frames", silenceFrames)
	}
	m := AnalyzePCM16LE(pcm, CrackleThresholds{})
	if m.HasCrackle(CrackleThresholds{}) {
		t.Fatalf("pump-style full-buffer path reported crackle: %+v", m)
	}
}
