// Package log records a meeting as a human-readable document plus a structured
// JSONL event stream. Both files are synced after every event so a crash
// never loses what was already captured.
//
// Import as:
//
//	import meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
//
// JSONL event types: session_start, utterance, note, bookmark, error,
// speaker_analysis, speaker_segment, speaker_utterance, session_end, routed.
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
	TypeSessionStart     = "session_start"
	TypeUtterance        = "utterance"
	TypeNote             = "note"
	TypeBookmark         = "bookmark"
	TypeError            = "error"
	TypeSpeakerAnalysis  = "speaker_analysis"
	TypeSpeakerSegment   = "speaker_segment"
	TypeSpeakerUtterance = "speaker_utterance"
	TypeSessionEnd       = "session_end"

	// Bundle filenames keep one visible item per meeting while preserving the
	// machine event stream needed for recovery, routing, and reprocessing.
	BundleDocumentName        = "meeting.md"
	BundleInternalDirName     = ".samantha"
	BundleEventsName          = "events.jsonl"
	BundleSpeakerAnalysisName = "speaker-analysis.json"
	BundleAudioName           = "audio.wav"
)

// Event is one JSONL record. OffsetMs is milliseconds since session start.
type Event struct {
	Type         string  `json:"type"`
	TS           string  `json:"ts"`                // RFC3339
	OffsetMs     int64   `json:"offset_ms"`         // from session start
	ID           string  `json:"id,omitempty"`      // stable within this meeting
	Text         string  `json:"text,omitempty"`    // utterance / note body
	Label        string  `json:"label,omitempty"`   // bookmark or speaker label
	Message      string  `json:"message,omitempty"` // error text
	STT          string  `json:"stt,omitempty"`     // session_start only
	Desc         string  `json:"description,omitempty"`
	Status       string  `json:"status,omitempty"`
	Artifact     string  `json:"artifact,omitempty"`
	AudioFile    string  `json:"audio_file,omitempty"`
	StartMS      int64   `json:"start_ms,omitempty"`
	EndMS        int64   `json:"end_ms,omitempty"`
	Confidence   float32 `json:"confidence,omitempty"`
	State        string  `json:"state,omitempty"`
	Timing       string  `json:"timing,omitempty"`
	SpeakerCount int     `json:"speaker_count,omitempty"`
}

// TranscriptRecord is the timestamp estimate retained for post-capture
// attribution. It contains text only; no PCM or embeddings are persisted.
type TranscriptRecord struct {
	ID      string
	StartMS int64
	EndMS   int64
	Text    string
}

// SpeakerSegment and SpeakerUtterance are persistence-only projections of the
// analysis domain, keeping this log package independent from meeting policy.
type SpeakerSegment struct {
	ID         string
	StartMS    int64
	EndMS      int64
	Label      string
	Confidence float32
	State      string
}

type SpeakerUtterance struct {
	TranscriptRecord
	Speaker    string
	Confidence float32
	State      string
}

type SpeakerAnalysis struct {
	Status     string
	Error      string
	Artifact   string
	AudioFile  string
	Segments   []SpeakerSegment
	Utterances []SpeakerUtterance
}

// Summary describes a finished recording, for the file trailer and the
// command's console/JSON summaries.
type Summary struct {
	Description         string    `json:"description"`
	Bundle              string    `json:"bundle,omitempty"` // one meeting-level directory
	File                string    `json:"file"`             // canonical human document
	JSONLFile           string    `json:"jsonl_file"`       // structured event stream
	StartedAt           time.Time `json:"started_at"`
	EndedAt             time.Time `json:"ended_at"`
	DurationSeconds     int64     `json:"duration_seconds"`
	Utterances          int       `json:"utterances"`
	Notes               int       `json:"notes"`
	Bookmarks           int       `json:"bookmarks"`
	Errors              int       `json:"errors"`
	SpeakerStatus       string    `json:"speaker_status,omitempty"`
	SpeakerCount        int       `json:"speaker_count,omitempty"`
	SpeakerAnalysisFile string    `json:"speaker_analysis_file,omitempty"`
	AudioFile           string    `json:"audio_file,omitempty"`
	SpeakerError        string    `json:"speaker_error,omitempty"`
}

// Duration reports the recording length.
func (s Summary) Duration() time.Duration { return s.EndedAt.Sub(s.StartedAt) }

