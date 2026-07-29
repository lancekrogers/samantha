package aecprobe

import (
	"testing"

	"github.com/lancekrogers/samantha/internal/audio"
)

// The decorator sits between the player and the real front-end on the live
// audio path. If it is not transparent, the probe measures itself rather than
// the shipping configuration — and the run would look fine while being wrong.
func TestRecorderIsTransparentToTheFrontend(t *testing.T) {
	inner := audio.NewVoiceFrontend()
	rec := NewRecorder(inner)

	// Frontend, and the optional delay interface the player type-asserts.
	var _ audio.Frontend = rec
	delayer, ok := any(rec).(audio.ReferenceDelayer)
	if !ok {
		t.Fatal("Recorder must implement audio.ReferenceDelayer, or wrapping " +
			"silently disables delay compensation and the probe measures the old bug")
	}

	// The forwarded delay must reach the wrapped front-end, not stop here.
	delayer.SetReferenceDelay(341)
	got, published := rec.PublishedDelay()
	if !published || got != 341 {
		t.Fatalf("PublishedDelay() = (%d, %v), want (341, true)", got, published)
	}

	ref := SpeechLikeNoise(0.05, Rate, 1)
	rec.PushPlaybackReference(ref)

	mic := SpeechLikeNoise(0.05, Rate, 2)
	out := rec.ProcessCapture(append([]float32(nil), mic...))
	if len(out) != len(mic) {
		t.Fatalf("ProcessCapture returned %d samples for %d in", len(out), len(mic))
	}

	// The wrapped front-end must have actually run: a pass-through decorator
	// that forgot to call inner would return the input unchanged.
	same := true
	for i := range out {
		if out[i] != mic[i] {
			same = false
			break
		}
	}
	if same {
		t.Error("ProcessCapture output is identical to its input; the wrapped front-end did not run")
	}
}

// Scoring needs the raw microphone, not the processed one. If ProcessCapture
// mutates its input in place and the recorder kept a reference instead of a
// copy, mic-in.wav would silently become a second copy of mic-out.wav and every
// ERLE would read 0 dB.
func TestRecorderKeepsRawMicSeparateFromProcessed(t *testing.T) {
	rec := NewRecorder(&mutatingFrontend{})

	mic := []float32{0.5, 0.5, 0.5, 0.5}
	rec.ProcessCapture(mic)

	_, micIn, micOut := rec.Streams()
	if len(micIn) != 4 || len(micOut) != 4 {
		t.Fatalf("recorded %d in / %d out, want 4 each", len(micIn), len(micOut))
	}
	for i := range micIn {
		if micIn[i] != 0.5 {
			t.Fatalf("micIn[%d] = %v, want the pre-processing value 0.5", i, micIn[i])
		}
		if micOut[i] != 0 {
			t.Fatalf("micOut[%d] = %v, want the post-processing value 0", i, micOut[i])
		}
	}
}

// The anchor is what lets the delay be measured as reference-pushed to
// echo-heard. Without it the only available measurement also counts device
// initialisation and stream setup, which is what inflated the first hardware
// runs to ~153-165ms with 12ms of scatter across identical runs.
func TestRecorderAnchorsReferenceInMicrophoneCoordinates(t *testing.T) {
	rec := NewRecorder(&mutatingFrontend{})

	if _, ok := rec.ReferenceAnchor(); ok {
		t.Fatal("anchor reported before any reference was pushed")
	}

	// Capture runs before playback starts, as it does in a real session.
	rec.ProcessCapture(make([]float32, 160))
	rec.ProcessCapture(make([]float32, 160))

	rec.PushPlaybackReference(make([]float32, 80))
	anchor, ok := rec.ReferenceAnchor()
	if !ok {
		t.Fatal("anchor not set after the first reference push")
	}
	if anchor != 320 {
		t.Fatalf("anchor = %d, want 320 (mic samples recorded before playback began)", anchor)
	}

	// Later pushes must not move it: the anchor marks where playback started,
	// not where the most recent block arrived.
	rec.ProcessCapture(make([]float32, 160))
	rec.PushPlaybackReference(make([]float32, 80))
	if again, _ := rec.ReferenceAnchor(); again != 320 {
		t.Fatalf("anchor moved to %d on a later push, want it pinned at 320", again)
	}
}

func TestRecorderMarksTrackCaptureProgress(t *testing.T) {
	rec := NewRecorder(&mutatingFrontend{})
	if got := rec.Mark(); got != 0 {
		t.Fatalf("initial Mark() = %d, want 0", got)
	}
	rec.ProcessCapture(make([]float32, 160))
	rec.ProcessCapture(make([]float32, 160))
	if got := rec.Mark(); got != 320 {
		t.Fatalf("Mark() = %d after two 160-sample chunks, want 320", got)
	}
	if _, chunks := rec.Counts(); chunks != 2 {
		t.Fatalf("capture chunks = %d, want 2", chunks)
	}
}

// A front-end that zeroes its input in place, which is the aliasing hazard the
// recorder has to defend against.
type mutatingFrontend struct{}

func (m *mutatingFrontend) ProcessCapture(samples []float32) []float32 {
	for i := range samples {
		samples[i] = 0
	}
	return samples
}
func (m *mutatingFrontend) PushPlaybackReference([]float32) {}
func (m *mutatingFrontend) Close() error                    { return nil }
