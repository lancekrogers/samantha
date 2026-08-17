package main

import (
	"os"

	// Seed lipgloss dark-background cache before bubbletea OSC queries.
	_ "github.com/lancekrogers/samantha/internal/bginit"
	// Register persona ensure/apply on config.Load.
	_ "github.com/lancekrogers/samantha/internal/persona"

	"github.com/lancekrogers/samantha/cmd/samantha/cmd"
)

func main() {
	// fang renders styled errors itself, so only the exit status is decided
	// here. Commands that distinguish "called wrong" from "operation failed"
	// (the config surface) carry their own status; everything else exits 1.
	if err := cmd.Execute(); err != nil {
		os.Exit(cmd.ExitCode(err))
	}
}
