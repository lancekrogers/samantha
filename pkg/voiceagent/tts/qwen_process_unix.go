//go:build darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package tts

import (
	"os/exec"
	"syscall"
	"time"
)

const qwenProcessWaitDelay = 2 * time.Second

// configureQwenCommand puts the worker in its own process group so cancellation
// also terminates helpers forked by native GPU/runtime stacks.
func configureQwenCommand(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process == nil {
			return nil
		}
		return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	cmd.WaitDelay = qwenProcessWaitDelay
}
