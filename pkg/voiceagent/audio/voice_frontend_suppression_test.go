package audio

import "testing"

// TestVoiceFrontendOverSuppressesNormalSpeech documents why the voice frontend
// ships disabled by default (see config default voice_frontend_enabled=false).
//
// The noise suppressor's initial noise floor (noiseSuppressorFloor = 0.12) is
// higher than the RMS of normal-volume speech, so it treats an ordinary voice as
// below the noise floor and gates it out. Measured per stage for speech at RMS
// ~0.065, the suppressor alone drops it to ~1% and the AGC only claws it back to
// ~6% — far below what the VAD (threshold 0.6) needs, which is why voice mode sat
// stuck on "listening" until the user spoke loudly enough to clear the gate.
//
// This is a characterization test: it pins the current (bad) behavior so the
// reason for the default is explicit. When the suppressor is re-tuned for normal
// mic levels, invert the assertion to require that normal speech survives.
func TestVoiceFrontendOverSuppressesNormalSpeech(t *testing.T) {
	const frame = ChunkSize
	speech := make([]float32, frame)
	for i := range speech {
		speech[i] = voicedSample(i, 0.12) // normal speaking level
	}
	inRMS := frameRMS(speech)

	out := NewVoiceFrontend().ProcessCapture(append([]float32(nil), speech...))
	survived := frameRMS(out) / inRMS

	// Current behavior: normal speech is crushed to well under 20% of its level.
	// If this starts passing (survived >= 0.2), the suppressor has been fixed —
	// flip the default back on and turn this into a "must survive" assertion.
	if survived >= 0.2 {
		t.Fatalf("normal speech survived at %.2fx — frontend may be fixed; "+
			"revisit voice_frontend_enabled default and this test", survived)
	}
}
