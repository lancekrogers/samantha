//go:build integration

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/config"
)

var rootCmd = &cobra.Command{
	Use:   "samantha",
	Short: "Give Claude a voice",
	Long:  "Samantha — ultra-low-latency voice assistant for AI coding, inspired by Her.",
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		_, err := config.Load()
		return err
	},
	RunE: func(cmd *cobra.Command, args []string) error {
		return fmt.Errorf("voice runtime is not available in the integration test binary")
	},
}

func init() {
	rootCmd.Flags().BoolP("text", "t", false, "Text-only input mode (no microphone)")
	rootCmd.Flags().BoolP("no-voice", "n", false, "Disable TTS output")
	rootCmd.Flags().Bool("no-tui", false, "Skip TUI launcher, start directly")
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}
