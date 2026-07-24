//go:build !integration

package cmd

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/meeting"
	"github.com/lancekrogers/samantha/internal/speaker"
)

// newMeetingAnalyzeCmd runs offline speaker diarization on a WAV file.
// Useful for verifying the pipeline with real multi-voice meeting audio.
func newMeetingAnalyzeCmd() *cobra.Command {
	var (
		numSpeakers int
		outPath     string
		jsonOut     bool
	)
	cmd := &cobra.Command{
		Use:   "analyze [audio.wav]",
		Short: "Run offline speaker diarization on a meeting audio file",
		Long: `Analyze a mono WAV recording (preferably 16 kHz) with the meeting
speaker pipeline and write a speaker-analysis JSON timeline.

Example (YouTube multi-voice fixture, shared cache):
  just fetch-meeting-fixture
  samantha meeting analyze ~/.cache/festival-voice/fixtures/meetings/product-marketing-meeting-90s.wav

Notes:
  - Uses Samantha's managed pyannote + NeMo TitaNet speaker models.
  - Speaker labels are anonymous clusters (speaker-1, speaker-2, ...).`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			wavPath := args[0]
			samples, rate, err := audio.ReadWAVFloat32(wavPath)
			if err != nil {
				return fmt.Errorf("read wav: %w", err)
			}
			if rate != audio.SampleRate {
				return fmt.Errorf("speaker diarization requires %d Hz mono audio; got %d Hz", audio.SampleRate, rate)
			}
			if len(samples) == 0 {
				return fmt.Errorf("empty audio: %s", wavPath)
			}

			loaded, err := config.Load()
			if err != nil {
				return err
			}
			cfg := *loaded
			cfg.Speaker.Enabled = true
			cfg.Speaker.Meeting.Enabled = true
			if numSpeakers > 0 {
				cfg.Speaker.Meeting.NumSpeakers = numSpeakers
			}
			sp := speaker.FromAppConfig(&cfg)
			sp.Enabled = true
			sp.Meeting.Enabled = true
			sp = sp.Normalize()

			if err := config.EnsureRuntimeAssets(cmd.Context(), &cfg, config.AssetRequest{NeedSpeaker: true}, meetingAssetProgress(jsonOut)); err != nil {
				return err
			}
			engine, err := speaker.NewSherpaEngine(sp, config.ModelsDirFrom(&cfg))
			if err != nil {
				return err
			}
			analyzer, err := speaker.NewAnalyzer(sp, engine)
			if err != nil {
				_ = engine.Close()
				return err
			}
			defer func() { _ = analyzer.Close() }()

			result := meeting.AnalyzeRecording(cmd.Context(), analyzer, samples)

			if outPath == "" {
				base := strings.TrimSuffix(filepath.Base(wavPath), filepath.Ext(wavPath))
				outPath = filepath.Join(filepath.Dir(wavPath), base+"-speaker-analysis.json")
			}
			result.Artifact = outPath
			result.SpeakerCount = countTimelineSpeakers(result.Timeline)
			if err := meeting.WriteAnalysis(outPath, result); err != nil {
				return err
			}

			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			}

			fmt.Fprintf(cmd.OutOrStdout(), "Speaker analysis: %s\n", result.Status)
			if result.Error != "" {
				fmt.Fprintf(cmd.OutOrStdout(), "  error: %s\n", result.Error)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "  audio:   %s (%.1fs @ %d Hz)\n",
				wavPath, float64(len(samples))/float64(rate), rate)
			fmt.Fprintf(cmd.OutOrStdout(), "  output:  %s\n", outPath)
			fmt.Fprintf(cmd.OutOrStdout(), "  spans:   %d\n", len(result.Timeline.Observations))
			for _, obs := range result.Timeline.Observations {
				fmt.Fprintf(cmd.OutOrStdout(), "    %s  %dms–%dms  conf=%.2f  %s\n",
					obs.Label, obs.StartMS, obs.EndMS, obs.Confidence, obs.State)
			}
			if result.Status != meeting.AnalysisComplete {
				return fmt.Errorf("analysis status %s", result.Status)
			}
			return nil
		},
	}
	cmd.Flags().IntVar(&numSpeakers, "speakers", 0, "Hint number of speakers (0 = engine default)")
	cmd.Flags().StringVar(&outPath, "out", "", "Write analysis JSON here (default: <audio>-speaker-analysis.json)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print analysis JSON to stdout")
	return cmd
}

func countTimelineSpeakers(timeline speaker.Timeline) int {
	labels := make(map[string]struct{})
	for _, observation := range timeline.Observations {
		if observation.Label != "" && observation.Label != speaker.LabelUnknown {
			labels[observation.Label] = struct{}{}
		}
	}
	return len(labels)
}
