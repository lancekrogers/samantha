//go:build integration

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/config"
)

var providersCmd = &cobra.Command{
	Use:   "providers",
	Short: "Show available TTS and STT providers",
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println()
		fmt.Println("  Providers")
		fmt.Println("  Brain:")
		fmt.Println("    [active] claude — Claude CLI")
		fmt.Println("    [      ] ollama — Local Ollama server")
		fmt.Println()
		fmt.Println("  TTS (text-to-speech):")
		fmt.Println("    [active] kokoro — Local Kokoro TTS")
		fmt.Println()
		fmt.Println("  STT (speech-to-text):")
		fmt.Println("    [active] sherpa — Local sherpa-onnx Whisper (utterance-final)")
		fmt.Println("    [      ] sherpa-streaming — Local sherpa-onnx streaming Zipformer")
		fmt.Println("    [      ] sherpa-offline — Local sherpa-onnx Whisper (legacy alias)")
		fmt.Println("    [      ] whispercpp — Local whisper.cpp CLI")
		fmt.Println()
		return nil
	},
}

func init() {
	rootCmd.AddCommand(providersCmd)
	rootCmd.AddCommand(newRenderCmd(runRenderPlan))
	rootCmd.AddCommand(newAudiobookCmd(runRenderPlan, config.Load))
	rootCmd.AddCommand(newNarrateCmd())
	rootCmd.AddCommand(newMeetingCmd())
}
