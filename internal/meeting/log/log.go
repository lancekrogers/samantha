// Package log records a meeting as a human-readable .log plus a structured
// .jsonl event stream. Both files are synced after every event so a crash
// never loses what was already captured.
//
// Import as:
//
//	import meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
//
// JSONL event types: session_start, utterance, note, bookmark, error, session_end, routed.
// Notes and bookmarks carry offset_ms from session start so they can be
// aligned with the transcript later (Plaude-style important moments).
package log

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lancekrogers/samantha/internal/listen"
)

// Event kinds written to the JSONL sidecar.
const (
	TypeSessionStart = "session_start"
	TypeUtterance    = "utterance"
	TypeNote         = "note"
	TypeBookmark     = "bookmark"
	TypeError        = "error"
	TypeSessionEnd   = "session_end"
)

// Event is one JSONL record. OffsetMs is milliseconds since session start.
type Event struct {
	Type     string `json:"type"`
	TS       string `json:"ts"`                // RFC3339
	OffsetMs int64  `json:"offset_ms"`         // from session start
	Text     string `json:"text,omitempty"`    // utterance / note body
	Label    string `json:"label,omitempty"`   // bookmark label (default "important")
	Message  string `json:"message,omitempty"` // error text
	STT      string `json:"stt,omitempty"`     // session_start only
	Desc     string `json:"description,omitempty"`
}

// Summary describes a finished recording, for the file trailer and the
// command's console/JSON summaries.
type Summary struct {
	Description     string    `json:"description"`
	File            string    `json:"file"`       // plain-text .log
	JSONLFile       string    `json:"jsonl_file"` // structured .jsonl
	StartedAt       time.Time `json:"started_at"`
	EndedAt         time.Time `json:"ended_at"`
	DurationSeconds int64     `json:"duration_seconds"`
	Utterances      int       `json:"utterances"`
	Notes           int       `json:"notes"`
	Bookmarks       int       `json:"bookmarks"`
	Errors          int       `json:"errors"`
}

// Duration reports the recording length.
func (s Summary) Duration() time.Duration { return s.EndedAt.Sub(s.StartedAt) }

// Writer is the dual-file sink for one meeting recording.
type Writer struct {
	mu          sync.Mutex
	log         *os.File
	jsonl       *os.File
	path        string
	jsonlPath   string
	description string
	sttLabel    string
	started     time.Time
	utterances  int
	notes       int
	bookmarks   int
	errors      int
	closed      bool
	summary     Summary // last Close result; returned again on idempotent Close
}

// Create opens path (.log) exclusively and a sibling .jsonl file. Path must
// end in .log (or any extension); the JSONL path replaces/adds .jsonl.
func Create(path, description, sttLabel string) (*Writer, error) {
	// 0o600: meeting transcripts are private (credentials, personal speech).
	logF, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("meetinglog: create %s: %w", path, err)
	}
	jsonlPath := jsonlPathFor(path)
	jsonlF, err := os.OpenFile(jsonlPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		_ = logF.Close()
		_ = os.Remove(path)
		return nil, fmt.Errorf("meetinglog: create %s: %w", jsonlPath, err)
	}

	w := &Writer{
		log:         logF,
		jsonl:       jsonlF,
		path:        path,
		jsonlPath:   jsonlPath,
		description: description,
		sttLabel:    sttLabel,
		started:     time.Now(),
	}
	header := fmt.Sprintf("# Meeting: %s\n# Started: %s\n# STT: %s\n# JSONL: %s\n\n",
		description, w.started.Format(time.RFC3339), sttLabel, jsonlPath)
	if err := w.writeLog(header); err != nil {
		_ = w.abortCreate()
		return nil, err
	}
	if err := w.writeEvent(Event{
		Type: TypeSessionStart,
		Desc: description,
		STT:  sttLabel,
	}); err != nil {
		_ = w.abortCreate()
		return nil, err
	}
	return w, nil
}

func (w *Writer) abortCreate() error {
	_ = w.log.Close()
	_ = w.jsonl.Close()
	_ = os.Remove(w.path)
	_ = os.Remove(w.jsonlPath)
	return nil
}

// jsonlPathFor maps foo.log → foo.jsonl and foo → foo.jsonl.
func jsonlPathFor(path string) string {
	ext := filepath.Ext(path)
	if ext == "" {
		return path + ".jsonl"
	}
	return strings.TrimSuffix(path, ext) + ".jsonl"
}

// Path returns the plain-text log path.
func (w *Writer) Path() string { return w.path }

// JSONLPath returns the structured event stream path.
func (w *Writer) JSONLPath() string { return w.jsonlPath }

// StartedAt returns when the session opened.
func (w *Writer) StartedAt() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.started
}

