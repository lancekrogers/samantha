package remote

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/lancekrogers/samantha/internal/audio"
	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// testGapSamples is the stand-in segment length for the tiny fixtures in this
// file, matching the 4-sample segments the tests write.
const testGapSamples = 4

// countingWriter records how audio was handed over, which is what proves
// assembly streams instead of buffering a whole meeting.
type countingWriter struct {
	total    int
	maxBatch int
	values   []float32
	keep     bool
}

func (c *countingWriter) Write(samples []float32) error {
	c.total += len(samples)
	if len(samples) > c.maxBatch {
		c.maxBatch = len(samples)
	}
	if c.keep {
		c.values = append(c.values, samples...)
	}
	return nil
}

func newTestStore(t *testing.T) *segmentStore {
	t.Helper()
	bundle := filepath.Join(t.TempDir(), "test.meeting")
	if err := os.MkdirAll(filepath.Join(bundle, meetinglog.BundleInternalDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := newSegmentStore(bundle, DefaultSegmentSeconds)
	if err != nil {
		t.Fatalf("newSegmentStore() error = %v", err)
	}
	// Tiny fixtures: keep substituted silence the same size as a test segment
	// so gap padding stays proportional to what the test wrote.
	store.gapSamples = testGapSamples
	return store
}

func TestSegmentPutRejectsMalformed(t *testing.T) {
	store := newTestStore(t)
	tests := []struct {
		name string
		seq  int64
		data []byte
	}{
		{"negative sequence", -1, pcm(1, 4)},
		{"empty body", 0, nil},
		{"odd byte count", 0, []byte{0x01, 0x02, 0x03}},
		{"over the size cap", 0, make([]byte, MaxSegmentBytes+2)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if err := store.Put(tt.seq, tt.data); !errors.Is(err, ErrBadSegment) {
				t.Fatalf("Put() error = %v, want ErrBadSegment", err)
			}
		})
	}
}

// TestSegmentPutIsIdempotent covers the client's whole retry story: a dropped
// ack must be safe to retry, and the second upload must not corrupt the first.
func TestSegmentPutIsIdempotent(t *testing.T) {
	store := newTestStore(t)
	original := pcm(1000, 8)
	for i := 0; i < 3; i++ {
		if err := store.Put(4, original); err != nil {
			t.Fatalf("Put() attempt %d error = %v", i, err)
		}
	}
	// A retry carrying different bytes must not rewrite what was acked.
	if err := store.Put(4, pcm(-1000, 8)); err != nil {
		t.Fatalf("conflicting re-Put() error = %v", err)
	}
	raw, err := os.ReadFile(store.path(4))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(original) {
		t.Error("a duplicate upload overwrote the acknowledged segment")
	}
}

// TestSegmentOutOfOrderArrival is the reconnect case: a client that resumes
// mid-outbox uploads whatever it still holds, in whatever order it holds it.
func TestSegmentOutOfOrderArrival(t *testing.T) {
	store := newTestStore(t)
	for _, seq := range []int64{3, 0, 4, 1, 2} {
		if err := store.Put(seq, pcm(int16(seq+1), 4)); err != nil {
			t.Fatalf("Put(%d) error = %v", seq, err)
		}
	}
	missing, err := store.Missing(4)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Fatalf("Missing() = %v, want none", missing)
	}

	sink := &countingWriter{keep: true}
	gaps, err := store.Assemble(context.Background(), sink, 4)
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	if len(gaps) != 0 {
		t.Errorf("gaps = %+v, want none", gaps)
	}
	// Sequence order, not arrival order.
	for i, want := range []int16{1, 2, 3, 4, 5} {
		got := sink.values[i*4]
		if int16(got*0x7FFF) != want {
			t.Fatalf("segment %d assembled at position %d as %v, want %d", i, i*4, got, want)
		}
	}
}

func TestSegmentMissingReportsGaps(t *testing.T) {
	store := newTestStore(t)
	for _, seq := range []int64{0, 1, 4} {
		if err := store.Put(seq, pcm(1, 4)); err != nil {
			t.Fatal(err)
		}
	}
	missing, err := store.Missing(5)
	if err != nil {
		t.Fatal(err)
	}
	want := []int64{2, 3, 5}
	if len(missing) != len(want) {
		t.Fatalf("Missing() = %v, want %v", missing, want)
	}
	for i := range want {
		if missing[i] != want[i] {
			t.Fatalf("Missing() = %v, want %v", missing, want)
		}
	}

	// Assembly tolerates gaps rather than failing: partial audio beats none.
	sink := &countingWriter{}
	gaps, err := store.Assemble(context.Background(), sink, 5)
	if err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	// Two runs: sequences 2-3, then 5.
	if len(gaps) != 2 {
		t.Fatalf("gaps = %+v, want two runs (2-3 and 5)", gaps)
	}
	if gaps[0].FirstSeq != 2 || gaps[0].Segments != 2 {
		t.Errorf("first gap = %+v, want sequences 2-3 coalesced", gaps[0])
	}
	if gaps[1].FirstSeq != 5 || gaps[1].Segments != 1 {
		t.Errorf("second gap = %+v, want sequence 5", gaps[1])
	}
	// Silence stands in for what never arrived, so later audio keeps its
	// position: 3 delivered segments + 3 gap segments, all testGapSamples long.
	if want := 6 * testGapSamples; sink.total != want {
		t.Errorf("assembled %d samples, want %d — gaps must not shift the timeline", sink.total, want)
	}
}

