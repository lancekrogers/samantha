//go:build !integration

package cmd

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/listen"
	"github.com/lancekrogers/samantha/internal/meeting"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/internal/speaker"
	appTUI "github.com/lancekrogers/samantha/internal/tui"
	"github.com/lancekrogers/samantha/pkg/voiceagent/audio"
	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
	"github.com/lancekrogers/samantha/pkg/voiceagent/stt"
)

// runMeetingRecord wires the STT-only chain into listen.Loop with the file
// writer and console/JSON sinks. Nothing is written to disk until the STT
// stack has constructed successfully. On an interactive TTY (and not
// --json / --no-tui) the Bubble Tea meeting UI shows the same voice EQ as
// the conversation screen.
func runMeetingRecord(cmd *cobra.Command, opts meetingOptions) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// One-shot provider override: clear stt_mode exactly as benchmark.go does
	// so a compound alias never conflicts with a leftover configured mode.
	cfgCopy := *cfg
	if opts.STTProvider != "" {
		cfgCopy.STTProvider = opts.STTProvider
		cfgCopy.STTMode = ""
	}

	ctx, cancel := signalContext()
	defer cancel()

	progress := meetingAssetProgress(opts.JSON)
	capture, provider, sttLabel, sttCleanup, err := buildSTTOnly(ctx, &cfgCopy, progress)
	if err != nil {
		return err
	}
	// Two-stage teardown (launcher parity, WI-162bbb R1): releaseCapture frees
	// the mic/STT/live stack at capture stop — the device must not stay hot
	// through review and diarization — while the analyzer stays alive for
	// background Finalize; the deferred speakerSession.Close releases it.
	var speakerSession *meeting.SpeakerSession
	var stopLive func()
	var releaseOnce sync.Once
	releaseCapture := func() {
		releaseOnce.Do(func() {
			if stopLive != nil {
				stopLive()
			}
			if speakerSession != nil {
				speakerSession.StopCapture()
			}
			sttCleanup()
		})
	}
	defer releaseCapture()

	outDir := opts.OutDir
	if outDir == "" {
		outDir = config.MeetingsDir()
	}
	// 0o700: meeting filenames include description slugs; keep the directory
	// private the same way auth/cert paths are.
	if err := os.MkdirAll(outDir, 0o700); err != nil {
		return fmt.Errorf("meeting record: create out dir: %w", err)
	}
	bundlePath := filepath.Join(outDir, meetingBundleName(opts.Description, time.Now()))

	writer, err := meetinglog.CreateBundle(bundlePath, opts.Description, sttLabel, "mac")
	if err != nil {
		return err
	}
	// Durable delivery intent: an auto destination known at start is recorded
	// so the launcher's startup sweep can retry if this process never routes.
	// A failed write voids that promise — say so instead of claiming durability.
	if dest := cliAutoRouteDest(meeting.FromConfig(&cfgCopy), opts); dest != "" {
		mode := "auto"
		if opts.RouteTo != "" {
			mode = "dest"
		}
		if err := writer.WriteRoutePlan(dest, mode); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(),
				"warning: route plan not persisted: %v — %s delivery will not survive a crash\n", err, dest)
		}
	}
	speakerSession, speakerSetupErr := prepareMeetingSpeakers(ctx, &cfgCopy, capture, writer, bundlePath, progress)
	if speakerSession != nil {
		defer speakerSession.Close()
	}
	if speakerSetupErr != nil {
		_ = writer.WriteSpeakerAnalysis(meetinglog.SpeakerAnalysis{
			Status: string(meeting.AnalysisError), Error: speakerSetupErr.Error(),
		})
	}
	var liveSpeaker appTUI.LiveSpeakerController
	liveSpeaker, stopLive = prepareMeetingLiveSpeaker(ctx, &cfgCopy, capture, progress)

	out := cmd.OutOrStdout()
	useTUI := useMeetingRecordTUI(opts)

	var loopErr error
	if useTUI {
		loopErr = appTUI.RunMeeting(appTUI.MeetingOpts{
			Ctx:           ctx,
			Cancel:        cancel,
			Capture:       capture,
			Provider:      provider,
			Writer:        writer,
			Description:   opts.Description,
			Path:          bundlePath,
			StopPhrases:   stopPhraseSet(opts.StopPhrases),
			SpeakerStatus: speakerInitialStatus(speakerSession, speakerSetupErr),
			SpeakerError:  speakerInitialDetail(speakerSession, speakerSetupErr),
			LiveSpeaker:   liveSpeaker,
		})
	} else {
		var sinks []listen.Sink
		sinks = append(sinks, writer)
		if opts.JSON {
			sinks = append(sinks, &jsonSink{enc: json.NewEncoder(out)})
		} else {
			fmt.Fprintf(out, "Recording meeting: %q\n", opts.Description)
			fmt.Fprintf(out, "Meeting bundle: %s\n", bundlePath)
			fmt.Fprintln(out, "🎙 Listening... (say \"stop recording\" or press Ctrl+C to stop)")
			sinks = append(sinks, &consoleSink{out: out, errOut: cmd.ErrOrStderr()})
		}
		sink := &stopPhraseSink{
			inner:   multiSink(sinks),
			phrases: stopPhraseSet(opts.StopPhrases),
			stop:    cancel,
		}
		loopErr = listen.Loop(ctx, capture, provider, sink)
	}

	// Capture ended: free the mic/STT/live stack now — the device must not
	// stay hot through review or diarization, and unsubscribing the speaker
	// session here keeps post-stop PCM out of the diarization working file.
	releaseCapture()
	// Trailer/session_end land now. Diarization and routing run behind the
	// review instead of gating it (WI-162bbb R1/R2); analysis events append
	// to the closed bundle.
	summary, closeErr := writer.Close()

	resultsAnalysis, waitAnalysis, stopAnalysis := startBackgroundDiarize(speakerSession)

	// Auto-chosen destinations (--route, mode=auto default) deliver at capture
	// end, before review; ask-mode still prompts after review.
	var routeErr error
	autoDest := ""
	routeCfg, cfgErr := config.Load()
	if cfgErr != nil {
		routeErr = cfgErr
	} else {
		autoDest = cliAutoRouteDest(meeting.FromConfig(routeCfg), opts)
	}
	if closeErr == nil && cfgErr == nil && autoDest != "" {
		routeErr = routeAfterRecordTo(cmd, routeCfg, summary, autoDest, opts.JSON)
	}

	if useTUI && closeErr == nil {
		if err := appTUI.RunMeetingResults(summary, resultsAnalysis); err != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "meeting review: %v\n", err)
		}
	}
	if closeErr == nil && cfgErr == nil && autoDest == "" {
		routeErr = maybeRouteAfterRecord(cmd, routeCfg, summary, opts)
	}

	speakerResult, speakerRan := awaitDiarize(cmd, waitAnalysis, stopAnalysis)
	if speakerRan {
		foldSpeakerOutcome(&summary, speakerResult)
	}

	var outputErr error
	if opts.JSON {
		outputErr = json.NewEncoder(out).Encode(summary)
	} else {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Meeting recording stopped.")
		fmt.Fprintf(out, "  Description: %s\n", summary.Description)
		fmt.Fprintf(out, "  Bundle:      %s\n", summary.Bundle)
		fmt.Fprintf(out, "  Meeting:     %s\n", summary.File)
		fmt.Fprintf(out, "  Duration:    %s\n", summary.Duration().Round(time.Second))
		fmt.Fprintf(out, "  Utterances:  %d\n", summary.Utterances)
		fmt.Fprintf(out, "  Notes:       %d\n", summary.Notes)
		fmt.Fprintf(out, "  Bookmarks:   %d\n", summary.Bookmarks)
		if speakerSetupErr != nil {
			fmt.Fprintf(cmd.ErrOrStderr(), "  Speaker analysis unavailable: %v (recording preserved)\n", speakerSetupErr)
		} else if speakerResult.Status != "" {
			fmt.Fprintf(out, "  Speakers:    %s\n", meetingAnalysisSummary(speakerResult))
		}
	}
	return errors.Join(loopErr, closeErr, outputErr, routeErr)
}

