package cmd

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// forceQuitTimeout caps how long graceful shutdown may run after the first
// interrupt before the process force-exits.
const forceQuitTimeout = 3 * time.Second

// signalContext returns a context cancelled on the first SIGINT/SIGTERM. A
// second signal, or forceQuitTimeout elapsing, force-exits — so unlike
// signal.NotifyContext, a blocked shutdown can never leave the process ignoring
// interrupts.
func signalContext() (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(context.Background())

	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)

	go func() {
		select {
		case <-sigCh:
			cancel()
		case <-ctx.Done(): // normal completion
			signal.Stop(sigCh)
			return
		}

		select {
		case <-sigCh: // second signal
		case <-time.After(forceQuitTimeout): // shutdown wedged
		}
		os.Exit(130)
	}()

	return ctx, cancel
}