// Writer is the crash-safe document/event sink for one meeting recording.
type Writer struct {
	mu                 sync.Mutex
	log                *os.File
	jsonl              *os.File
	path               string
	jsonlPath          string
	bundlePath         string
	description        string
	sttLabel           string
	started            time.Time
	utterances         int
	notes              int
	bookmarks          int
	errors             int
	transcripts        []TranscriptRecord
	lastUtteranceEndMS int64
	speakerAnalysis    SpeakerAnalysis
	speakerCount       int
	closed             bool
	summary            Summary // last Close result; returned again on idempotent Close
}

// CreateBundle creates one private meeting directory with a canonical
// meeting.md document and hidden machine event stream. bundlePath must not
// already exist, preventing accidental mixing of two recordings.
func CreateBundle(bundlePath, description, sttLabel string) (*Writer, error) {
	if err := os.Mkdir(bundlePath, 0o700); err != nil {
		return nil, fmt.Errorf("meetinglog: create bundle %s: %w", bundlePath, err)
	}
	internalDir := filepath.Join(bundlePath, BundleInternalDirName)
	if err := os.Mkdir(internalDir, 0o700); err != nil {
		_ = os.Remove(bundlePath)
		return nil, fmt.Errorf("meetinglog: create bundle internals %s: %w", internalDir, err)
	}
	documentPath := filepath.Join(bundlePath, BundleDocumentName)
	eventsPath := filepath.Join(internalDir, BundleEventsName)
	w, err := createAt(documentPath, eventsPath, bundlePath, description, sttLabel)
	if err != nil {
		_ = os.RemoveAll(bundlePath)
		return nil, err
	}
	return w, nil
}

func createAt(path, jsonlPath, bundlePath, description, sttLabel string) (*Writer, error) {
	// 0o600: meeting transcripts are private (credentials, personal speech).
	logF, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("meetinglog: create %s: %w", path, err)
	}
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
		bundlePath:  bundlePath,
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

// Path returns the canonical human-readable meeting document.
func (w *Writer) Path() string { return w.path }

// JSONLPath returns the structured event stream path.
func (w *Writer) JSONLPath() string { return w.jsonlPath }

// BundlePath returns the meeting-level directory.
func (w *Writer) BundlePath() string { return w.bundlePath }

// StartedAt returns when the session opened.
func (w *Writer) StartedAt() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.started
}

// Transcripts returns an owned snapshot for post-capture speaker attribution.
func (w *Writer) Transcripts() []TranscriptRecord {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]TranscriptRecord(nil), w.transcripts...)
}

// OnUtterance implements listen.Sink.
func (w *Writer) OnUtterance(u listen.Utterance) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	endMS := u.At.Sub(w.started).Milliseconds()
	if endMS < 0 {
		endMS = 0
	}
	startMS := endMS - estimatedSpeechDuration(u.Text).Milliseconds()
	if startMS < w.lastUtteranceEndMS {
		startMS = w.lastUtteranceEndMS
	}
	if startMS < 0 {
		startMS = 0
	}
	if endMS <= startMS {
		endMS = startMS + 1
	}
	record := TranscriptRecord{
		ID:      fmt.Sprintf("utterance-%d", w.utterances+1),
		StartMS: startMS,
		EndMS:   endMS,
		Text:    u.Text,
	}
	if err := w.writeLog(fmt.Sprintf("[%s] %s\n", u.At.Format("15:04:05"), u.Text)); err != nil {
		return err
	}
	if err := w.writeEvent(Event{
		Type: TypeUtterance, ID: record.ID, Text: u.Text,
		TS: u.At.Format(time.RFC3339), StartMS: startMS, EndMS: endMS, Timing: "estimated",
	}); err != nil {
		return err
	}
	w.transcripts = append(w.transcripts, record)
	w.lastUtteranceEndMS = endMS
	w.utterances++
	return nil
}

