// Command embed is a minimal host for the samantha voice agent.
//
// It exists to prove the library is importable from outside the repository, so
// it lives in its own module with a replace directive pointing at a local
// checkout. Compiling in-tree would prove nothing: only a separate module
// catches an accidental dependency on an internal package.
//
//	go run .
package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/lancekrogers/samantha/pkg/voiceagent"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/events"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// TextOnly and Silent keep this example off the microphone and speakers, so
	// it runs anywhere. Drop them for a full voice agent.
	agent, cleanup, err := voiceagent.New(context.Background(), voiceagent.Options{
		Config:   cfg,
		Events:   events.NewBus(),
		TextOnly: true,
		Silent:   true,
		Logf:     func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	})
	if err != nil {
		return fmt.Errorf("new agent: %w", err)
	}
	defer cleanup()

	stream := agent.EventStream(64)
	defer stream.Close()
	go func() {
		for e := range stream.C() {
			fmt.Printf("event: %T\n", e)
		}
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	if err := agent.SendText(ctx, "Say hello in three words."); err != nil {
		return fmt.Errorf("send: %w", err)
	}

	// Interrupt is safe with nothing in flight, which is what a host wired to a
	// hotkey needs.
	agent.Interrupt()

	if dropped := stream.Dropped(); dropped > 0 {
		fmt.Printf("note: dropped %d events (consumer slower than the pipeline)\n", dropped)
	}
	return nil
}
