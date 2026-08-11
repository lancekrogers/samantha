//go:build integration

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

// newSpeakerCmd preserves the production command tree in the CGO-free
// integration binary. Enrollment embeds audio through sherpa's native
// runtime, which that binary deliberately excludes.
func newSpeakerCmd() *cobra.Command {
	return &cobra.Command{
		Use:           "speaker",
		Short:         "Manage named speaker profiles (enroll, list, remove)",
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(*cobra.Command, []string) error {
			return fmt.Errorf("speaker enrollment is unavailable in the CGO-free integration build")
		},
	}
}
