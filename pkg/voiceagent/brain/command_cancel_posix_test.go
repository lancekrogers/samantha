//go:build linux || darwin

package brain

import (
	"bufio"
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestConfigureCommandCancellationKillsProcessGroup(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", `sleep 30 & child=$!; printf '%s\n' "$child"; wait`)
	configureCommandCancellation(cmd)

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start command: %v", err)
	}

	childLine, err := bufio.NewReader(stdout).ReadString('\n')
	if err != nil {
		t.Fatalf("read child PID: %v", err)
	}
	childPID, err := strconv.Atoi(strings.TrimSpace(childLine))
	if err != nil {
		t.Fatalf("parse child PID %q: %v", childLine, err)
	}
	t.Cleanup(func() {
		if childPID > 0 {
			_ = syscall.Kill(childPID, syscall.SIGKILL)
		}
	})

	cancel()
	if err := cmd.Wait(); err == nil {
		t.Fatal("cancelled command exited successfully, want signal error")
	}

	deadline := time.Now().Add(2 * time.Second)
	for {
		err := syscall.Kill(childPID, 0)
		if errors.Is(err, syscall.ESRCH) {
			childPID = 0
			return
		}
		if err != nil {
			t.Fatalf("probe child process %d: %v", childPID, err)
		}
		if time.Now().After(deadline) {
			t.Fatalf("child process %d survived command cancellation", childPID)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
