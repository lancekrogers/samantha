//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package brain

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
)

// configureCommandCancellation places the shell and its descendants in a new
// process group so a context timeout stops the whole tool command, not just the
// parent shell.
func configureCommandCancellation(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return os.ErrProcessDone
		}
		// SIGKILL is deliberate: tool timeouts are hard sandbox bounds, and a
		// SIGTERM grace period would let uncooperative descendants exceed them.
		err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return os.ErrProcessDone
		}
		return err
	}
}
