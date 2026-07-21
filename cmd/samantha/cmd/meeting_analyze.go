package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
	"time"

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

Example (YouTube multi-voice fixture):
  just fetch-meeting-fixture
  samantha meeting analyze tests/fixtures/meetings/product-marketing-meeting-90s.wav

Notes:
  - Requires speaker analysis enabled (or passes --speakers N).
  - Until a native sherpa diarization engine is wired, this uses the
    deterministic FakeEngine split so the pipeline and JSON layout can be
    verified end-to-end against real multi-voice PCM.`,
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			wavPath := args[0]
			samples, rate, err := audio.ReadWAVFloat32(wavPath)
			if err != nil {
				return fmt.Errorf("read wav: %w", err)
			}
			if rate != 16000 {
				fmt.Fprintf(cmd.ErrOrStderr(), "warning: sample rate %d Hz (pipeline assumes 16 kHz mono)\n", rate)
			}
			if len(samples) == 0 {
				return fmt.Errorf("empty audio: %s", wavPath)
			}

			cfg, err := config.Load()
			if err != nil {
				return err
			}
			sp := speaker.FromAppConfig(cfg)
			sp.Enabled = true
			sp.Meeting.Enabled = true
			if numSpeakers > 0 {
				sp.Meeting.NumSpeakers = numSpeakers
			}
			if sp.Meeting.NumSpeakers <= 0 {
				sp.Meeting.NumSpeakers = 2
			}
			sp = sp.Normalize()

			// FakeEngine is the only Engine implementation today; real sherpa
			// diarization will replace it when models are wired.
			engine := &speaker.FakeEngine{}
			analyzer, err := speaker.NewAnalyzer(sp, engine)
			if err != nil {
				return err
			}
			defer func() { _ = analyzer.Close() }()

			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
			defer cancel()
			result := meeting.AnalyzeRecording(ctx, analyzer, samples)

			if outPath == "" {
				base := strings.TrimSuffix(filepath.Base(wavPath), filepath.Ext(wavPath))
				outPath = filepath.Join(filepath.Dir(wavPath), base+"-speaker-analysis.json")
			}
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
