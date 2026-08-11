//go:build !integration

package cmd

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/listen"
	"github.com/lancekrogers/samantha/internal/meeting"
	"github.com/lancekrogers/samantha/internal/meeting/ideas"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/internal/meeting/remote"
	"github.com/lancekrogers/samantha/internal/netapi"
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
	step := job.Step
	if step == nil {
		step = func(string) {}
	}
	step("transcribing")
	if err := transcribeMeetingAudio(ctx, cfg, job); err != nil {
		return err
	}
	step("filing ideas")
	// Idea spans resolve as soon as the transcript exists — before diarization,
	// so a speaker-stack failure cannot cost anyone their filed ideas. A
	// resolution error is itself non-fatal: the meeting's notes are worth more
	// than one intent, and unfiled spans retry on reprocess.
	if report, err := resolveMeetingIdeas(ctx, job); err != nil {
		fmt.Fprintf(os.Stderr, "meeting %s: idea resolution: %v\n", job.Title, err)
	} else if line := formatIdeaReport(job.Title, report); line != "" {
		fmt.Fprintln(os.Stderr, line)
	}
	// The diarize step only appears when speaker analysis is enabled — an
	// anonymous stage the phone shows for a feature that is off would lie.
	if speaker.FromAppConfig(cfg).MeetingActive() {
		step("diarizing")
	}
	if _, err := diarizeMeetingAudio(ctx, cfg, job); err != nil {
		return fmt.Errorf("speaker analysis: %w", err)
	}
	return nil
}

// resolveMeetingIdeas files spoken idea spans through the same intent file
// sink POST /v1/intent writes to (serve default: <config>/serve/intents).
func resolveMeetingIdeas(ctx context.Context, job remote.Job) (ideas.Report, error) {
	sinkDir := filepath.Join(config.ConfigDir(), "serve", "intents")
	// The session's wire id, falling back to the bundle name for pipelines
	// fed outside serve (reprocessing tools).
	meetingID := job.MeetingID
	if meetingID == "" {
		meetingID = filepath.Base(job.BundlePath)
	}
	return ideas.Resolve(ctx, job.BundlePath, job.Writer, func(ctx context.Context, idea ideas.Resolved) (bool, error) {
		// Deterministic id = durable receipt: if a previous pass filed this
		// span and crashed before its bundle marker landed, the retry finds
		// the intent file itself (create-if-absent) instead of duplicating.
		id := spanIntentKey(meetingID, idea.SpanID)
		_, created, err := netapi.WriteIntentFileWithID(sinkDir, id, netapi.IntentRequest{
			Type:       "note",
			Body:       idea.Body,
			Source:     "meeting",
			CapturedAt: time.Now().UTC().Format(time.RFC3339),
			Context:    &netapi.IntentContext{MeetingID: meetingID, OffsetMs: idea.StartMS},
		})
		return created, err
	})
}

func formatIdeaReport(title string, report ideas.Report) string {
	if report.Filed == 0 && report.Rediscovered == 0 && report.AlreadyFiled == 0 &&
		report.Unresolved == 0 && report.Failed == 0 && report.MarkerFailed == 0 {
		return ""
	}
	return fmt.Sprintf(
		"meeting %s: ideas filed=%d rediscovered=%d already_marked=%d unresolved=%d failed=%d marker_failed=%d",
		title, report.Filed, report.Rediscovered, report.AlreadyFiled,
		report.Unresolved, report.Failed, report.MarkerFailed,
	)
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
	seedEnrolledProfiles(cfg, engine, nil)
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

// Close implements audio.FrameSource as a no-op. The replay outlives the
// individual STT sessions that read it — a provider closing its session must
// not end the recording halfway through. There is nothing to release: the WAV
// was read into memory at construction.
func (r *replaySource) Close() error { return nil }

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
		// A session that consumed no audio cannot be making progress, whatever
		// it returned; without this the loop would spin on a stuck source.
		if source.Elapsed() == before {
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

// newServeMeetingRouter files a finished remote meeting into a campaign
// (POST /v1/meeting/{id}/route). It reuses the desktop routing stack —
// Render + CampaignSink via Router.RouteMeeting — so a phone-routed note is
// byte-identical to one routed from the TUI.
//
// The CI0009 gate runs per call, not cached: serve stays up for days while
// camp gets upgraded underneath it, and the probe is one fast --help spawn.
func newServeMeetingRouter(cfg *config.Config) netapi.RouteMeetingFunc {
	routeCfg := meeting.FromConfig(cfg)
	return func(ctx context.Context, summary meetinglog.Summary, campaign, capture string) (remote.RouteReceipt, error) {
		if err := ctx.Err(); err != nil {
			return remote.RouteReceipt{}, err
		}
		if meeting.NormalizeCampaignCapture(capture) == meeting.CaptureMeeting {
			if err := meeting.SupportsImportMeeting(ctx, meeting.DefaultRunner, meeting.DefaultLookPath); err != nil {
				return remote.RouteReceipt{}, err
			}
		}
		note, err := meeting.Render(summary, routeCfg.Body)
		if err != nil {
			return remote.RouteReceipt{}, fmt.Errorf("render meeting note: %w", err)
		}
		// The phone names the campaign directly; no camp-list discovery
		// round-trip is needed to construct the destination.
		dest := meeting.Destination{
			ID:       "camp:" + campaign,
			Type:     meeting.TypeCampaign,
			Campaign: campaign,
			Capture:  capture,
		}
		router := meeting.NewDefaultRouter(routeCfg)
		receipt, err := router.RouteMeeting(ctx, note, dest)
		if err != nil {
			return remote.RouteReceipt{}, err
		}
		destination := campaign + " notes/meetings"
		if meeting.NormalizeCampaignCapture(capture) != meeting.CaptureMeeting {
			destination = campaign + " (idea add)"
		}
		return remote.RouteReceipt{
			Outcome:     receipt.Outcome,
			Detail:      receipt.Detail,
			Destination: destination,
		}, nil
	}
}

// spanIntentKey keeps a readable prefix but hashes the original pair so ids
// that sanitize or truncate to the same text can never suppress each other.
func spanIntentKey(meetingID, spanID string) string {
	digest := sha256.Sum256([]byte(meetingID + "\x00" + spanID))
	return fmt.Sprintf("meeting-%s-span-%s-%x",
		sanitizeIntentKey(meetingID), sanitizeIntentKey(spanID), digest)
}

// sanitizeIntentKey keeps the readable portion of ids filesystem-safe. It is
// not itself the uniqueness boundary; spanIntentKey's digest supplies that.
func sanitizeIntentKey(raw string) string {
	var b strings.Builder
	for _, r := range raw {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
		if b.Len() >= 64 {
			break
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}
