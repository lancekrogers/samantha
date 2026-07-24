//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package brain

import "os/exec"

// configureCommandCancellation keeps os/exec's default single-process
// cancellation on platforms without POSIX process groups.
func configureCommandCancellation(*exec.Cmd) {}
