//go:build !integration

package cmd

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/listen"
	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/internal/meeting/remote"
	"github.com/lancekrogers/samantha/internal/speaker"
	"github.com/lancekrogers/samantha/internal/stt"
)

// This file wires serve's phone-driven meeting capture (PROTOCOL_DELTAS D6) to
// the same STT and speaker stack the desktop recorder uses. The phone owns the
// microphone; serve only ever sees a finished WAV, so everything here runs
// after the recording stops and none of it touches the live pipeline.

// newServeMeetingManager builds the meeting manager serve hands to netapi.
// The heavy model construction happens lazily inside the pipeline, so starting
// serve never waits on STT assets.
func newServeMeetingManager(cfg *config.Config) (*remote.Manager, error) {
	pipelineCfg := *cfg
	return remote.NewManager(remote.Options{
		Root:     config.MeetingsDirFrom(cfg),
		STTLabel: serveMeetingSTTLabel(&pipelineCfg),
		Pipeline: remote.PipelineFunc(func(ctx context.Context, job remote.Job) error {
			return runServeMeetingPipeline(ctx, &pipelineCfg, job)
		}),
	})
}

// serveMeetingSTTLabel names the transcription backend in the bundle header,
// matching what a desktop recording writes.
func serveMeetingSTTLabel(cfg *config.Config) string {
	label := cfg.STTProvider
	if norm, err := config.NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode); err == nil {
		label = fmt.Sprintf("%s (%s)", norm.Provider, norm.Mode)
	}
	return label
}

// runServeMeetingPipeline transcribes the assembled audio, then diarizes it.
// Diarization is best-effort: a transcript without speaker labels is still a
// useful meeting, so its failure is reported without discarding the recording.
func runServeMeetingPipeline(ctx context.Context, cfg *config.Config, job remote.Job) error {
	if err := transcribeMeetingAudio(ctx, cfg, job); err != nil {
		return err
	}
	if _, err := diarizeMeetingAudio(ctx, cfg, job); err != nil {
		return fmt.Errorf("speaker analysis: %w", err)
	}
	return nil
}

// transcribeMeetingAudio replays the bundle's WAV through the configured STT
// provider and appends every utterance to the bundle.
func transcribeMeetingAudio(ctx context.Context, cfg *config.Config, job remote.Job) error {
	if err := config.EnsureRuntimeAssets(ctx, cfg, config.AssetRequest{NeedSTT: true, NeedVAD: true}, nil); err != nil {
		return fmt.Errorf("prepare STT models: %w", err)
	}
	// Every shipped STT backend rejects a nil VAD; a serve config with VAD off
	// must not silently disable meeting transcription.
	sttCfg := *cfg
	sttCfg.VADEnabled = true

	source, err := newReplaySource(job.AudioPath)
	if err != nil {
		return err
	}
	vad, err := audio.NewVAD(&sttCfg)
	if err != nil {
		return fmt.Errorf("init VAD: %w", err)
	}
	defer vad.Delete()

	provider, cleanup, err := stt.NewProvider(&sttCfg, source, vad)
	if err != nil {
		return fmt.Errorf("init STT: %w", err)
	}
	if cleanup != nil {
		defer cleanup()
	}
	return replayTranscribe(ctx, source, provider, job.Writer)
}

// diarizeMeetingAudio runs offline speaker analysis over the same WAV.
func diarizeMeetingAudio(ctx context.Context, cfg *config.Config, job remote.Job) (meeting.AnalysisResult, error) {
	sp := speaker.FromAppConfig(cfg)
	if !sp.MeetingActive() {
		return meeting.AnalysisResult{Status: meeting.AnalysisDisabled}, nil
	}
	if err := config.EnsureRuntimeAssets(ctx, cfg, config.AssetRequest{NeedSpeaker: true}, nil); err != nil {
		return meeting.AnalysisResult{}, fmt.Errorf("prepare speaker models: %w", err)
	}
	engine, err := speaker.NewSherpaEngine(sp, config.ModelsDirFrom(cfg))
	if err != nil {
		return meeting.AnalysisResult{}, err
	}
	analyzer, err := speaker.NewAnalyzer(sp, engine)
	if err != nil {
		_ = engine.Close()
		return meeting.AnalysisResult{}, err
	}
	defer func() { _ = analyzer.Close() }()

	return meeting.AnalyzeBundleAudio(ctx, analyzer, job.Writer, job.BundlePath, job.AudioPath)
}

