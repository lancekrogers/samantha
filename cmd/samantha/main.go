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
	// fang renders styled errors itself, so just exit non-zero.
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
