package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/speaker"
)

// newSpeakerCmd builds the `samantha speaker` command group: durable, named
// speaker enrollment consumed by the live indicator and meeting diarization.
func newSpeakerCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "speaker",
		Short: "Manage named speaker profiles (enroll, list, remove)",
		Long: `Enroll named speakers from short voice clips so live speaker labels and
meeting diarization can say "Lance" instead of "speaker-1".

Profiles persist under speaker.enrollment_dir (default: <config>/speakers)
and are versioned with the embedding model that produced them.`,
	}
	cmd.AddCommand(newSpeakerEnrollCmd(), newSpeakerListCmd(), newSpeakerRemoveCmd())
	return cmd
}

// speakerEnrollmentDir resolves the profile store location.
func speakerEnrollmentDir(cfg *config.Config) string {
	if cfg != nil && cfg.Speaker.EnrollmentDir != "" {
		return cfg.Speaker.EnrollmentDir
	}
	return filepath.Join(config.ConfigDir(), "speakers")
}

func newSpeakerEnrollCmd() *cobra.Command {
	var (
		name    string
		wavs    []string
		jsonOut bool
	)
	cmd := &cobra.Command{
		Use:   "enroll --name <name> --from-wav <clip.wav> [--from-wav ...]",
		Short: "Enroll a named speaker from one or more 16 kHz mono WAV clips",
		Long: `Embed each clip with the managed speaker model and persist the profile.
More clips (3+ short, distinct utterances) make matching more robust.
Re-enrolling an existing name replaces its profile in place.

Record a clip on macOS (16 kHz mono):
  ffmpeg -f avfoundation -i ":0" -t 8 -ar 16000 -ac 1 clip.wav`,
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if name == "" {
				return fmt.Errorf("--name is required")
			}
			if len(wavs) == 0 {
				return fmt.Errorf("at least one --from-wav clip is required")
			}
			loaded, err := config.Load()
			if err != nil {
				return err
			}
			cfg := *loaded
			cfg.Speaker.Enabled = true
			cfg.Speaker.Live.Enabled = true
			sp := speaker.FromAppConfig(&cfg)
			sp.Enabled = true
			sp.Live.Enabled = true
			sp = sp.Normalize()

			if err := config.EnsureRuntimeAssets(cmd.Context(), &cfg, config.AssetRequest{NeedSpeaker: true}, speakerAssetProgress(cmd, jsonOut)); err != nil {
				return err
			}
			modelsDir := config.ModelsDirFrom(&cfg)
			engine, err := speaker.NewSherpaLiveEngine(sp, modelsDir)
			if err != nil {
				engine, err = speaker.NewSherpaEngine(sp, modelsDir)
			}
			if err != nil {
				return fmt.Errorf("load speaker embedding model: %w", err)
			}
			defer func() { _ = engine.Close() }()

			store, err := speaker.OpenEnrollment(speakerEnrollmentDir(&cfg))
			if err != nil {
				return err
			}
			profile, err := speaker.EnrollFromWAVs(cmd.Context(), engine, store, name, engine.EmbeddingRev(), wavs)
			if err != nil {
				return err
			}

			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(profile)
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Enrolled %q: %d clip(s), model %s\n", profile.Name, profile.Samples, profile.ModelRev)
			fmt.Fprintf(cmd.OutOrStdout(), "  store: %s\n", store.Dir())
			return nil
		},
	}
	cmd.Flags().StringVar(&name, "name", "", "Display name for the speaker (required)")
	cmd.Flags().StringArrayVar(&wavs, "from-wav", nil, "16 kHz mono WAV clip of this speaker (repeatable; ≥0.5s each)")
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print the stored profile as JSON")
	return cmd
}

func newSpeakerListCmd() *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:           "list",
		Short:         "List enrolled speaker profiles",
		Args:          cobra.NoArgs,
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			store, err := speaker.OpenEnrollment(speakerEnrollmentDir(cfg))
			if err != nil {
				return err
			}
			profiles := store.List()
			if jsonOut {
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(profiles)
			}
			if len(profiles) == 0 {
				fmt.Fprintf(cmd.OutOrStdout(), "No speakers enrolled (store: %s)\n", store.Dir())
				fmt.Fprintln(cmd.OutOrStdout(), "Enroll one: samantha speaker enroll --name <name> --from-wav clip.wav")
				return nil
			}
			w := tabwriter.NewWriter(cmd.OutOrStdout(), 0, 4, 2, ' ', 0)
			fmt.Fprintln(w, "NAME\tCLIPS\tMODEL\tUPDATED")
			for _, p := range profiles {
				fmt.Fprintf(w, "%s\t%d\t%s\t%s\n", p.Name, p.Samples, p.ModelRev, p.UpdatedAt.Local().Format("2006-01-02 15:04"))
			}
			return w.Flush()
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Print profiles as JSON")
	return cmd
}

func newSpeakerRemoveCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "remove <name>",
		Short:         "Remove an enrolled speaker and delete its stored embedding",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg, err := config.Load()
			if err != nil {
				return err
			}
			store, err := speaker.OpenEnrollment(speakerEnrollmentDir(cfg))
			if err != nil {
				return err
			}
			if err := store.Remove(args[0]); err != nil {
				if errors.Is(err, speaker.ErrNotEnrolled) {
					return fmt.Errorf("no speaker named %q is enrolled", args[0])
				}
				return err
			}
			fmt.Fprintf(cmd.OutOrStdout(), "Removed %q (embedding deleted from disk)\n", args[0])
			return nil
		},
	}
}

// speakerAssetProgress reports model download progress on first-time enroll;
// silent for --json output.
func speakerAssetProgress(cmd *cobra.Command, jsonOut bool) func(string, float64) {
	if jsonOut {
		return nil
	}
	return func(assetName string, pct float64) {
		fmt.Fprintf(cmd.ErrOrStderr(), "\r  downloading %s: %3.0f%%", assetName, pct*100)
		if pct >= 1 {
			fmt.Fprintln(cmd.ErrOrStderr())
		}
	}
}
