package aecprobe

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A hardware session is expensive to repeat, so the report has to tell the
// operator when a run is worthless. Silent failures are the failure mode that
// costs a whole afternoon.
func TestInterpretFlagsUntrustworthyRuns(t *testing.T) {
	tests := []struct {
		name   string
		report Report
		want   string
	}{
		{
			name:   "delay never published",
			report: Report{DelayPublished: false, ReferencePushes: 10, CaptureChunks: 10, TapWindowSamples: 768},
			want:   "uncompensated",
		},
		{
			name:   "no reference reached the canceller",
			report: Report{DelayPublished: true, ReferencePushes: 0, CaptureChunks: 10, TapWindowSamples: 768},
			want:   "voice_frontend_enabled",
		},
		{
			name:   "microphone heard nothing",
			report: Report{DelayPublished: true, ReferencePushes: 10, CaptureChunks: 10, SilentRun: true, TapWindowSamples: 768},
			want:   "meaningless",
		},
		{
			name:   "microphone clipped",
			report: Report{DelayPublished: true, ReferencePushes: 10, CaptureChunks: 10, Clipped: true, TapWindowSamples: 768},
			want:   "nonlinear",
		},
		{
			// Found by the first smoke run: a quiet mic makes the noise
			// suppressor gate everything and ERLE reads superb.
			name: "high ERLE off a near-silent echo",
			report: Report{
				DelayPublished: true, ReferencePushes: 10, CaptureChunks: 10,
				DelayConfidence: 0.9, TapWindowSamples: 768,
				ERLEdB: 21.75, EchoRMS: 0.008,
			},
			want: "echo RMS is only",
		},
		{
			// Found by the first real hardware runs, and the reason this check
			// is no longer gated on an implausible score: two runs at ~0.010
			// RMS returned +8.69dB and +5.45dB — believable numbers that passed
			// silently while disagreeing with each other by 3dB.
			name: "plausible ERLE off a too-quiet echo",
			report: Report{
				DelayPublished: true, ReferencePushes: 10, CaptureChunks: 10,
				DelayConfidence: 0.9, TapWindowSamples: 768,
				ERLEdB: 8.69, EchoRMS: 0.0100,
			},
			want: "Raise the output volume",
		},
		{
			name: "estimate exceeds the measured delay",
			report: Report{
				DelayPublished: true, ReferencePushes: 10, CaptureChunks: 10,
				EstimatedDelaySamples: 900, MeasuredDelaySamples: 400,
				DelayConfidence: 0.9, TapWindowSamples: 768,
			},
			want: "over-estimating",
		},
		{
			name: "residual beyond the tap window",
			report: Report{
				DelayPublished: true, ReferencePushes: 10, CaptureChunks: 10,
				EstimatedDelaySamples: 341, MeasuredDelaySamples: 4000,
				DelayConfidence: 0.9, TapWindowSamples: 768,
			},
			want: "exceeds the tap window",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			r := tc.report
			r.Interpret()
			joined := strings.Join(r.Warnings, " | ")
			if !strings.Contains(joined, tc.want) {
				t.Errorf("warnings %q do not mention %q", joined, tc.want)
			}
		})
	}
}

// The good case must stay quiet, or operators learn to ignore warnings.
func TestInterpretIsSilentOnAHealthyRun(t *testing.T) {
	r := Report{
		DelayPublished: true, ReferencePushes: 400, CaptureChunks: 900,
		EstimatedDelaySamples: 341, MeasuredDelaySamples: 640,
		DelayConfidence: 0.85, TapWindowSamples: 768,
		ERLEdB: 8.4, EchoRMS: 0.05, MicPeak: 0.4,
	}
	// 0.05 RMS is comfortably above quietEchoRMS; if that ever stops being
	// true the "healthy" case below is not testing what it claims.
	r.Interpret()

	if len(r.Warnings) != 0 {
		t.Fatalf("healthy run produced warnings: %v", r.Warnings)
	}
	if !r.ResidualFitsTaps {
		t.Error("residual 299 should fit a 768-sample tap window")
	}
	if r.ResidualSamples != 299 {
		t.Errorf("residual = %d, want 640-341=299", r.ResidualSamples)
	}
}

func TestWriteProducesReadableRun(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "run")
	r := &Report{Label: "test-device", TapWindowSamples: 768, DelayPublished: true,
		ReferencePushes: 1, CaptureChunks: 1}
	r.Interpret()

	samples := SpeechLikeNoise(0.05, Rate, 1)
	if err := r.Write(dir, samples, samples, samples); err != nil {
		t.Fatalf("Write() error: %v", err)
	}

	for _, name := range []string{"metrics.json", "reference.wav", "mic-in.wav", "mic-out.wav"} {
		info, err := os.Stat(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("%s missing: %v", name, err)
		}
		if info.Size() == 0 {
			t.Errorf("%s is empty", name)
		}
	}

	raw, err := os.ReadFile(filepath.Join(dir, "metrics.json"))
	if err != nil {
		t.Fatal(err)
	}
	var round Report
	if err := json.Unmarshal(raw, &round); err != nil {
		t.Fatalf("metrics.json is not valid JSON: %v", err)
	}
	if round.Label != "test-device" {
		t.Errorf("round-tripped label = %q, want test-device", round.Label)
	}
}

// The WAVs exist so a human can listen to what the canceller removed; a wrong
// header makes them unopenable and the recording useless.
func TestWriteWAVHeaderIsValid(t *testing.T) {
	path := filepath.Join(t.TempDir(), "x.wav")
	samples := SpeechLikeNoise(0.1, Rate, 1)
	if err := WriteWAV(path, samples, Rate); err != nil {
		t.Fatal(err)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if want := 44 + len(samples)*2; len(raw) != want {
		t.Fatalf("file is %d bytes, want %d (44-byte header + 16-bit mono)", len(raw), want)
	}
	if string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		t.Fatalf("bad RIFF/WAVE magic: %q %q", raw[0:4], raw[8:12])
	}
	if string(raw[36:40]) != "data" {
		t.Fatalf("bad data chunk id: %q", raw[36:40])
	}
}
