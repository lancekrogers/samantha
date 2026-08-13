// Command embed is a minimal host for the samantha voice agent.
//
// It exists to prove two things that only an outside consumer can prove:
//
//   - The library is importable. This is a separate module with a replace
//     directive, because compiling in-tree would still succeed if pkg/voiceagent
//     secretly leaned on internal/.
//   - The library is self-contained. Run with -hermetic it builds its config in
//     code and injects every provider, so nothing may reach for a home
//     directory, a models cache or a prompts folder.
//
// See README.md for the hermetic gate procedure.
package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/lancekrogers/samantha/pkg/voiceagent"
	"github.com/lancekrogers/samantha/pkg/voiceagent/audio"
	"github.com/lancekrogers/samantha/pkg/voiceagent/brain"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/events"
	"github.com/lancekrogers/samantha/pkg/voiceagent/tts"
)

func main() {
	hermetic := flag.Bool("hermetic", false, "build config in code and inject every provider; no disk, no home directory")
	flag.Parse()
	if err := run(*hermetic); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(hermetic bool) error {
	opts := voiceagent.Options{
		Events:   events.NewBus(),
		TextOnly: true,
		Silent:   true,
		Logf:     func(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) },
	}

	if hermetic {
		// Everything stated in code. If the agent still touches a home
		// directory after this, that is a reach-in the library has to fix.
		opts.Config = &config.Config{AgentName: "Embed"}
		opts.Brain = &echoBrain{}
		opts.TTS = &silentTTS{}
		// Env and PromptsDir stay unset: they only feed a brain New builds from
		// Config, and New rejects them alongside an injected Brain rather than
		// ignore them. With every provider injected, nothing here can reach a
		// home directory — that guarantee is now structural, not stated.
	} else {
		cfg, err := config.Load()
		if err != nil {
			return fmt.Errorf("load config: %w", err)
		}
		opts.Config = cfg
	}

	agent, cleanup, err := voiceagent.New(context.Background(), opts)
	if err != nil {
		return fmt.Errorf("new agent: %w", err)
	}
	defer cleanup()

	stream := agent.EventStream(64)
	defer stream.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
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

	stream.Close()
	<-done
	if dropped := stream.Dropped(); dropped > 0 {
		fmt.Printf("note: dropped %d events (consumer slower than the pipeline)\n", dropped)
	}
	fmt.Println("ok: agent constructed, ran a turn, and shut down cleanly")
	return nil
}

// echoBrain stands in for a model so the hermetic run needs no weights.
type echoBrain struct{ turns []brain.Turn }

func (b *echoBrain) ThinkStream(ctx context.Context, input string, _ brain.StreamOptions) (*brain.Stream, error) {
	chunks := make(chan string, 1)
	done := make(chan brain.StreamResult, 1)
	chunks <- "Hello there friend."
	close(chunks)
	done <- brain.StreamResult{}
	close(done)
	return &brain.Stream{Chunks: chunks, Done: done}, nil
}

func (b *echoBrain) ThinkFull(context.Context, string, brain.StreamOptions) (string, error) {
	return "Hello there friend.", nil
}
func (b *echoBrain) ClearHistory()              { b.turns = nil }
func (b *echoBrain) History() []brain.Turn      { return b.turns }
func (b *echoBrain) LoadHistory(t []brain.Turn) { b.turns = t }

// silentTTS produces no audio; the hermetic run has no speakers.
type silentTTS struct{}

func (silentTTS) Synthesize(ctx context.Context, _ string) (*audio.PCMStream, error) {
	s := audio.NewPCMStream(ctx)
	s.Close()
	return s, nil
}
func (silentTTS) Available() bool                       { return true }
func (silentTTS) ListVoices(string, string) []tts.Voice { return nil }
