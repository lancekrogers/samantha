// Package voiceagent assembles samantha's voice pipeline so it can be embedded.
//
// The CLI is one consumer of this package, not its owner. Everything a host
// application needs — brain, speech-to-text, text-to-speech, capture, playback —
// is constructed here from a config, and every piece can be replaced through
// Options.
//
// There is no API stability promise yet. That is deliberate: the surface is
// being learned by using it, and the first two users are samantha's own
// interactive and serve paths.
package voiceagent

import (
	"context"
	"fmt"

	"github.com/lancekrogers/samantha/pkg/voiceagent/audio"
	"github.com/lancekrogers/samantha/pkg/voiceagent/brain"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/events"
	"github.com/lancekrogers/samantha/pkg/voiceagent/pipeline"
	"github.com/lancekrogers/samantha/pkg/voiceagent/stt"
	"github.com/lancekrogers/samantha/pkg/voiceagent/tts"
)

// Agent is a constructed voice pipeline.
//
// The embedded *pipeline.Pipeline is exposed on purpose. Hosts still reach for
// fields this constructor does not cover — speaker gating, turn callbacks, the
// compact hook — and hiding the pipeline behind a partial facade would mean
// inventing a pass-through method for each one before knowing which are actually
// part of the library's surface. Narrowing comes after the surface is known.
type Agent struct {
	*pipeline.Pipeline
}

// Options configures New. Every provider field is optional: nil means "build it
// from Config", which is what the CLI does.
//
// Injecting a provider is how a host supplies its own implementation, and how
// integration tests supply fakes without a build tag excluding the wiring they
// are meant to exercise.
type Options struct {
	// Config drives every default. Required.
	Config *config.Config

	// Events is the bus the pipeline publishes to. Required — a pipeline with
	// no bus is silent in the UI sense, which is never what a caller wants.
	Events *events.Bus

	// Brain, STT, TTS, TTSFallback and Player each replace the provider that
	// Config would otherwise select.
	Brain       brain.Provider
	STT         stt.Provider
	TTS         tts.Provider
	TTSFallback tts.Provider
	Player      audio.Engine

	// VAD replaces the config-built voice detector. It is typed concretely
	// because pipeline's voiceDetector interface is still unexported — an
	// embedder cannot name it today. Exporting the front-end interfaces is
	// sequence 008.4's job; until then this field is honest about what it
	// accepts rather than pretending to be pluggable.
	VAD *audio.VAD

	// TextOnly skips capture, VAD and STT: the agent reads text turns instead of
	// listening. Silent skips TTS and playback: it thinks and writes but does not
	// speak. Both are false for a full voice agent.
	TextOnly bool
	Silent   bool

	// Logf reports non-fatal setup problems — a configured TTS fallback that
	// could not be loaded, for instance. Nil discards them. The CLI passes a
	// function that writes to stderr; a host may prefer its own logger.
	//
	// Prompts and Env are not here yet. They are meaningless while config
	// resolution is global; they arrive in stage 2 (sequence 008.3) alongside
	// de-globalizing config, so that injecting them actually changes behaviour
	// rather than being quietly ignored.
	Logf func(format string, args ...any)
}

// New builds a voice agent and returns it with a cleanup function.
//
// **The cleanup must be called, and its ordering is load-bearing.** Resources
// are released in reverse order of acquisition: the audio device closes after
// the models that feed it, the capture stops before the VAD it writes into.
// Every error path below runs cleanup before returning, so a partially built
// agent never leaks a device or a loaded model.
func New(ctx context.Context, opts Options) (*Agent, func(), error) {
	if opts.Config == nil {
		return nil, nil, fmt.Errorf("voiceagent: Config is required")
	}
	if opts.Events == nil {
		return nil, nil, fmt.Errorf("voiceagent: Events is required")
	}
	cfg := opts.Config
	logf := opts.Logf
	if logf == nil {
		logf = func(string, ...any) {}
	}

	var cleanups []func()
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}

	p := &pipeline.Pipeline{
		Events:            opts.Events,
		VoiceToolsEnabled: cfg.VoiceToolsEnabled,
	}

	// Brain — injected, or selected by config.
	if opts.Brain != nil {
		p.Brain = opts.Brain
	} else {
		b, err := brain.NewProvider(cfg)
		if err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("init brain: %w", err)
		}
		p.Brain = b
	}

	// TTS + Player (skipped in silent mode).
	if !opts.Silent {
		if opts.Player != nil {
			p.Player = opts.Player
		} else {
			player := audio.NewPlayerWithDevice(cfg.OutputDevice)
			cleanups = append(cleanups, func() { _ = player.Close() })
			p.Player = player
		}

		switch {
		case opts.TTS != nil:
			p.ReplaceTTS(opts.TTS, opts.TTSFallback)
		default:
			ttsSet, err := NewTTSSet(cfg)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("init TTS: %w", err)
			}
			cleanups = append(cleanups, ttsSet.Close)
			p.ReplaceTTS(ttsSet.Primary, ttsSet.Fallback)
			if ttsSet.FallbackWarning != nil {
				logf("warning: %v", ttsSet.FallbackWarning)
			}
		}
	}

	// Capture + VAD + STT (skipped in text mode).
	if !opts.TextOnly {
		var frontend *audio.VoiceFrontend
		if cfg.VoiceFrontendEnabled {
			frontend = audio.NewVoiceFrontend()
			cleanups = append(cleanups, func() { _ = frontend.Close() })
		}

		capture := audio.NewCaptureWithDevice(cfg.InputDevice)
		if frontend != nil {
			capture.SetFrontend(frontend)
		}
		if err := capture.Start(ctx); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("start capture: %w", err)
		}
		cleanups = append(cleanups, capture.Stop)
		p.Capture = capture

		if !opts.Silent && frontend != nil {
			if player, ok := p.Player.(*audio.Player); ok {
				player.SetFrontend(frontend)
			}
		}

		var vad *audio.VAD
		switch {
		case opts.VAD != nil:
			// Caller owns the lifetime of an injected detector, so it is not
			// added to cleanups — deleting a VAD the host still holds would be
			// a use-after-free waiting to happen.
			vad = opts.VAD
			p.VAD = vad
		case cfg.VADEnabled:
			var err error
			vad, err = audio.NewVAD(cfg)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("init VAD: %w", err)
			}
			cleanups = append(cleanups, vad.Delete)
			p.VAD = vad
		}
		if vad != nil {

			// Barge-in stays nil (watchBargeIn no-ops) unless explicitly enabled.
			if !opts.Silent && cfg.BargeInEnabled {
				bargeInVAD, err := audio.NewBargeInVAD(cfg)
				if err != nil {
					cleanup()
					return nil, nil, fmt.Errorf("init barge-in VAD: %w", err)
				}
				cleanups = append(cleanups, bargeInVAD.Delete)
				p.BargeInVAD = bargeInVAD
			}
		}

		if opts.STT != nil {
			p.STT = opts.STT
		} else {
			sttProvider, sttCleanup, err := stt.NewProvider(cfg, capture, vad)
			if err != nil {
				cleanup()
				return nil, nil, fmt.Errorf("init STT: %w", err)
			}
			if sttCleanup != nil {
				cleanups = append(cleanups, sttCleanup)
			}
			p.STT = sttProvider
		}
	} else if opts.STT != nil {
		// A caller that injects an STT provider in text mode means it: the
		// provider reads from somewhere other than a local microphone, which is
		// exactly what serve does with a network ingress.
		p.STT = opts.STT
	}

	return &Agent{Pipeline: p}, cleanup, nil
}
