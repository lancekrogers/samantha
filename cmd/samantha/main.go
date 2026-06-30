package main

import (
	"os"

	"github.com/lancekrogers/samantha/cmd/samantha/cmd"
)

func main() {
	// fang renders styled errors itself, so just exit non-zero.
	if err := cmd.Execute(); err != nil {
		os.Exit(1)
	}
}