// OnUtterance implements listen.Sink.
func (w *Writer) OnUtterance(u listen.Utterance) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if err := w.writeLog(fmt.Sprintf("[%s] %s\n", u.At.Format("15:04:05"), u.Text)); err != nil {
		return err
	}
	if err := w.writeEvent(Event{Type: TypeUtterance, Text: u.Text, TS: u.At.Format(time.RFC3339)}); err != nil {
		return err
	}
	w.utterances++
	return nil
}

// OnTimeout implements listen.Sink. Silence writes nothing.
func (w *Writer) OnTimeout() error { return nil }

// OnError implements listen.Sink: errors are part of the record.
func (w *Writer) OnError(err error) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	if writeErr := w.writeLog(fmt.Sprintf("[%s] [transcription error: %v]\n", now.Format("15:04:05"), err)); writeErr != nil {
		return writeErr
	}
	if writeErr := w.writeEvent(Event{Type: TypeError, Message: err.Error(), TS: now.Format(time.RFC3339)}); writeErr != nil {
		return writeErr
	}
	w.errors++
	return nil
}

// AddNote records a typed note at the current offset (meeting-relative).
func (w *Writer) AddNote(text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	if err := w.writeLog(fmt.Sprintf("[%s] 📝 note: %s\n", now.Format("15:04:05"), text)); err != nil {
		return err
	}
	if err := w.writeEvent(Event{Type: TypeNote, Text: text, TS: now.Format(time.RFC3339)}); err != nil {
		return err
	}
	w.notes++
	return nil
}

// AddBookmark marks an important moment (Plaude-style). Label defaults to
// "important". Optional text is a short caption from the note field.
func (w *Writer) AddBookmark(label, text string) error {
	label = strings.TrimSpace(label)
	if label == "" {
		label = "important"
	}
	text = strings.TrimSpace(text)
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()
	line := fmt.Sprintf("[%s] ★ %s", now.Format("15:04:05"), strings.ToUpper(label))
	if text != "" {
		line += ": " + text
	}
	line += "\n"
	if err := w.writeLog(line); err != nil {
		return err
	}
	if err := w.writeEvent(Event{
		Type:  TypeBookmark,
		Label: label,
		Text:  text,
		TS:    now.Format(time.RFC3339),
	}); err != nil {
		return err
	}
	w.bookmarks++
	return nil
}

// Close writes the trailer / session_end and closes both files.
// Safe to call more than once: later calls return the first Summary and a nil
// error so embedded teardown and the CLI summary path can both Close.
func (w *Writer) Close() (Summary, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return w.summary, nil
	}
	s := Summary{
		Description: w.description,
		File:        w.path,
		JSONLFile:   w.jsonlPath,
		StartedAt:   w.started,
		EndedAt:     time.Now(),
		Utterances:  w.utterances,
		Notes:       w.notes,
		Bookmarks:   w.bookmarks,
		Errors:      w.errors,
	}
	s.DurationSeconds = int64(s.Duration().Round(time.Second) / time.Second)
	trailer := fmt.Sprintf("\n# Ended: %s (duration %s, %d utterances, %d notes, %d bookmarks, %d errors)\n",
		s.EndedAt.Format(time.RFC3339), s.Duration().Round(time.Second),
		s.Utterances, s.Notes, s.Bookmarks, s.Errors)
	werr := w.writeLog(trailer)
	jerr := w.writeEvent(Event{Type: TypeSessionEnd, TS: s.EndedAt.Format(time.RFC3339)})
	cerr1 := w.log.Close()
	cerr2 := w.jsonl.Close()
	w.closed = true
	w.summary = s
	if werr != nil {
		return s, werr
	}
	if jerr != nil {
		return s, jerr
	}
	if cerr1 != nil {
		return s, cerr1
	}
	return s, cerr2
}

func (w *Writer) writeLog(line string) error {
	if _, err := w.log.WriteString(line); err != nil {
		return fmt.Errorf("meetinglog: write log: %w", err)
	}
	if err := w.log.Sync(); err != nil {
		return fmt.Errorf("meetinglog: sync log: %w", err)
	}
	return nil
}

// writeEvent appends one JSONL line. TS/OffsetMs are filled if zero-ish.
// Caller must hold w.mu.
func (w *Writer) writeEvent(e Event) error {
	now := time.Now()
	if e.TS == "" {
		e.TS = now.Format(time.RFC3339)
	}
	// Prefer wall clock of TS when parseable for offset; else now.
	at := now
	if t, err := time.Parse(time.RFC3339, e.TS); err == nil {
		at = t
	}
	e.OffsetMs = at.Sub(w.started).Milliseconds()
	if e.OffsetMs < 0 {
		e.OffsetMs = 0
	}
	data, err := json.Marshal(e)
	if err != nil {
		return fmt.Errorf("meetinglog: marshal event: %w", err)
	}
	if _, err := w.jsonl.Write(append(data, '\n')); err != nil {
		return fmt.Errorf("meetinglog: write jsonl: %w", err)
	}
	if err := w.jsonl.Sync(); err != nil {
		return fmt.Errorf("meetinglog: sync jsonl: %w", err)
	}
	return nil
}
