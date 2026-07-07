//go:build !integration

package cmd

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/brain"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/session"
	"github.com/lancekrogers/samantha/internal/stt"
	"github.com/lancekrogers/samantha/internal/tts"
)

var (
	voiceLocale string
	voiceGender string
)

var testCmd = &cobra.Command{
	Use:   "test",
	Short: "Test microphone and speaker",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		fmt.Printf("\n  %s\n", titleStyle.Render("Samantha Audio Test"))
		fmt.Printf("  %s\n\n", dimStyle.Render(fmt.Sprintf("TTS: %s | STT: %s", cfg.TTSProvider, cfg.STTProvider)))

		// TTS test
		fmt.Printf("  %s %s\n", sectionStyle.Render("1."), "Testing speaker (TTS)...")
		speakerErr := runSpeakerTest(cmd.Context(), cfg)
		if speakerErr != nil {
			fmt.Printf("  %s %v\n\n", failStyle.Render("FAIL:"), speakerErr)
		} else {
			fmt.Printf("  %s Played speaker test clip\n\n", okStyle.Render("PASS:"))
		}

		// STT test placeholder
		fmt.Printf("  %s %s\n", sectionStyle.Render("2."), "Testing microphone (STT)...")
		fmt.Println(dimStyle.Render("  (mic test requires full pipeline — use 'samantha' to test)"))
		fmt.Println()
		if speakerErr != nil {
			fmt.Println(failStyle.Render("  Test failed."))
			fmt.Println()
			return fmt.Errorf("speaker (TTS) test failed: %w", speakerErr)
		}
		fmt.Println(okStyle.Render("  Test complete."))
		fmt.Println()
		return nil
	},
}

func runSpeakerTest(ctx context.Context, cfg *config.Config) error {
	if err := config.EnsureRuntimeAssets(ctx, cfg, config.AssetRequest{NeedTTS: true}, nil); err != nil {
		return err
	}

	ttsProvider, cleanup, err := tts.NewProvider(cfg)
	if err != nil {
		return err
	}
	if cleanup != nil {
		defer cleanup()
	}

	player := audio.NewPlayer()
	defer func() { _ = player.Close() }()

	stream, err := ttsProvider.Synthesize(ctx, "Hello! I'm Samantha. Your speaker is working.")
	if err != nil {
		return err
	}

	playback, err := player.PlayStream(ctx, stream)
	if err != nil {
		return err
	}

	result := <-playback.Done()
	if result.Err != nil && !result.Interrupted {
		return result.Err
	}
	return nil
}

var voicesCmd = &cobra.Command{
	Use:   "voices",
	Short: "List available TTS voices",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		fmt.Printf("\n  %s %s\n\n", titleStyle.Render("Voices for:"), sectionStyle.Render(cfg.TTSProvider))

		if err := config.EnsureRuntimeAssets(cmd.Context(), cfg, config.AssetRequest{NeedTTS: true}, nil); err != nil {
			return err
		}

		ttsProvider, cleanup, err := tts.NewProvider(cfg)
		if err != nil {
			return fmt.Errorf("init TTS: %w", err)
		}
		if cleanup != nil {
			defer cleanup()
		}

		voices := ttsProvider.ListVoices(voiceLocale, voiceGender)
		if len(voices) == 0 {
			fmt.Println(dimStyle.Render("  No voices found."))
			return nil
		}

		for _, v := range voices {
			active := ""
			if v.Name == cfg.TTSVoice {
				active = " " + activeStyle.Render("●")
			}
			fmt.Printf("  %s %s  %s\n", keyStyle.Render(fmt.Sprintf("%-16s", v.Name)), v.FriendlyName, dimStyle.Render(fmt.Sprintf("%s / %s", v.Gender, v.Locale))+active)
		}
		fmt.Printf("\n  %s\n\n", dimStyle.Render(fmt.Sprintf("%d voices found.", len(voices))))
		return nil
	},
}

var providersCmd = &cobra.Command{
	Use:   "providers",
	Short: "Show available TTS and STT providers",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		fmt.Println()
		fmt.Printf("  %s\n", titleStyle.Render("Providers"))
		fmt.Printf("  %s\n", sectionStyle.Render("Brain:"))
		brainActive := strings.TrimSpace(cfg.BrainProvider)
		if brainActive == "" {
			brainActive = "claude"
		}
		for _, spec := range brain.Providers() {
			printProvider(brainActive == spec.Name, spec.Name, spec.Description)
		}
		if !brainImplemented(brainActive) {
			fmt.Printf("    %s %s\n", dimStyle.Render("[config]"), dimStyle.Render(brainActive+" — configured but not implemented in this build"))
		}

		fmt.Println()
		fmt.Printf("  %s\n", sectionStyle.Render("TTS (text-to-speech):"))
		ttsActive := cfg.TTSProvider
		if ttsActive == "" {
			ttsActive = "kokoro"
		}
		for _, spec := range tts.Providers() {
			printProvider(ttsActive == spec.Name, spec.Name, spec.Description)
		}
		if !ttsImplemented(ttsActive) {
			fmt.Printf("    %s %s\n", dimStyle.Render("[config]"), dimStyle.Render(ttsActive+" — configured but not implemented in this build"))
		}

		fmt.Println()
		fmt.Printf("  %s\n", sectionStyle.Render("STT (speech-to-text):"))
		sttActive := cfg.STTProvider
		if sttActive == "" {
			sttActive = "sherpa"
		}
		for _, spec := range stt.Providers() {
			printProvider(sttActive == spec.Name, spec.Name, spec.Description)
			if detail := sttSpecDetail(spec.Name); detail != "" {
				fmt.Printf("      %s\n", dimStyle.Render(detail))
			}
		}
		if !sttImplemented(sttActive) {
			fmt.Printf("    %s %s\n", dimStyle.Render("[config]"), dimStyle.Render(sttActive+" — configured but not implemented in this build"))
		}
		fmt.Println()
		return nil
	},
}