// startBackgroundDiarize finalizes speaker analysis off the main flow. It
// returns one outcome channel for the live review fold, one for the summary
// wait (each buffered so neither reader can starve the other), and a cancel
// that releases the skip-signal watcher.
func startBackgroundDiarize(session *meeting.SpeakerSession) (<-chan appTUI.MeetingAnalysisOutcome, <-chan appTUI.MeetingAnalysisOutcome, context.CancelFunc) {
	if session == nil {
		return nil, nil, nil
	}
	// A fresh signal context makes Ctrl-C skip enrichment cleanly instead of
	// being swallowed by the recording's already-fired handler.
	sctx, scancel := signalContext()
	actx, acancel := context.WithTimeout(sctx, 2*time.Hour)
	cancel := func() {
		acancel()
		scancel()
	}
	resultsCh := make(chan appTUI.MeetingAnalysisOutcome, 1)
	waitCh := make(chan appTUI.MeetingAnalysisOutcome, 1)
	go func() {
		result, err := session.Finalize(actx)
		outcome := appTUI.MeetingAnalysisOutcome{Result: result, Err: err}
		resultsCh <- outcome
		close(resultsCh)
		waitCh <- outcome
		close(waitCh)
	}()
	return resultsCh, waitCh, cancel
}

// awaitDiarize blocks for background analysis, telling an interactive user
// how to skip. Ctrl-C cancels enrichment only — the transcript and any
// delivered route are already on disk.
func awaitDiarize(cmd *cobra.Command, wait <-chan appTUI.MeetingAnalysisOutcome, cancel context.CancelFunc) (meeting.AnalysisResult, bool) {
	if wait == nil {
		return meeting.AnalysisResult{}, false
	}
	defer cancel()
	select {
	case outcome := <-wait:
		return outcome.Result, true
	default:
	}
	fmt.Fprintln(cmd.ErrOrStderr(), "Diarizing speakers… (Ctrl+C skips speaker labels; transcript is saved)")
	outcome := <-wait
	return outcome.Result, true
}

