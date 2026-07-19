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
	"time"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/listen"
	"github.com/lancekrogers/samantha/internal/meetinglog"
	"github.com/lancekrogers/samantha/internal/stt"
	appTUI "github.com/lancekrogers/samantha/internal/tui"
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
	capture, provider, sttLabel, cleanup, err := buildSTTOnly(ctx, &cfgCopy, progress)
	if err != nil {
		return err
	}
	defer cleanup()

	outDir := opts.OutDir
	if outDir == "" {
		outDir = config.MeetingsDir()
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return fmt.Errorf("meeting record: create out dir: %w", err)
	}
	path := filepath.Join(outDir, meetingFilename(opts.Description, time.Now()))

	writer, err := meetinglog.Create(path, opts.Description, sttLabel)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	useTUI := useMeetingRecordTUI(opts)

	var loopErr error
	if useTUI {
		loopErr = appTUI.RunMeeting(appTUI.MeetingOpts{
			Ctx:         ctx,
			Cancel:      cancel,
			Capture:     capture,
			Provider:    provider,
			Writer:      writer,
			Description: opts.Description,
			Path:        path,
			StopPhrases: stopPhraseSet(opts.StopPhrases),
		})
	} else {
		var sinks []listen.Sink
		sinks = append(sinks, writer)
		if opts.JSON {
			sinks = append(sinks, &jsonSink{enc: json.NewEncoder(out)})
		} else {
			fmt.Fprintf(out, "Recording meeting: %q\n", opts.Description)
			fmt.Fprintf(out, "Writing to: %s\n", path)
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

	summary, closeErr := writer.Close()

	var outputErr error
	if opts.JSON {
		outputErr = json.NewEncoder(out).Encode(summary)
	} else {
		fmt.Fprintln(out)
		fmt.Fprintln(out, "Meeting recording stopped.")
		fmt.Fprintf(out, "  Description: %s\n", summary.Description)
		fmt.Fprintf(out, "  Log:         %s\n", summary.File)
		if summary.JSONLFile != "" {
			fmt.Fprintf(out, "  JSONL:       %s\n", summary.JSONLFile)
		}
		fmt.Fprintf(out, "  Duration:    %s\n", summary.Duration().Round(time.Second))
		fmt.Fprintf(out, "  Utterances:  %d\n", summary.Utterances)
		fmt.Fprintf(out, "  Notes:       %d\n", summary.Notes)
		fmt.Fprintf(out, "  Bookmarks:   %d\n", summary.Bookmarks)
	}
	return errors.Join(loopErr, closeErr, outputErr)
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
		if err := os.MkdirAll(outDir, 0o755); err != nil {
			return nil, fmt.Errorf("meeting: create out dir: %w", err)
		}
		path := filepath.Join(outDir, meetingFilename(description, time.Now()))

		// VHS/demo path: skip real mic/models; the TUI scripts STT events.
		if os.Getenv("SAMANTHA_DEMO_MEETING") == "1" ||
			os.Getenv("SAMANTHA_DEMO_MEETING") == "true" ||
			os.Getenv("SAMANTHA_DEMO_MEETING") == "yes" {
			writer, err := meetinglog.Create(path, description, "demo")
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
				Path:        path,
				StopPhrases: stopPhraseSet(nil),
				Cleanup:     func() {},
			}, nil
		}

		capture, provider, sttLabel, cleanup, err := buildSTTOnly(ctx, cfg, progress)
		if err != nil {
			return nil, err
		}
		writer, err := meetinglog.Create(path, description, sttLabel)
		if err != nil {
			cleanup()
			return nil, err
		}
		return &appTUI.MeetingRuntime{
			Capture:     capture,
			Provider:    provider,
			Writer:      writer,
			Description: description,
			Path:        path,
			StopPhrases: stopPhraseSet(nil),
			Cleanup:     cleanup,
		}, nil
	}
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
// ends the recording (same path as Ctrl+C) and is not written as content.
type stopPhraseSink struct {
	inner   listen.Sink
	phrases map[string]bool
	stop    func()
}

func (s *stopPhraseSink) OnUtterance(u listen.Utterance) error {
	if s.phrases[normalizeStopPhrase(u.Text)] {
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