func estimatedSpeechDuration(text string) time.Duration {
	words := len(strings.Fields(text))
	if words < 1 {
		words = 1
	}
	d := time.Duration(words) * 350 * time.Millisecond
	if d < 800*time.Millisecond {
		return 800 * time.Millisecond
	}
	if d > 15*time.Second {
		return 15 * time.Second
	}
	return d
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

// WriteSpeakerAnalysis appends post-capture status, timeline, and attributed
// utterances. Historical transcript lines remain untouched; the additive
// section and event types make fallback/reprocessing explicit.
func (w *Writer) WriteSpeakerAnalysis(analysis SpeakerAnalysis) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return fmt.Errorf("meetinglog: writer closed before speaker analysis")
	}

	speakers := make(map[string]struct{})
	for _, segment := range analysis.Segments {
		if segment.Label != "" && segment.Label != "unknown" {
			speakers[segment.Label] = struct{}{}
		}
	}
	w.speakerAnalysis = analysis
	w.speakerCount = len(speakers)

	var section strings.Builder
	section.WriteString("\n# Speaker analysis: ")
	section.WriteString(analysis.Status)
	if len(speakers) > 0 {
		noun := "speakers"
		if len(speakers) == 1 {
			noun = "speaker"
		}
		fmt.Fprintf(&section, " (%d %s)", len(speakers), noun)
	}
	if analysis.Error != "" {
		section.WriteString(" — ")
		section.WriteString(analysis.Error)
	}
	section.WriteString("\n")
	if analysis.Artifact != "" {
		fmt.Fprintf(&section, "# Speaker analysis file: %s\n", analysis.Artifact)
	}
	if analysis.AudioFile != "" {
		fmt.Fprintf(&section, "# Meeting audio: %s\n", analysis.AudioFile)
	}
	if len(analysis.Segments) > 0 {
		section.WriteString("# Speaker timeline\n")
		for _, segment := range analysis.Segments {
			fmt.Fprintf(&section, "[%s] %s\n", formatOffsetRange(segment.StartMS, segment.EndMS), segment.Label)
		}
	}
	if len(analysis.Utterances) > 0 {
		section.WriteString("# Speaker-attributed transcript\n")
		for _, utterance := range analysis.Utterances {
			fmt.Fprintf(&section, "[%s] %s: %s\n",
				formatOffsetRange(utterance.StartMS, utterance.EndMS), utterance.Speaker, utterance.Text)
		}
	}
	if err := w.writeLog(section.String()); err != nil {
		return err
	}

	statusEvent := Event{
		Type: TypeSpeakerAnalysis, Status: analysis.Status, Message: analysis.Error,
		Artifact: analysis.Artifact, AudioFile: analysis.AudioFile, SpeakerCount: len(speakers),
	}
	if err := w.writeEvent(statusEvent); err != nil {
		return err
	}
	for _, segment := range analysis.Segments {
		if err := w.writeEvent(Event{
			Type: TypeSpeakerSegment, ID: segment.ID, Label: segment.Label,
			StartMS: segment.StartMS, EndMS: segment.EndMS,
			Confidence: segment.Confidence, State: segment.State,
			TS: w.started.Add(time.Duration(segment.StartMS) * time.Millisecond).Format(time.RFC3339),
		}); err != nil {
			return err
		}
	}
	for _, utterance := range analysis.Utterances {
		if err := w.writeEvent(Event{
			Type: TypeSpeakerUtterance, ID: utterance.ID, Text: utterance.Text,
			Label: utterance.Speaker, StartMS: utterance.StartMS, EndMS: utterance.EndMS,
			Confidence: utterance.Confidence, State: utterance.State, Timing: "estimated",
			TS: w.started.Add(time.Duration(utterance.EndMS) * time.Millisecond).Format(time.RFC3339),
		}); err != nil {
			return err
		}
	}
	return nil
}

func formatOffsetRange(startMS, endMS int64) string {
	format := func(ms int64) string {
		if ms < 0 {
			ms = 0
		}
		d := time.Duration(ms) * time.Millisecond
		return fmt.Sprintf("%02d:%02d:%02d.%03d",
			int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60, int(d.Milliseconds())%1000)
	}
	return format(startMS) + "–" + format(endMS)
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
		Description:         w.description,
		Bundle:              w.bundlePath,
		File:                w.path,
		JSONLFile:           w.jsonlPath,
		StartedAt:           w.started,
		EndedAt:             time.Now(),
		Utterances:          w.utterances,
		Notes:               w.notes,
		Bookmarks:           w.bookmarks,
		Errors:              w.errors,
		SpeakerStatus:       w.speakerAnalysis.Status,
		SpeakerCount:        w.speakerCount,
		SpeakerAnalysisFile: w.speakerAnalysis.Artifact,
		AudioFile:           w.speakerAnalysis.AudioFile,
		SpeakerError:        w.speakerAnalysis.Error,
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
		// Preserve sub-second ordering between short/test meetings while staying
		// RFC3339-compatible for existing readers.
		e.TS = now.Format(time.RFC3339Nano)
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
