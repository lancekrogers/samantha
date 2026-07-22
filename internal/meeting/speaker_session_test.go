package meeting

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/lancekrogers/samantha/internal/listen"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/internal/speaker"
)

type fakeCaptureSubscriber struct {
	mu     sync.Mutex
	chunks chan []float32
	closed bool
}

func (f *fakeCaptureSubscriber) Subscribe(int) (int, <-chan []float32) {
	f.chunks = make(chan []float32, 8)
	return 7, f.chunks
}

func (f *fakeCaptureSubscriber) Unsubscribe(id int) {
	if id != 7 {
		panic("unexpected subscription id")
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed {
		close(f.chunks)
		f.closed = true
	}
}

func (f *fakeCaptureSubscriber) publish(samples []float32) {
	f.chunks <- samples
}

type recordingSpeakerAnalyzer struct {
	sampleCount int
	timeline    speaker.Timeline
}

func (a *recordingSpeakerAnalyzer) Finalize(_ context.Context, samples []float32) (speaker.Timeline, error) {
	a.sampleCount = len(samples)
	return a.timeline, nil
}

func TestSpeakerSessionCollectsFinalizesAndPersists(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "planning.log")
	writer, err := meetinglog.Create(logPath, "Planning", "fake")
	if err != nil {
		t.Fatal(err)
	}
	if err := writer.OnUtterance(listen.Utterance{
		Text: "hello from the first speaker", At: writer.StartedAt().Add(3 * time.Second),
	}); err != nil {
		t.Fatal(err)
	}

	capture := &fakeCaptureSubscriber{}
	analyzer := &recordingSpeakerAnalyzer{timeline: speaker.Timeline{Observations: []speaker.Observation{{
		SegmentID: "diarization-1", StartMS: 0, EndMS: 4000,
		Label: "speaker-1", State: speaker.StateStable, Source: speaker.SourceRecording,
	}}}}
	session, err := NewSpeakerSession(capture, analyzer, writer, logPath, true)
	if err != nil {
		t.Fatal(err)
	}
	capture.publish([]float32{0.1, 0.2})
	capture.publish([]float32{0.3})

	result, err := session.Finalize(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != AnalysisComplete || result.SpeakerCount != 1 || analyzer.sampleCount != 3 {
		t.Fatalf("result = %+v, samples = %d", result, analyzer.sampleCount)
	}
	if result.Artifact == "" || result.AudioFile == "" {
		t.Fatalf("artifact paths missing: %+v", result)
	}
	for _, path := range []string{result.Artifact, result.AudioFile} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm()&0o077 != 0 {
			t.Fatalf("%s mode = %o, want private", path, info.Mode().Perm())
		}
	}

	summary, err := writer.Close()
	if err != nil {
		t.Fatal(err)
	}
	if summary.SpeakerStatus != string(AnalysisComplete) || summary.SpeakerCount != 1 || summary.SpeakerAnalysisFile != result.Artifact {
		t.Fatalf("summary speaker metadata = %+v", summary)
	}
	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Speaker analysis: complete (1 speaker)", "# Speaker timeline", "speaker-1: hello from the first speaker"} {
		if !strings.Contains(string(logData), want) {
			t.Fatalf("meeting log missing %q:\n%s", want, logData)
		}
	}
	events, err := ReadEvents(writer.JSONLPath())
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, event := range events {
		kinds[event.Type]++
	}
	for _, kind := range []string{meetinglog.TypeSpeakerAnalysis, meetinglog.TypeSpeakerSegment, meetinglog.TypeSpeakerUtterance} {
		if kinds[kind] == 0 {
			t.Fatalf("missing %s event: %v", kind, kinds)
		}
	}

	// Finalization is idempotent and must not append duplicate analysis blocks.
	if again, err := session.Finalize(context.Background()); err != nil || again.Artifact != result.Artifact {
		t.Fatalf("second finalize = %+v, %v", again, err)
	}
}

func TestAnalyzeRecordingEnabledWithoutPCMIsVisibleError(t *testing.T) {
	got := AnalyzeRecording(context.Background(), &recordingSpeakerAnalyzer{}, nil)
	if got.Status != AnalysisError || !strings.Contains(got.Error, "no meeting audio") {
		t.Fatalf("result = %+v", got)
	}
}
