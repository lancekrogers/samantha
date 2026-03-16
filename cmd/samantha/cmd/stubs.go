package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/Obedience-Corp/samantha/internal/config"
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
	Args:  cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("  Resume not yet fully implemented")
		return nil
	},
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
}
