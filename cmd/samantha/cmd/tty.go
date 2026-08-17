package cmd

import (
	"os"

	"github.com/mattn/go-isatty"
)

// stdinIsTerminal reports whether a human is at the other end of stdin.
// Untagged (unlike root.go) so integration-tagged commands such as `models
// clean` can gate interactive prompts the same way the voice runtime does.
func stdinIsTerminal() bool {
	fd := os.Stdin.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}

// stdoutIsTerminal reports whether a human is watching stdout. Destructive
// commands use it to refuse a confirmation nobody could have read.
func stdoutIsTerminal() bool {
	fd := os.Stdout.Fd()
	return isatty.IsTerminal(fd) || isatty.IsCygwinTerminal(fd)
}