var resumeCmd = &cobra.Command{
	Use:   "resume [session-id]",
	Short: "Resume a past conversation",
	Long:  "Pick a past conversation to resume. Shows recent sessions if no ID given.",
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		if len(args) == 1 {
			// Resume specific session.
			sess, err := session.Load(args[0])
			if err != nil {
				return fmt.Errorf("load session: %w", err)
			}
			return startPipeline(cfg, sess)
		}

		// List sessions and let user pick.
		sessions := session.List()
		if len(sessions) == 0 {
			fmt.Println(dimStyle.Render("  No saved sessions."))
			return nil
		}

		fmt.Println()
		fmt.Printf("  %s\n\n", titleStyle.Render("Recent sessions"))
		max := 10
		if len(sessions) < max {
			max = len(sessions)
		}
		for i, s := range sessions[:max] {
			turns := len(s.Turns)
			age := fmtAge(s.UpdatedAt)
			meta := dimStyle.Render(fmt.Sprintf("%d turns, %s ago", turns, age))
			fmt.Printf("  %s %s %s %s\n",
				dimStyle.Render(fmt.Sprintf("%2d.", i+1)),
				sectionStyle.Render(s.ID),
				s.Summary,
				dimStyle.Render("— ")+meta)
		}
		fmt.Println()
		fmt.Printf("  %s\n\n", dimStyle.Render("Usage: samantha resume <session-id>"))
		return nil
	},
}

var continueCmd = &cobra.Command{
	Use:     "continue",
	Short:   "Continue the most recent conversation",
	Aliases: []string{"c"},
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		sess := session.Latest()
		if sess == nil {
			fmt.Println(dimStyle.Render("  No saved sessions to continue."))
			return nil
		}

		return startPipeline(cfg, sess)
	},
}

func fmtAge(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// printProvider renders one provider row with a green marker when it is the
// active provider.
func printProvider(active bool, name, desc string) {
	marker := "  "
	style := keyStyle
	if active {
		marker = activeStyle.Render("●") + " "
		style = activeStyle
	}
	fmt.Printf("    %s%s %s\n", marker, style.Render(name), dimStyle.Render("— "+desc))
}

func brainImplemented(name string) bool {
	for _, spec := range brain.Providers() {
		if spec.Name == name {
			return true
		}
	}
	return false
}

// sttSpecDetail renders one STT provider's capability line from the spec side
// table. It returns "" when the name has no spec, so the row prints unchanged.
func sttSpecDetail(name string) string {
	norm, ok := config.NormalizeSTT(name)
	if !ok {
		return ""
	}
	spec, ok := stt.SpecForNormalized(norm)
	if !ok {
		return ""
	}

	parts := []string{spec.Provider + "/" + spec.Mode}
	if spec.EmitsPartial {
		parts = append(parts, "partials")
	}
	if spec.UsesEndpoint {
		parts = append(parts, "self-endpoint")
	}
	if spec.SupportsEOF {
		parts = append(parts, "eof")
	}
	if spec.RequiresVAD {
		parts = append(parts, "requires vad")
	}
	if spec.RecommendedUse != "" {
		parts = append(parts, spec.RecommendedUse)
	}
	return strings.Join(parts, " · ")
}

func sttImplemented(name string) bool {
	for _, spec := range stt.Providers() {
		if spec.Name == name {
			return true
		}
	}
	return false
}

func ttsImplemented(name string) bool {
	for _, spec := range tts.Providers() {
		if spec.Name == name {
			return true
		}
	}
	return false
}

func init() {
	voicesCmd.Flags().StringVarP(&voiceLocale, "locale", "l", "", "Filter by locale (e.g. en-US)")
	voicesCmd.Flags().StringVarP(&voiceGender, "gender", "g", "", "Filter by gender (male/female)")

	rootCmd.AddCommand(testCmd)
	rootCmd.AddCommand(voicesCmd)
	rootCmd.AddCommand(providersCmd)
	rootCmd.AddCommand(resumeCmd)
	rootCmd.AddCommand(continueCmd)
	rootCmd.AddCommand(newRenderCmd(runRenderText))
}
