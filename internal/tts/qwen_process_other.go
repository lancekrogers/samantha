//go:build !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd && !solaris

package tts

import (
	"os/exec"
	"time"
)

const qwenProcessWaitDelay = 2 * time.Second

// configureQwenCommand keeps the provider portable on platforms without the
// Unix process-group APIs. The context still bounds the direct worker process.
func configureQwenCommand(cmd *exec.Cmd) {
	cmd.WaitDelay = qwenProcessWaitDelay
}