// replaySource replays a finished meeting's WAV through the STT stack while
// tracking how much audio it has handed over. That position is what lets each
// transcript be stamped with the moment inside the meeting it came from,
// rather than the wall-clock time transcription happened to run.
//
// It deliberately does not expose Reset: rewinding a replay would transcribe
// the same meeting forever.
type replaySource struct {
	fixture *audio.FixtureSource

	mu      sync.Mutex
	samples int64
}

func newReplaySource(path string) (*replaySource, error) {
	fixture, err := audio.NewFixtureSourceFromWAV(path, audio.ChunkSize, false)
	if err != nil {
		return nil, fmt.Errorf("open meeting audio: %w", err)
	}
	return &replaySource{fixture: fixture}, nil
}

// Read implements the legacy untyped capture contract.
func (r *replaySource) Read() []float32 {
	chunk := r.fixture.Read()
	r.advance(len(chunk))
	return chunk
}

// ReadFrame implements audio.FrameSource.
func (r *replaySource) ReadFrame(ctx context.Context) (audio.Frame, error) {
	frame, err := r.fixture.ReadFrame(ctx)
	r.advance(len(frame.Samples))
	return frame, err
}

// Close implements audio.FrameSource.
func (r *replaySource) Close() error { return r.fixture.Close() }

// Exhausted reports whether the whole recording has been handed over.
func (r *replaySource) Exhausted() bool { return r.fixture.Exhausted() }

func (r *replaySource) advance(n int) {
	if n <= 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.samples += int64(n)
}

// Elapsed is how far into the recording the replay has reached.
func (r *replaySource) Elapsed() time.Duration {
	r.mu.Lock()
	defer r.mu.Unlock()
	return time.Duration(r.samples) * time.Second / time.Duration(audio.SampleRate)
}

// progressSource is the seam replayTranscribe drives, so the loop is testable
// with a fake provider and no models.
type progressSource interface {
	Exhausted() bool
	Elapsed() time.Duration
}

// replayTranscribe runs STT sessions back to back over a finite source until
// the recording is consumed, appending each final utterance to the bundle.
//
// It is deliberately not listen.Loop: that loop resets its capture between
// sessions, which for a replay would rewind to the start of the meeting and
// never terminate.
func replayTranscribe(ctx context.Context, source progressSource, provider stt.Provider, writer *meetinglog.Writer) error {
	startedAt := writer.StartedAt()
	for !source.Exhausted() {
		if err := ctx.Err(); err != nil {
			return err
		}
		before := source.Elapsed()
		session, err := provider.Start(ctx)
		if err != nil {
			return fmt.Errorf("start STT session: %w", err)
		}
		text, drainErr := drainReplaySession(ctx, session)
		_ = session.Close()
		if drainErr != nil {
			return drainErr
		}
		if text != "" {
			at := startedAt.Add(source.Elapsed())
			if err := writer.OnUtterance(listen.Utterance{Text: text, At: at}); err != nil {
				return err
			}
		}
		// A session that consumed nothing and produced nothing would spin
		// forever against a source that cannot advance.
		if text == "" && source.Elapsed() == before {
			return nil
		}
	}
	return nil
}

// drainReplaySession consumes one session's events and returns its final
// transcript, if any. A timeout is silence, not an error — long meetings have
// plenty of it.
func drainReplaySession(ctx context.Context, session stt.Session) (string, error) {
	for {
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case event, ok := <-session.Events():
			if !ok {
				return "", nil
			}
			typed := stt.ToTyped(event)
			switch typed.Kind {
			case stt.KindFinalTranscript:
				return typed.Text, nil
			case stt.KindTimeout:
				return "", nil
			case stt.KindFailure:
				failure, _ := event.(stt.Failure)
				return "", fmt.Errorf("transcribe meeting audio: %w", failure.Err)
			}
		}
	}
}
