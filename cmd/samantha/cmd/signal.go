package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// forceQuitTimeout bounds how long graceful shutdown may take after the first
// interrupt before the process force-exits. This guarantees the process is
// always killable via SIGINT/SIGTERM, even if a shutdown path blocks.
const forceQuitTimeout = 3 * time.Second

// signalContext returns a context cancelled on the first SIGINT/SIGTERM, with an
// escalation guarantee: a second signal — or forceQuitTimeout elapsing after the
// first — force-exits the process.
//
// This replaces signal.NotifyContext, which disarms Go's default
// terminate-on-signal behaviour but offers no escalation: if any shutdown path
// fails to observe the cancelled context (e.g. a blocking stdin read), the
// process becomes unkillable except via SIGKILL. Here the first signal still
// requests a graceful shutdown, but the process can no longer get stuck
// ignoring interrupts.
func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		select {
		case <-sigCh:
			// First signal: request graceful shutdown.
			cancel()
		case <-ctx.Done():
			// Caller finished (or cancelled) on its own — stop watching.
			signal.Stop(sigCh)
			return
		}

		// Graceful shutdown is underway. If it completes, main returns and the
		// process exits normally before the timeout fires. Otherwise a second
		// signal or the timeout force-exits, so an interrupt is never ignored.
		select {
		case <-sigCh:
		case <-time.After(forceQuitTimeout):
		}
		os.Exit(130)
	}()

	return ctx, cancel
}
