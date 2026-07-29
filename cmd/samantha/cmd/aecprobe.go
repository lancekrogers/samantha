//go:build !integration

package cmd

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/aecprobe"
	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
)

var (
	aecProbeOut       string
	aecProbeLabel     string
	aecProbeRate      int
	aecProbeChirpSecs float64
	aecProbeEchoSecs  float64
)

// newAECProbeCmd builds the hardware echo-cancellation probe.
//
// This exists because internal/audio's ERLE tests score the canceller against a
// synthetic echo — one delayed linear copy in a silent room — which can prove
// the reference is aligned but cannot prove the filter works. Rooms reverberate,
// speakers distort, and the two device clocks drift. Those only show up against
// real hardware, so this plays a known signal through the real player and scores
// what the microphone actually hears.
func newAECProbeCmd(loadConfig func() (*config.Config, error)) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "aec-probe",
		Short: "Measure echo cancellation against real speakers and microphone",
		Long: `Play a known signal through the configured output device, record what the
microphone hears, and report how much of it the echo canceller removed.

Run this once per device class — built-in speakers, Bluetooth, USB mic,
external display — with the speaker audible and nobody talking. Each run writes
WAVs and a metrics.json you can compare across devices.

Warnings in metrics.json mean the run is not trustworthy; fix the condition and
re-run rather than recording the number.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := loadConfig()
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			return runAECProbe(cmd.Context(), cmd.OutOrStdout(), cfg)
		},
	}

	cmd.Flags().StringVar(&aecProbeOut, "out", "", "Run directory (default: <debug-audio dir or cwd>/aec-probe/<timestamp>)")
	cmd.Flags().StringVar(&aecProbeLabel, "label", "", "Device label recorded in metrics.json (e.g. 'macbook-builtin', 'airpods')")
	cmd.Flags().IntVar(&aecProbeRate, "rate", 24000, "Stimulus sample rate; 24000 matches the TTS rate the player prefers")
	cmd.Flags().Float64Var(&aecProbeChirpSecs, "chirp-seconds", 2, "Chirp duration used to measure true speaker-to-mic delay")
	cmd.Flags().Float64Var(&aecProbeEchoSecs, "echo-seconds", 12, "Speech-like playback duration used to score ERLE")

	return cmd
}

func init() {
	rootCmd.AddCommand(newAECProbeCmd(config.Load))
}

func runAECProbe(ctx context.Context, out interface{ Write([]byte) (int, error) }, cfg *config.Config) error {
	if ctx == nil {
		ctx = context.Background()
	}

	// The probe measures the front-end, so it must exist regardless of the
	// user's config: voice_frontend_enabled defaults to false and would
	// otherwise make every run report "no reference reached the canceller".
	frontend := audio.NewVoiceFrontend()
	recorder := aecprobe.NewRecorder(frontend)
	defer func() { _ = recorder.Close() }()

	player := audio.NewPlayerWithDevice(cfg.OutputDevice)
	defer func() { _ = player.Close() }()
	player.SetFrontend(recorder)

	capture := audio.NewCaptureWithDevice(cfg.InputDevice)
	capture.SetFrontend(recorder)
	if err := capture.Start(ctx); err != nil {
		return fmt.Errorf("start capture: %w", err)
	}
	defer capture.Stop()

	fmt.Fprintf(out, "Output device: %s\nInput device:  %s\n\n",
		deviceLabel(cfg.OutputDevice), deviceLabel(cfg.InputDevice))

	// Phase 1 — delay. A swept chirp has a sharp autocorrelation peak, which is
	// what makes the speaker-to-mic delay measurable at all.
	fmt.Fprintln(out, "[1/2] Measuring speaker-to-mic delay (chirp). Stay quiet…")
	chirp := aecprobe.Chirp(aecProbeChirpSecs, aecProbeRate)
	if err := playThrough(ctx, player, chirp, aecProbeRate); err != nil {
		return fmt.Errorf("play chirp: %w", err)
	}
	settle(ctx, 500*time.Millisecond)

	// Let the reference queue drain so the ERLE phase starts from the same
	// burst-reprime state a real utterance would.
	settle(ctx, 700*time.Millisecond)

	// Phase 2 — cancellation.
	fmt.Fprintln(out, "[2/2] Measuring echo cancellation (speech-like). Stay quiet…")
	echo := aecprobe.SpeechLikeNoise(aecProbeEchoSecs, aecProbeRate, 1)
	echoStart := recorder.Mark()
	if err := playThrough(ctx, player, echo, aecProbeRate); err != nil {
		return fmt.Errorf("play echo stimulus: %w", err)
	}
	settle(ctx, 500*time.Millisecond)
	echoEnd := recorder.Mark()

	reference, micIn, micOut := recorder.Streams()
	delaySamples, delayPublished := recorder.PublishedDelay()
	refPushes, capChunks := recorder.Counts()

	// Measure reference-pushed to echo-heard, which is the lag the canceller
	// has to span. Correlating the *generated* chirp against the microphone
	// instead would also count device initialisation and stream setup between
	// the mark and the first audible sample — that inflated the first hardware
	// runs to ~153-165ms and produced 12ms of scatter across identical runs.
	//
	// The recorded reference is what PushPlaybackReference received, already at
	// the capture clock, and refAnchor places it in microphone coordinates.
	effectiveDelay, calibrated, _ := recorder.EffectiveDelay()
	anchor, anchored := recorder.ReferenceAnchor()
	var measured int
	var confidence float64
	if anchored {
		// Half a second of search covers anything short of a badly buffered
		// Bluetooth path, and bounds the correlation cost.
		measured, confidence = aecprobe.MeasureDelay(
			reference, aecprobe.Slice(micIn, anchor, len(micIn)), aecprobe.Rate/2)
	}

	echoIn := aecprobe.Slice(micIn, echoStart, echoEnd)
	echoOut := aecprobe.Slice(micOut, echoStart, echoEnd)
	echoRMS := aecprobe.SegmentRMS(echoIn)

	report := &aecprobe.Report{
		CreatedAt:             time.Now().Format(time.RFC3339),
		Label:                 aecProbeLabel,
		OutputDevice:          deviceLabel(cfg.OutputDevice),
		InputDevice:           deviceLabel(cfg.InputDevice),
		DeviceRate:            aecProbeRate,
		CaptureRate:           aecprobe.Rate,
		EstimatedDelaySamples: delaySamples,
		DelayPublished:        delayPublished,
		EffectiveDelaySamples: effectiveDelay,
		DelayCalibrated:       calibrated,
		MeasuredDelaySamples:  measured,
		DelayConfidence:       confidence,
		TapWindowSamples:      audio.EchoCancellerTaps(),
		ERLEdB:                aecprobe.ERLE(echoIn, echoOut),
		EchoRMS:               echoRMS,
		ResidualRMS:           aecprobe.SegmentRMS(echoOut),
		MicPeak:               aecprobe.PeakLevel(echoIn),
		ReferencePushes:       refPushes,
		CaptureChunks:         capChunks,
		// 0.99 rather than 1.0: samples reaching full scale mean the ADC was
		// already limiting, which distorts the echo path before the canceller
		// ever sees it.
		Clipped: aecprobe.PeakLevel(echoIn) >= 0.99,
		// Below this the microphone effectively heard nothing, and an ERLE
		// computed from it describes room noise rather than cancellation.
		SilentRun: echoRMS < aecprobe.SilentEchoRMS,
	}
	report.Interpret()

	dir, err := probeRunDir()
	if err != nil {
		return err
	}
	if err := report.Write(dir, reference, micIn, micOut); err != nil {
		return err
	}

	printProbeSummary(out, report, dir)
	return nil
}

// playThrough pushes samples into the real playback path and waits for the
// audio to finish. Writes run on their own goroutine because PCMStream's frame
// channel is small and PlayStream is what drains it.
func playThrough(ctx context.Context, player *audio.Player, samples []float32, rate int) error {
	stream := audio.NewPCMStream(ctx)
	if err := stream.SetSampleRate(rate); err != nil {
		return err
	}

	go func() {
		defer stream.Close()
		const chunk = 1024
		for start := 0; start < len(samples); start += chunk {
			end := min(start+chunk, len(samples))
			if err := stream.Write(samples[start:end]); err != nil {
				return
			}
		}
	}()

	playback, err := player.PlayStream(ctx, stream)
	if err != nil {
		return err
	}
	select {
	case result := <-playback.Done():
		return result.Err
	case <-ctx.Done():
		return ctx.Err()
	}
}

// settle waits for audio still in flight — the device ring, the ingress worker,
// and the room — to land in the recording before a phase boundary is taken.
func settle(ctx context.Context, d time.Duration) {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
	case <-ctx.Done():
	}
}

func probeRunDir() (string, error) {
	if aecProbeOut != "" {
		return aecProbeOut, nil
	}
	root := audio.DebugAudioDir()
	if root == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		root = cwd
	}
	name := time.Now().Format("20060102T150405")
	if aecProbeLabel != "" {
		name = aecProbeLabel + "-" + name
	}
	return filepath.Join(root, "aec-probe", name), nil
}

func deviceLabel(name string) string {
	if name == "" {
		return "(system default)"
	}
	return name
}

func printProbeSummary(out interface{ Write([]byte) (int, error) }, r *aecprobe.Report, dir string) {
	fmt.Fprintf(out, "\n─── AEC probe: %s ───\n", labelOrDefault(r.Label))
	fmt.Fprintf(out, "  delay estimated by player : %d samples (%.2f ms)\n", r.EstimatedDelaySamples, r.EstimatedDelayMS)
	source := "player estimate, calibration did not fire"
	if r.DelayCalibrated {
		source = "measured by runtime calibration"
	}
	fmt.Fprintf(out, "  delay actually in use     : %d samples (%.2f ms, %s)\n",
		r.EffectiveDelaySamples, r.EffectiveDelayMS, source)
	fmt.Fprintf(out, "  delay measured from chirp : %d samples (%.2f ms, confidence %.2f)\n",
		r.MeasuredDelaySamples, r.MeasuredDelayMS, r.DelayConfidence)
	fmt.Fprintf(out, "  residual for the filter   : %d samples (%.2f ms) against a %.2f ms tap window\n",
		r.ResidualSamples, r.ResidualMS, r.TapWindowMS)
	fmt.Fprintf(out, "  echo cancellation (ERLE)  : %+.2f dB\n", r.ERLEdB)
	fmt.Fprintf(out, "  mic level                 : rms %.4f, peak %.3f\n", r.EchoRMS, r.MicPeak)

	if len(r.Warnings) > 0 {
		fmt.Fprintf(out, "\n  ⚠ this run is not trustworthy:\n")
		for _, w := range r.Warnings {
			fmt.Fprintf(out, "    - %s\n", w)
		}
	} else if !r.ResidualFitsTaps {
		fmt.Fprintf(out, "\n  ⚠ residual delay does not fit the tap window on this device.\n")
	}

	fmt.Fprintf(out, "\n  run: %s\n", dir)
	fmt.Fprintf(out, "  listen to mic-in.wav and mic-out.wav to hear what the canceller removed.\n")
}

func labelOrDefault(label string) string {
	if label == "" {
		return "unlabelled run"
	}
	return label
}
