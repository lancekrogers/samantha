//go:build integration

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newMeetingAnalyzeCmd preserves the production command tree in the CGO-free
// integration binary. Offline diarization itself depends on sherpa's native
// runtime and is exercised by the dedicated speakerflow suite instead.
func newMeetingAnalyzeCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "analyze [audio.wav]",
		Short:         "Run offline speaker diarization on a meeting audio file",
		Args:          cobra.ExactArgs(1),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("meeting analyze is unavailable in the CGO-free integration build")
		},
	}
}