// foldSpeakerOutcome mirrors the bundle's post-close analysis into the
// summary so console/JSON output reports what diarization produced.
func foldSpeakerOutcome(summary *meetinglog.Summary, result meeting.AnalysisResult) {
	summary.SpeakerStatus = string(result.Status)
	summary.SpeakerCount = result.SpeakerCount
	summary.SpeakerAnalysisFile = result.Artifact
	summary.AudioFile = result.AudioFile
	summary.SpeakerError = result.Error
}

// useMeetingRecordTUI is true for an interactive terminal session that should
// open the Bubble Tea recorder. --json and --no-tui keep the plain sinks so
// scripts never hang on a full-screen UI.
func useMeetingRecordTUI(opts meetingOptions) bool {
	if opts.JSON || opts.NoTUI {
		return false
	}
	return isatty.IsTerminal(os.Stdout.Fd()) && isatty.IsTerminal(os.Stdin.Fd())
}

// meetingRuntimeBuilder powers the main launcher "Meeting" entry.
func meetingRuntimeBuilder() appTUI.MeetingBuilder {
	return func(ctx context.Context, description string, progress func(string, float64)) (*appTUI.MeetingRuntime, error) {
		cfg, err := config.Load()
		if err != nil {
			return nil, err
		}
		outDir := config.MeetingsDir()
		if err := os.MkdirAll(outDir, 0o700); err != nil {
			return nil, fmt.Errorf("meeting: create out dir: %w", err)
		}
		bundlePath := filepath.Join(outDir, meetingBundleName(description, time.Now()))

		// VHS/demo path: skip real mic/models; the TUI scripts STT events.
		if os.Getenv("SAMANTHA_DEMO_MEETING") == "1" ||
			os.Getenv("SAMANTHA_DEMO_MEETING") == "true" ||
			os.Getenv("SAMANTHA_DEMO_MEETING") == "yes" {
			writer, err := meetinglog.CreateBundle(bundlePath, description, "demo", "mac")
			if err != nil {
				return nil, err
			}
			return &appTUI.MeetingRuntime{
				// Non-nil placeholders; meeting loop swaps in demo provider when
				// SAMANTHA_DEMO_MEETING is set.
				Capture:     noopResetter{},
				Provider:    noopProvider{},
				Writer:      writer,
				Description: description,
				Path:        bundlePath,
				StopPhrases: stopPhraseSet(nil),
				Cleanup:     func() {},
			}, nil
		}

		capture, provider, sttLabel, cleanup, err := buildSTTOnly(ctx, cfg, progress)
		if err != nil {
			return nil, err
		}
		writer, err := meetinglog.CreateBundle(bundlePath, description, sttLabel, "mac")
		if err != nil {
			cleanup()
			return nil, err
		}
		speakerSession, speakerSetupErr := prepareMeetingSpeakers(ctx, cfg, capture, writer, bundlePath, progress)
		if speakerSetupErr != nil {
			_ = writer.WriteSpeakerAnalysis(meetinglog.SpeakerAnalysis{
				Status: string(meeting.AnalysisError), Error: speakerSetupErr.Error(),
			})
		}
		liveSpeaker, stopLive := prepareMeetingLiveSpeaker(ctx, cfg, capture, progress)
		runtimeCleanup := cleanup
		// Two-stage teardown: capture resources (mic, STT, live labels) release
		// at stop so the next screen can use the device, while the speaker
		// analyzer stays alive for background diarization. Each stage is
		// idempotent so the global-quit path may run both unconditionally.
		var releaseOnce, cleanupOnce sync.Once
		releaseCapture := func() {
			releaseOnce.Do(func() {
				if stopLive != nil {
					stopLive()
				}
				if speakerSession != nil {
					speakerSession.StopCapture()
				}
				runtimeCleanup()
			})
		}
		cleanup = func() {
			releaseCapture()
			cleanupOnce.Do(func() {
				if speakerSession != nil {
					_ = speakerSession.Close()
				}
			})
		}
		return &appTUI.MeetingRuntime{
			Capture:          capture,
			Provider:         provider,
			Writer:           writer,
			FinalizeSpeakers: speakerFinalizer(speakerSession),
			SpeakerStatus:    speakerInitialStatus(speakerSession, speakerSetupErr),
			SpeakerError:     speakerInitialDetail(speakerSession, speakerSetupErr),
			LiveSpeaker:      liveSpeaker,
			Description:      description,
			Path:             bundlePath,
			StopPhrases:      stopPhraseSet(nil),
			ReleaseCapture:   releaseCapture,
			Cleanup:          cleanup,
		}, nil
	}
}