// TestSegmentIndexSurvivesRestart is the kill-and-resume case. The directory
// listing is the index, so a fresh store over the same bundle picks up exactly
// where the dead one left off and assembles byte-identical audio.
func TestSegmentIndexSurvivesRestart(t *testing.T) {
	bundle := filepath.Join(t.TempDir(), "restart.meeting")
	if err := os.MkdirAll(filepath.Join(bundle, meetinglog.BundleInternalDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	before, err := newSegmentStore(bundle, DefaultSegmentSeconds)
	if err != nil {
		t.Fatal(err)
	}
	for seq := int64(0); seq < 3; seq++ {
		if err := before.Put(seq, pcm(int16(seq+1), 32)); err != nil {
			t.Fatal(err)
		}
	}

	// Serve dies here; a new process opens the same bundle.
	after, err := newSegmentStore(bundle, DefaultSegmentSeconds)
	if err != nil {
		t.Fatal(err)
	}
	missing, err := after.Missing(5)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 3 || missing[0] != 3 {
		t.Fatalf("Missing() after restart = %v, want [3 4 5]", missing)
	}
	for seq := int64(3); seq < 6; seq++ {
		if err := after.Put(seq, pcm(int16(seq+1), 32)); err != nil {
			t.Fatal(err)
		}
	}

	resumed := assembleToWAV(t, after, 5)
	// The same segments assembled in one go, for a byte comparison.
	oneShot := newTestStore(t)
	for seq := int64(0); seq < 6; seq++ {
		if err := oneShot.Put(seq, pcm(int16(seq+1), 32)); err != nil {
			t.Fatal(err)
		}
	}
	if reference := assembleToWAV(t, oneShot, 5); string(resumed) != string(reference) {
		t.Error("resumed assembly is not byte-identical to an uninterrupted one")
	}
}

func assembleToWAV(t *testing.T, store *segmentStore, lastSeq int64) []byte {
	t.Helper()
	path := filepath.Join(t.TempDir(), "audio.wav")
	wav, err := audio.NewWAVWriter(path, audio.SampleRate)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Assemble(context.Background(), wav, lastSeq); err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	if err := wav.Close(); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestAssembleHonorsContextCancellation(t *testing.T) {
	store := newTestStore(t)
	for seq := int64(0); seq < 4; seq++ {
		if err := store.Put(seq, pcm(1, 8)); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := store.Assemble(ctx, &countingWriter{}, 3); !errors.Is(err, context.Canceled) {
		t.Fatalf("Assemble() error = %v, want context.Canceled", err)
	}
}

// TestAssembleIsMemoryBounded proves a long meeting costs no more memory than
// a short one: conversion happens in fixed batches and heap use does not track
// the size of the recording.
func TestAssembleIsMemoryBounded(t *testing.T) {
	store := newTestStore(t)
	const segments = 240 // 20 minutes at 5 s per segment
	const samplesPerSegment = 8000
	for seq := int64(0); seq < segments; seq++ {
		if err := store.Put(seq, pcm(int16(seq), samplesPerSegment)); err != nil {
			t.Fatal(err)
		}
	}

	sink := &countingWriter{}
	if _, err := store.Assemble(context.Background(), sink, segments-1); err != nil {
		t.Fatalf("Assemble() error = %v", err)
	}
	if sink.total != segments*samplesPerSegment {
		t.Fatalf("assembled %d samples, want %d", sink.total, segments*samplesPerSegment)
	}
	// The batch cap is the real proof of streaming: no call ever sees more
	// than one fixed chunk, however long the meeting is. (A heap-delta
	// assertion would be noise — a concurrent GC can free a fully buffered
	// meeting before the measurement and pass anyway.)
	if sink.maxBatch > assembleChunkSamples {
		t.Errorf("largest batch = %d samples, want no more than %d", sink.maxBatch, assembleChunkSamples)
	}
}

func TestWritePCMDecodesLittleEndian(t *testing.T) {
	raw := make([]byte, 6)
	binary.LittleEndian.PutUint16(raw[0:], uint16(0x7FFF))
	binary.LittleEndian.PutUint16(raw[2:], uint16(0))
	negativeFullScale := int16(-0x7FFF)
	binary.LittleEndian.PutUint16(raw[4:], uint16(negativeFullScale))

	sink := &countingWriter{keep: true}
	if err := writePCM(sink, raw, make([]float32, 0, 2)); err != nil {
		t.Fatalf("writePCM() error = %v", err)
	}
	want := []float32{1, 0, -1}
	if len(sink.values) != len(want) {
		t.Fatalf("decoded %d samples, want %d", len(sink.values), len(want))
	}
	for i := range want {
		if sink.values[i] != want[i] {
			t.Errorf("sample %d = %v, want %v", i, sink.values[i], want[i])
		}
	}
}

func TestPurgeDropsSegmentsButNotTheBundle(t *testing.T) {
	store := newTestStore(t)
	if err := store.Put(0, pcm(1, 8)); err != nil {
		t.Fatal(err)
	}
	if err := store.purge(); err != nil {
		t.Fatalf("purge() error = %v", err)
	}
	if _, err := os.Stat(store.dir); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("segments dir still present: %v", err)
	}
	// A purged store reports an empty index rather than an error.
	missing, err := store.Missing(1)
	if err != nil {
		t.Fatalf("Missing() after purge error = %v", err)
	}
	if len(missing) != 2 {
		t.Errorf("Missing() = %v, want both sequences", missing)
	}
}
