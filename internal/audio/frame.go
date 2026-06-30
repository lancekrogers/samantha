package audio

import (
	"context"
	"errors"
	"time"
)

// SourceKind identifies where an audio Frame originated so consumers can apply
// the right lifecycle policy: a live microphone streams until canceled, while a
// finite fixture or file ends with an explicit final frame.
type SourceKind string

const (
	SourceLive    SourceKind = "live"
	SourceFixture SourceKind = "fixture"
	SourceFile    SourceKind = "file"
)

// Frame is a typed unit of captured audio plus the lifecycle metadata needed to
// tell "no audio yet" apart from "end of input". A Frame with Final set marks
// the end of a finite source, after any Samples it still carries.
type Frame struct {
	Samples    []float32
	SampleRate int
	Channels   int
	StartedAt  time.Time
	Duration   time.Duration
	Sequence   int64
	SourceKind SourceKind
	Final      bool
}

// Empty reports whether the frame carries no samples.
func (f Frame) Empty() bool { return len(f.Samples) == 0 }

var (
	// ErrNoFrameReady signals a live source has no audio buffered yet. It is
	// non-fatal: callers should poll again. Finite sources never return it; they
	// report end-of-input with a Final frame instead.
	ErrNoFrameReady = errors.New("audio: no frame ready")
	// ErrSourceClosed is returned by ReadFrame once the source has been closed.
	ErrSourceClosed = errors.New("audio: source closed")
)

// FrameSource is the typed audio input contract. ReadFrame returns the next
// Frame or an error, following these semantics:
//
//   - nil error with non-empty Samples: usable audio.
//   - nil error with Final == true: end of input, after any Samples included.
//   - context.Canceled / context.DeadlineExceeded: the caller canceled; return promptly.
//   - ErrNoFrameReady: a live source has no audio buffered yet; poll again.
//   - ErrSourceClosed: ReadFrame was called after Close.
//   - any other error: a source failure that names the failing input.
//
// Live sources (SourceLive) stream until canceled or closed and never set Final.
// Finite sources (SourceFixture, SourceFile) must signal end of input with a
// Final frame rather than letting silence imply finality.
type FrameSource interface {
	ReadFrame(ctx context.Context) (Frame, error)
	Close() error
}
