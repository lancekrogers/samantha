package cmd

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/samantha/internal/config"
	"github.com/Obedience-Corp/samantha/internal/session"
	"github.com/Obedience-Corp/samantha/internal/tts"
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

		fmt.Printf("\n  Samantha Audio Test\n")
		fmt.Printf("  TTS: %s | STT: %s\n\n", cfg.TTSProvider, cfg.STTProvider)

		// TTS test
		fmt.Println("  1. Testing speaker (TTS)...")
		kokoroTTS, err := tts.NewKokoroTTS(cfg)
		if err != nil {
			fmt.Printf("  FAIL: %v\n\n", err)
		} else {
			samples, sr, err := kokoroTTS.Generate("Hello! I'm Samantha. Your speaker is working.")
			if err != nil {
				fmt.Printf("  FAIL: %v\n\n", err)
			} else {
				fmt.Printf("  PASS: Generated %d samples at %dHz\n\n", len(samples), sr)
				_ = samples // TODO: play audio when player is wired
			}
		}

		// STT test placeholder
		fmt.Println("  2. Testing microphone (STT)...")
		fmt.Println("  (mic test requires full pipeline — use 'samantha' to test)")
		fmt.Println()
		fmt.Println("  Test complete.")
		fmt.Println()
		return nil
	},
}

var voicesCmd = &cobra.Command{
	Use:   "voices",
	Short: "List available TTS voices",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		fmt.Printf("\n  Voices for: %s\n\n", cfg.TTSProvider)

		kokoroTTS, err := tts.NewKokoroTTS(cfg)
		if err != nil {
			return fmt.Errorf("init TTS: %w", err)
		}
		defer kokoroTTS.Delete()

		voices := kokoroTTS.ListVoices(voiceLocale, voiceGender)
		if len(voices) == 0 {
			fmt.Println("  No voices found.")
			return nil
		}

		for _, v := range voices {
			fmt.Printf("  %-16s %s  %s / %s\n", v.Name, v.FriendlyName, v.Gender, v.Locale)
		}
		fmt.Printf("\n  %d voices found.\n\n", len(voices))
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
		fmt.Println("  Providers")
		fmt.Println("  TTS (text-to-speech):")
		ttsActive := cfg.TTSProvider
		fmt.Printf("    [%s] kokoro — Local, high quality, no API key\n", activeTag(ttsActive == "kokoro"))
		fmt.Printf("    [%s] edge — Free cloud, no API key\n", activeTag(ttsActive == "edge"))
		fmt.Printf("    [%s] fish — Paid cloud, custom voice clones\n", activeTag(ttsActive == "fish"))

		fmt.Println()
		fmt.Println("  STT (speech-to-text):")
		sttActive := cfg.STTProvider
		fmt.Printf("    [%s] sherpa — Local whisper, no internet\n", activeTag(sttActive == "sherpa"))
		fmt.Printf("    [%s] google — Free cloud\n", activeTag(sttActive == "google"))
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
			fmt.Println("  No saved sessions.")
			return nil
		}

		fmt.Println()
		fmt.Println("  Recent sessions:")
		fmt.Println()
		max := 10
		if len(sessions) < max {
			max = len(sessions)
		}
		for i, s := range sessions[:max] {
			turns := len(s.Turns)
			age := fmtAge(s.UpdatedAt)
			fmt.Printf("  %2d. [%s] %s — %d turns, %s ago\n", i+1, s.ID, s.Summary, turns, age)
		}
		fmt.Println()
		fmt.Println("  Usage: samantha resume <session-id>")
		fmt.Println()
		return nil
	},
}

var continueCmd = &cobra.Command{
	Use:   "continue",
	Short: "Continue the most recent conversation",
	Aliases: []string{"c"},
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}

		sess := session.Latest()
		if sess == nil {
			fmt.Println("  No saved sessions to continue.")
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

func activeTag(active bool) string {
	if active {
		return "active"
	}
	return "      "
}

func init() {
	voicesCmd.Flags().StringVarP(&voiceLocale, "locale", "l", "", "Filter by locale (e.g. en-US)")
	voicesCmd.Flags().StringVarP(&voiceGender, "gender", "g", "", "Filter by gender (male/female)")

	rootCmd.AddCommand(testCmd)
	rootCmd.AddCommand(voicesCmd)
	rootCmd.AddCommand(providersCmd)
	rootCmd.AddCommand(resumeCmd)
	rootCmd.AddCommand(continueCmd)
}
