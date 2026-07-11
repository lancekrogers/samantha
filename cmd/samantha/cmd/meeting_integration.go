//go:build integration

package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

func runMeetingRecord(cmd *cobra.Command, opts meetingOptions) error {
	fmt.Fprintf(cmd.OutOrStdout(), "Meeting record %q (integration stub)\n", opts.Description)
	return nil
}