// prepareMeetingLiveSpeaker builds the same LazyLive stack as chat when
// speaker.meeting.live is on. Capture is shared with STT (fan-out); live feed
// never blocks the listen loop.
func prepareMeetingLiveSpeaker(
	ctx context.Context,
	cfg *config.Config,
	capture *audio.Capture,
	progress func(string, float64),
) (appTUI.LiveSpeakerController, func()) {
	sp := speaker.FromAppConfig(cfg)
	if !sp.MeetingLiveActive() || capture == nil {
		return nil, nil
	}
	// Reuse chat's live builder so meeting labels share the embedding model
	// and /speakers-class startup path (assets ensured on first enable).
	controller, stop, _ := prepareLiveSpeaker(ctx, cfg, capture, progress)
	if controller == nil {
		return nil, stop
	}
	// Meeting session wants labels immediately when live is configured on.
	controller.SetEnabled(true)
	return controller, stop
}

func prepareMeetingSpeakers(ctx context.Context, cfg *config.Config, capture *audio.Capture, writer *meetinglog.Writer, bundlePath string, progress func(string, float64)) (*meeting.SpeakerSession, error) {
	sp := speaker.FromAppConfig(cfg)
	if !sp.MeetingActive() {
		return nil, nil
	}
	if err := config.EnsureRuntimeAssets(ctx, cfg, config.AssetRequest{NeedSpeaker: true}, progress); err != nil {
		return nil, fmt.Errorf("prepare speaker models: %w", err)
	}
	engine, err := speaker.NewSherpaEngine(sp, config.ModelsDirFrom(cfg))
	if err != nil {
		return nil, err
	}
	seedEnrolledProfiles(cfg, engine, nil)
	analyzer, err := speaker.NewAnalyzer(sp, engine)
	if err != nil {
		_ = engine.Close()
		return nil, err
	}
	session, err := meeting.NewSpeakerSession(capture, analyzer, writer, bundlePath, sp.Meeting.RecordAudio)
	if err != nil {
		_ = analyzer.Close()
		return nil, err
	}
	return session, nil
}

func speakerFinalizer(session *meeting.SpeakerSession) func(context.Context) (meeting.AnalysisResult, error) {
	if session == nil {
		return nil
	}
	return session.Finalize
}

func speakerInitialStatus(session *meeting.SpeakerSession, setupErr error) meeting.AnalysisStatus {
	if setupErr != nil {
		return meeting.AnalysisError
	}
	if session != nil {
		return meeting.AnalysisQueued
	}
	return meeting.AnalysisDisabled
}

func speakerInitialDetail(session *meeting.SpeakerSession, setupErr error) string {
	if setupErr != nil {
		return setupErr.Error() + " (recording unaffected)"
	}
	if session != nil {
		return "collecting audio; diarizes when recording stops"
	}
	return ""
}

func meetingAnalysisSummary(result meeting.AnalysisResult) string {
	if result.Error != "" {
		return string(result.Status) + " — " + result.Error
	}
	return fmt.Sprintf("%s — %d detected · %s", result.Status, result.SpeakerCount, result.Artifact)
}

// noopResetter/provider satisfy the MeetingRuntime fields for demo builds.
// The meeting TUI replaces them when SAMANTHA_DEMO_MEETING is set.
type noopResetter struct{}

func (noopResetter) Reset() {}

type noopProvider struct{}

func (noopProvider) Available() bool { return true }
func (noopProvider) Start(ctx context.Context) (stt.Session, error) {
	return nil, fmt.Errorf("demo noop provider")
}

