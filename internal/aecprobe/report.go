package aecprobe

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
)

// quietEchoRMS is the level below which a run cannot judge the canceller. The
// front-end's AGC targets 0.08 RMS and the noise suppressor gates at its own
// floor, so an echo much quieter than this is removed by the suppressor
// regardless of what the AEC does. Chosen from the first smoke run, where a
// 0.008-RMS echo scored +21.75dB and meant nothing.
const quietEchoRMS = 0.02

// Report is one probe run. It is written as metrics.json next to the WAVs so a
// run is self-describing: someone reading it a month later can tell which
// device, which rates, and whether the numbers are trustworthy.
type Report struct {
	CreatedAt string `json:"created_at"`
	Label     string `json:"label"`

	OutputDevice string `json:"output_device"`
	InputDevice  string `json:"input_device"`
	DeviceRate   int    `json:"playback_device_rate"`
	CaptureRate  int    `json:"capture_rate"`

	// EstimatedDelaySamples is what referenceDelaySamples published.
	// MeasuredDelaySamples is what the chirp actually showed. The difference is
	// the residual the tap window has to absorb, and the reason this probe
	// exists.
	EstimatedDelaySamples int     `json:"estimated_delay_samples"`
	EstimatedDelayMS      float64 `json:"estimated_delay_ms"`
	DelayPublished        bool    `json:"delay_published"`
	MeasuredDelaySamples  int     `json:"measured_delay_samples"`
	MeasuredDelayMS       float64 `json:"measured_delay_ms"`
	DelayConfidence       float64 `json:"delay_confidence"`
	ResidualSamples       int     `json:"residual_samples"`
	ResidualMS            float64 `json:"residual_ms"`
	TapWindowSamples      int     `json:"tap_window_samples"`
	TapWindowMS           float64 `json:"tap_window_ms"`
	ResidualFitsTaps      bool    `json:"residual_fits_tap_window"`

	ERLEdB          float64 `json:"erle_db"`
	EchoRMS         float64 `json:"echo_rms"`
	ResidualRMS     float64 `json:"residual_rms"`
	MicPeak         float64 `json:"mic_peak"`
	Clipped         bool    `json:"clipped"`
	SilentRun       bool    `json:"silent_run"`
	ReferencePushes int     `json:"reference_pushes"`
	CaptureChunks   int     `json:"capture_chunks"`

	// Warnings are conditions that make the numbers above untrustworthy. A run
	// with warnings should be re-run, not recorded as a result.
	Warnings []string `json:"warnings,omitempty"`
}

// Interpret fills the derived fields and warnings from the raw measurements, so
// the operator does not have to know what counts as a bad run.
func (r *Report) Interpret() {
	r.EstimatedDelayMS = samplesToMS(r.EstimatedDelaySamples)
	r.MeasuredDelayMS = samplesToMS(r.MeasuredDelaySamples)
	r.TapWindowMS = samplesToMS(r.TapWindowSamples)

	r.ResidualSamples = r.MeasuredDelaySamples - r.EstimatedDelaySamples
	r.ResidualMS = samplesToMS(r.ResidualSamples)
	r.ResidualFitsTaps = r.ResidualSamples >= 0 && r.ResidualSamples < r.TapWindowSamples

	if !r.DelayPublished {
		r.Warnings = append(r.Warnings,
			"player never published a reference delay: this run measured an uncompensated front-end")
	}
	if r.ReferencePushes == 0 {
		r.Warnings = append(r.Warnings,
			"no playback reference reached the canceller: check voice_frontend_enabled")
	}
	if r.CaptureChunks == 0 {
		r.Warnings = append(r.Warnings, "no microphone audio captured")
	}
	if r.SilentRun {
		r.Warnings = append(r.Warnings,
			"microphone heard almost nothing: raise output volume or move the mic closer, the ERLE number is meaningless")
	}
	// Any echo this quiet makes the ERLE number meaningless, whatever it says:
	// the noise suppressor gates near its floor, so mic-out collapses whether
	// or not the canceller did anything.
	//
	// This was originally gated on ERLE > 15, on the theory that only an
	// implausibly good score needed challenging. The first real hardware runs
	// disproved that — two runs at ~0.010 RMS returned +8.69dB and +5.45dB,
	// plausible-looking numbers that passed silently while differing from each
	// other by 3dB. Believability is not evidence; the level is.
	if !r.SilentRun && r.EchoRMS < quietEchoRMS {
		r.Warnings = append(r.Warnings, fmt.Sprintf(
			"echo RMS is only %.4f: below %.2f the noise suppressor gates near-silence and the "+
				"%+.1fdB ERLE reflects that, not echo cancellation. Raise the output volume and re-run",
			r.EchoRMS, quietEchoRMS, r.ERLEdB))
	}
	if r.Clipped {
		r.Warnings = append(r.Warnings,
			"microphone clipped: the echo path is nonlinear here, so a linear canceller cannot be judged by this run")
	}
	if r.DelayConfidence < 0.3 {
		r.Warnings = append(r.Warnings,
			"delay correlation is weak: the measured delay is not trustworthy")
	}
	if r.ResidualSamples < 0 {
		r.Warnings = append(r.Warnings,
			"measured delay is SHORTER than the estimate: referenceDelaySamples is over-estimating, which taps cannot absorb")
	}
	if r.ResidualSamples >= r.TapWindowSamples {
		r.Warnings = append(r.Warnings,
			"residual delay exceeds the tap window: cancellation cannot work on this device without more taps")
	}
}

func samplesToMS(samples int) float64 {
	return math.Round(float64(samples)*1000/float64(Rate)*100) / 100
}

// Write persists the report and the three recorded streams into dir.
func (r *Report) Write(dir string, reference, micIn, micOut []float32) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create probe run directory: %w", err)
	}

	encoded, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "metrics.json"), append(encoded, '\n'), 0o600); err != nil {
		return fmt.Errorf("write metrics: %w", err)
	}

	for name, samples := range map[string][]float32{
		"reference.wav": reference,
		"mic-in.wav":    micIn,
		"mic-out.wav":   micOut,
	} {
		if err := WriteWAV(filepath.Join(dir, name), samples, Rate); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return nil
}

// WriteWAV writes mono float32 samples as 16-bit PCM. Keeping the recordings as
// WAV rather than a private format is deliberate: the point of a hardware run
// is that someone can open mic-in.wav and mic-out.wav in any editor and hear
// whether the echo is gone.
func WriteWAV(path string, samples []float32, rate int) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()

	dataBytes := len(samples) * 2
	header := make([]byte, 44)
	copy(header[0:], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(36+dataBytes))
	copy(header[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1) // PCM
	binary.LittleEndian.PutUint16(header[22:], 1) // mono
	binary.LittleEndian.PutUint32(header[24:], uint32(rate))
	binary.LittleEndian.PutUint32(header[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(header[32:], 2)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(dataBytes))
	if _, err := f.Write(header); err != nil {
		return err
	}

	buf := make([]byte, dataBytes)
	for i, s := range samples {
		v := math.Round(float64(s) * math.MaxInt16)
		if v > math.MaxInt16 {
			v = math.MaxInt16
		}
		if v < math.MinInt16 {
			v = math.MinInt16
		}
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(int16(v)))
	}
	_, err = f.Write(buf)
	return err
}

// SilentEchoRMS is the level below which the microphone heard essentially
// nothing. Exported so the probe command and the report agree on what counts as
// a dead run.
const SilentEchoRMS = 0.005
