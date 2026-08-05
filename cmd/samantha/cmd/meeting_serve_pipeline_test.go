//go:build !integration

package cmd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/internal/stt"
)

// scriptedSession replays one session's worth of STT events.
type scriptedSession struct {
	events chan stt.Event
	closed bool
}

func (s *scriptedSession) Events() <-chan stt.Event { return s.events }
func (s *scriptedSession) Close() error             { s.closed = true; return nil }

// scriptedSTT hands out one scripted session per turn and advances a fake
// replay position, standing in for the sherpa stack the real pipeline builds.
type scriptedSTT struct {
	turns    [][]stt.Event
	started  int
	startErr error
	source   *fakeReplaySource
	sessions []*scriptedSession
	// perTurn is how far the replay advances each time a session runs; zero
	// models a stuck source.
	perTurn time.Duration
}

func (p *scriptedSTT) Available() bool { return true }

func (p *scriptedSTT) Start(context.Context) (stt.Session, error) {
	if p.startErr != nil {
		return nil, p.startErr
	}
	session := &scriptedSession{events: make(chan stt.Event, 4)}
	if p.started < len(p.turns) {
		for _, event := range p.turns[p.started] {
			session.events <- event
		}
	}
	close(session.events)
	p.sessions = append(p.sessions, session)
	p.started++
	p.source.advance(p.perTurn)
	return session, nil
}

// fakeReplaySource satisfies the progressSource seam without any audio.
type fakeReplaySource struct {
	elapsed time.Duration
	total   time.Duration
}

func (f *fakeReplaySource) Exhausted() bool        { return f.elapsed >= f.total }
func (f *fakeReplaySource) Elapsed() time.Duration { return f.elapsed }
func (f *fakeReplaySource) advance(d time.Duration) {
	f.elapsed += d
	if f.elapsed > f.total {
		f.elapsed = f.total
	}
}

func testBundleWriter(t *testing.T) *meetinglog.Writer {
	t.Helper()
	writer, err := meetinglog.CreateBundle(filepath.Join(t.TempDir(), "replay.meeting"), "Replay", "fake")
	if err != nil {
		t.Fatalf("CreateBundle() error = %v", err)
	}
	t.Cleanup(func() { _, _ = writer.Close() })
	return writer
}

func finalTranscript(text string) []stt.Event {
	return []stt.Event{stt.FinalTranscript{Text: text}}
}

// TestReplayTranscribeStampsUtterancesByAudioPosition is the reason this loop
// exists: transcription runs long after the meeting, so an utterance must be
// timestamped by where it sits in the recording, not by when the transcriber
// happened to reach it.
func TestReplayTranscribeStampsUtterancesByAudioPosition(t *testing.T) {
	source := &fakeReplaySource{total: 30 * time.Second}
	provider := &scriptedSTT{
		source:  source,
		perTurn: 10 * time.Second,
		turns: [][]stt.Event{
			finalTranscript("first thing"),
			{stt.Timeout{}},
			finalTranscript("third thing"),
		},
	}
	writer := testBundleWriter(t)
	startedAt := writer.StartedAt()

	if err := replayTranscribe(context.Background(), source, provider, writer); err != nil {
		t.Fatalf("replayTranscribe() error = %v", err)
	}
	if provider.started != 3 {
		t.Errorf("ran %d sessions, want 3 (one per chunk of the recording)", provider.started)
	}
	for i, session := range provider.sessions {
		if !session.closed {
			t.Errorf("session %d was never closed — a long meeting would leak one per turn", i)
		}
	}

	records := writer.Transcripts()
	if len(records) != 2 {
		t.Fatalf("wrote %d utterances, want 2 — a timeout is silence, not content", len(records))
	}
	// Offsets are meeting-relative and inside the recording's length, which a
	// wall-clock stamp taken at transcription time would not be.
	for _, record := range records {
		if record.EndMS > int64(30*time.Second/time.Millisecond) {
			t.Errorf("utterance %q ends at %dms, past the 30s recording", record.Text, record.EndMS)
		}
	}
	if records[0].EndMS >= records[1].EndMS {
		t.Errorf("utterances are not in recording order: %+v", records)
	}
	if startedAt.IsZero() {
		t.Error("bundle has no start time to anchor offsets to")
	}
}