func meetingAssetProgress(jsonOutput bool) func(string, float64) {
	if jsonOutput {
		// Machine-readable output must remain JSONL even on a first run that
		// downloads model assets. modelProgress writes human text to stdout.
		return nil
	}
	return modelProgress
}

// buildSTTOnly constructs asset preflight -> capture -> VAD -> STT provider,
// with no Brain or TTS — the chain benchmark.go's runSingleSTTBenchmark
// proves. VAD is required: every shipped STT backend rejects a nil VAD.
// root.go's buildPipeline should eventually share this helper.
func buildSTTOnly(ctx context.Context, cfg *config.Config, progress func(string, float64)) (capture *audio.Capture, provider stt.Provider, sttLabel string, cleanup func(), err error) {
	if err := config.EnsureRuntimeAssets(ctx, cfg, config.AssetRequest{NeedSTT: true, NeedVAD: true}, progress); err != nil {
		return nil, nil, "", nil, err
	}

	var cleanups []func()
	cleanup = func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}
	fail := func(e error) (*audio.Capture, stt.Provider, string, func(), error) {
		cleanup()
		return nil, nil, "", nil, e
	}

	capture = audio.NewCaptureWithDevice(cfg.InputDevice)
	if err := capture.Start(ctx); err != nil {
		return fail(fmt.Errorf("start capture: %w", err))
	}
	cleanups = append(cleanups, capture.Stop)

	vad, err := audio.NewVAD(cfg)
	if err != nil {
		return fail(fmt.Errorf("init VAD: %w", err))
	}
	cleanups = append(cleanups, vad.Delete)

	provider, sttCleanup, err := stt.NewProvider(cfg, capture, vad)
	if err != nil {
		return fail(fmt.Errorf("init STT: %w", err))
	}
	if sttCleanup != nil {
		cleanups = append(cleanups, sttCleanup)
	}

	label := cfg.STTProvider
	if norm, nerr := config.NormalizeSTTWithMode(cfg.STTProvider, cfg.STTMode); nerr == nil {
		label = fmt.Sprintf("%s (%s)", norm.Provider, norm.Mode)
	}
	return capture, provider, label, cleanup, nil
}

// multiSink fans one event out to every sink.
type multiSink []listen.Sink

func (m multiSink) OnUtterance(u listen.Utterance) error {
	for _, s := range m {
		if err := s.OnUtterance(u); err != nil {
			return err
		}
	}
	return nil
}
func (m multiSink) OnTimeout() error {
	for _, s := range m {
		if err := s.OnTimeout(); err != nil {
			return err
		}
	}
	return nil
}
func (m multiSink) OnError(err error) error {
	for _, s := range m {
		if sinkErr := s.OnError(err); sinkErr != nil {
			return sinkErr
		}
	}
	return nil
}

// stopPhraseSink intercepts stop phrases before they reach the log: a match
// ends the recording (same path as Ctrl+C) and is intentionally not written
// as content (operators should not see the stop command in the transcript).
type stopPhraseSink struct {
	inner   listen.Sink
	phrases map[string]bool
	stop    func()
}

func (s *stopPhraseSink) OnUtterance(u listen.Utterance) error {
	if s.phrases[meeting.NormalizeStopPhrase(u.Text)] {
		s.stop()
		return nil
	}
	return s.inner.OnUtterance(u)
}
func (s *stopPhraseSink) OnTimeout() error        { return s.inner.OnTimeout() }
func (s *stopPhraseSink) OnError(err error) error { return s.inner.OnError(err) }

// consoleSink echoes utterances to stdout and errors to stderr.
type consoleSink struct {
	out    io.Writer
	errOut io.Writer
}

func (c *consoleSink) OnUtterance(u listen.Utterance) error {
	_, err := fmt.Fprintf(c.out, "[%s] %s\n", u.At.Format("15:04:05"), u.Text)
	return err
}
func (c *consoleSink) OnTimeout() error { return nil }
func (c *consoleSink) OnError(err error) error {
	_, writeErr := fmt.Fprintf(c.errOut, "  transcription error: %v (retrying)\n", err)
	return writeErr
}

// jsonSink emits one JSON line per utterance for live scripting.
type jsonSink struct{ enc *json.Encoder }

func (j *jsonSink) OnUtterance(u listen.Utterance) error {
	return j.enc.Encode(struct {
		TS   string `json:"ts"`
		Text string `json:"text"`
	}{TS: u.At.Format(time.RFC3339), Text: u.Text})
}
func (j *jsonSink) OnTimeout() error { return nil }
func (j *jsonSink) OnError(_ error) error {
	return nil
}
