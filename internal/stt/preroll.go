package stt

import "github.com/lancekrogers/samantha/internal/audio"

// preRollSamplesFromMS converts a pre-roll window in milliseconds to a sample
// count at the capture rate. A non-positive duration disables pre-roll.
func preRollSamplesFromMS(ms int) int {
	if ms <= 0 {
		return 0
	}
	return ms * audio.SampleRate / 1000
}

// vadDetector is the subset of *audio.VAD the pre-roll segmenter drives. Keeping
// it an interface lets the splice logic be tested without the cgo Silero VAD.
type vadDetector interface {
	AcceptWaveform(samples []float32)
	IsSpeech() bool
	IsSpeechDetected() bool
	IsEmpty() bool
	FrontSegment() (samples []float32, start int)
	Pop()
	Clear()
	Flush()
}

var _ vadDetector = (*audio.VAD)(nil)

// vadSegmenter adapts the cgo Silero *audio.VAD to the segmenter seam so the
// offline loop can run against either the real VAD or a deterministic fake.
//
// It also recovers the utterance onset the VAD trims: Silero only marks a
// segment once ~MinSpeechDuration of speech has accrued, so the first word can
// fall before the segment's start boundary. vadSegmenter keeps a rolling window
// of the most recent pre-roll samples fed to the detector and prepends the audio
// immediately preceding each segment's start, aligned via the segment offset.
type vadSegmenter struct {
	vad     vadDetector
	preRoll int // samples of onset audio to prepend; 0 disables

	window     []float32 // most recent up-to-preRoll samples fed to the VAD
	windowBase int       // absolute index (samples since Reset) of window[0]
	fed        int       // total samples fed since Reset
}

// newVADSegmenter builds a segmenter that prepends up to preRollSamples of
// pre-trigger audio to each recognized segment.
func newVADSegmenter(vad vadDetector, preRollSamples int) *vadSegmenter {
	return &vadSegmenter{vad: vad, preRoll: preRollSamples}
}

// maxPreRollWindow caps the retained onset buffer at 30s so a pathologically
// long single utterance cannot grow it without bound. The onset (first word)
// sits at the front of an utterance, so trimming the oldest audio only forfeits
// pre-roll for segments that began more than 30s ago — by which point the
// segment carries its own audio and pre-roll no longer matters.
const maxPreRollWindow = 30 * audio.SampleRate

func (s *vadSegmenter) AcceptWaveform(samples []float32) {
	s.vad.AcceptWaveform(samples)
	s.fed += len(samples)
	if s.preRoll <= 0 || len(samples) == 0 {
		return
	}

	// Retain audio from the utterance onset (since Reset), not a trailing
	// window: the pre-trigger region precedes the segment start and would be
	// evicted by a short trailing window before the segment is ever emitted.
	s.window = append(s.window, samples...)
	if len(s.window) > maxPreRollWindow {
		drop := len(s.window) - maxPreRollWindow
		// Re-slice onto a fresh backing array so the retained window does not
		// pin the trimmed history in memory.
		s.window = append([]float32(nil), s.window[drop:]...)
	}
	s.windowBase = s.fed - len(s.window)
}

func (s *vadSegmenter) IsSpeech() bool    { return s.vad.IsSpeech() }
func (s *vadSegmenter) HasSegments() bool { return s.vad.IsSpeechDetected() }

// NextSegment pops the next finalized segment and, when possible, prepends the
// pre-trigger audio the VAD trimmed from its onset. The prepend is defensive:
// if the segment's start offset falls outside the retained window (unexpected,
// or after a Reset the detector's offset basis did not share), it returns the
// segment unchanged rather than splicing misaligned audio.
func (s *vadSegmenter) NextSegment() ([]float32, bool) {
	if s.vad.IsEmpty() {
		return nil, false
	}
	seg, start := s.vad.FrontSegment()
	s.vad.Pop()

	if s.preRoll <= 0 || len(seg) == 0 {
		return seg, true
	}

	lo := max(start-s.preRoll, s.windowBase)
	hi := min(start, s.windowBase+len(s.window))
	if start < s.windowBase || hi <= lo {
		return seg, true
	}

	pre := s.window[lo-s.windowBase : hi-s.windowBase]
	out := make([]float32, 0, len(pre)+len(seg))
	out = append(out, pre...)
	out = append(out, seg...)
	return out, true
}

func (s *vadSegmenter) Reset() {
	s.vad.Clear()
	// The detector restarts its segment-offset counter on Clear, so drop the
	// retained window and reset the sample counter to keep offsets aligned.
	s.window = s.window[:0]
	s.windowBase = 0
	s.fed = 0
}

func (s *vadSegmenter) Flush() { s.vad.Flush() }