func TestReplayTranscribeStopsOnAStuckSource(t *testing.T) {
	source := &fakeReplaySource{total: time.Minute}
	// perTurn zero: the provider consumes no audio, so the loop must give up
	// rather than spin forever against a source that cannot advance.
	provider := &scriptedSTT{source: source, perTurn: 0, turns: [][]stt.Event{finalTranscript("hello")}}

	done := make(chan error, 1)
	go func() { done <- replayTranscribe(context.Background(), source, provider, testBundleWriter(t)) }()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("replayTranscribe() error = %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("replayTranscribe spun on a source that never advanced")
	}
}

func TestReplayTranscribeErrorPaths(t *testing.T) {
	tests := []struct {
		name     string
		provider func(*fakeReplaySource) *scriptedSTT
		wantErr  string
	}{
		{
			name: "session cannot start",
			provider: func(s *fakeReplaySource) *scriptedSTT {
				return &scriptedSTT{source: s, perTurn: time.Second, startErr: errors.New("models missing")}
			},
			wantErr: "models missing",
		},
		{
			name: "session fails mid-recording",
			provider: func(s *fakeReplaySource) *scriptedSTT {
				return &scriptedSTT{source: s, perTurn: time.Second, turns: [][]stt.Event{
					{stt.Failure{Err: errors.New("decoder exploded")}},
				}}
			},
			wantErr: "decoder exploded",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			source := &fakeReplaySource{total: 10 * time.Second}
			err := replayTranscribe(context.Background(), source, tt.provider(source), testBundleWriter(t))
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, tt.wantErr)
			}
		})
	}
}

func TestReplayTranscribeHonorsContextCancellation(t *testing.T) {
	source := &fakeReplaySource{total: time.Minute}
	provider := &scriptedSTT{source: source, perTurn: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := replayTranscribe(ctx, source, provider, testBundleWriter(t)); !errors.Is(err, context.Canceled) {
		t.Fatalf("replayTranscribe() error = %v, want context.Canceled", err)
	}
}

// TestReplaySourceOutlivesItsSessions pins the lifetime rule: an STT provider
// closing its session must not end the replay halfway through the meeting.
func TestReplaySourceOutlivesItsSessions(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audio.wav")
	writeTestWAV(t, path, 16000)

	source, err := newReplaySource(path)
	if err != nil {
		t.Fatalf("newReplaySource() error = %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}
	frame, err := source.ReadFrame(context.Background())
	if err != nil {
		t.Fatalf("ReadFrame() after Close error = %v — the replay died with its session", err)
	}
	if len(frame.Samples) == 0 {
		t.Fatal("ReadFrame() after Close returned no audio")
	}
	if source.Elapsed() <= 0 {
		t.Error("Elapsed() did not advance with the audio handed over")
	}
}

// writeTestWAV emits a silent mono 16 kHz WAV of the requested sample count.
func writeTestWAV(t *testing.T, path string, samples int) {
	t.Helper()
	header := []byte{
		'R', 'I', 'F', 'F', 0, 0, 0, 0, 'W', 'A', 'V', 'E',
		'f', 'm', 't', ' ', 16, 0, 0, 0, 1, 0, 1, 0,
		0x80, 0x3E, 0, 0, 0, 0x7D, 0, 0, 2, 0, 16, 0,
		'd', 'a', 't', 'a', 0, 0, 0, 0,
	}
	data := make([]byte, samples*2)
	riffSize := uint32(36 + len(data))
	header[4], header[5], header[6], header[7] = byte(riffSize), byte(riffSize>>8), byte(riffSize>>16), byte(riffSize>>24)
	dataSize := uint32(len(data))
	header[40], header[41], header[42], header[43] = byte(dataSize), byte(dataSize>>8), byte(dataSize>>16), byte(dataSize>>24)
	if err := os.WriteFile(path, append(header, data...), 0o600); err != nil {
		t.Fatal(err)
	}
}
