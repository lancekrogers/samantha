// Package meetinglog writes meeting transcripts: a plain-text log file with a
// header, one appended-and-synced line per utterance, and a summary trailer.
// Writer implements listen.Sink so it plugs directly into listen.Loop.
package meetinglog

import (
	"fmt"
	"os"
	"time"

	"github.com/lancekrogers/samantha/internal/listen"
)

// Summary describes a finished recording, for the file trailer and the
// command's console/JSON summaries.
type Summary struct {
	Description string    `json:"description"`
	File        string    `json:"file"`
	StartedAt   time.Time `json:"started_at"`
	EndedAt     time.Time `json:"ended_at"`
	Utterances  int       `json:"utterances"`
	Errors      int       `json:"errors"`
}

// Duration reports the recording length.
func (s Summary) Duration() time.Duration { return s.EndedAt.Sub(s.StartedAt) }

// Writer is the file sink for one meeting recording. Every line is written
// and synced immediately: a crash mid-meeting loses nothing already heard.
type Writer struct {
	f           *os.File
	path        string
	description string
	started     time.Time
	utterances  int
	errors      int
}

// Create opens path exclusively (never overwrites — the timestamped filename
// makes collisions a config error worth surfacing, not silently absorbing)
// and writes the header.
func Create(path, description, sttLabel string) (*Writer, error) {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return nil, fmt.Errorf("meetinglog: create %s: %w", path, err)
	}
	w := &Writer{f: f, path: path, description: description, started: time.Now()}
	header := fmt.Sprintf("# Meeting: %s\n# Started: %s\n# STT: %s\n\n",
		description, w.started.Format(time.RFC3339), sttLabel)
	if err := w.writeSync(header); err != nil {
		_ = f.Close()
		return nil, err
	}
	return w, nil
}

// Path returns the log file location.
func (w *Writer) Path() string { return w.path }

// OnUtterance implements listen.Sink.
func (w *Writer) OnUtterance(u listen.Utterance) {
	w.utterances++
	_ = w.writeSync(fmt.Sprintf("[%s] %s\n", u.At.Format("15:04:05"), u.Text))
}

// OnTimeout implements listen.Sink. Silence writes nothing.
func (w *Writer) OnTimeout() {}

// OnError implements listen.Sink: errors are part of the record.
func (w *Writer) OnError(err error) {
	w.errors++
	_ = w.writeSync(fmt.Sprintf("[%s] [transcription error: %v]\n", time.Now().Format("15:04:05"), err))
}

// Close writes the trailer and closes the file, returning the summary. If the
// process dies before Close, the header and every synced line survive — only
// the trailer is lost, by design.
func (w *Writer) Close() (Summary, error) {
	s := Summary{
		Description: w.description,
		File:        w.path,
		StartedAt:   w.started,
		EndedAt:     time.Now(),
		Utterances:  w.utterances,
		Errors:      w.errors,
	}
	trailer := fmt.Sprintf("\n# Ended: %s (duration %s, %d utterances, %d errors)\n",
		s.EndedAt.Format(time.RFC3339), s.Duration().Round(time.Second), s.Utterances, s.Errors)
	werr := w.writeSync(trailer)
	cerr := w.f.Close()
	if werr != nil {
		return s, werr
	}
	return s, cerr
}

func (w *Writer) writeSync(line string) error {
	if _, err := w.f.WriteString(line); err != nil {
		return fmt.Errorf("meetinglog: write: %w", err)
	}
	if err := w.f.Sync(); err != nil {
		return fmt.Errorf("meetinglog: sync: %w", err)
	}
	return nil
}
