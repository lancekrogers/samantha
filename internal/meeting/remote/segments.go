package remote

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/pkg/voiceagent/audio"
)

// segmentsDirName holds the uploaded PCM inside the bundle's hidden internal
// directory, so a bundle looks the same from the outside as a desktop one.
const segmentsDirName = "segments"

// segmentSuffix and partSuffix keep in-flight writes out of the index: only a
// fully written, renamed file counts as received.
const (
	segmentSuffix = ".pcm"
	partSuffix    = ".part"
)

// assembleChunkSamples bounds how many samples are converted at once while
// streaming segments into the WAV, keeping memory flat for a long meeting.
const assembleChunkSamples = 16000

// segmentStore persists uploaded PCM as one file per sequence number. The
// directory listing *is* the index: it survives a serve restart, makes a
// duplicate upload a no-op by construction, and needs no separate bookkeeping
// that could disagree with what is actually on disk.
type segmentStore struct {
	dir string
	// gapSamples is the nominal segment length, used to substitute silence
	// for audio that never arrived.
	gapSamples int

	mu sync.Mutex
}

func newSegmentStore(bundlePath string, segmentSeconds int) (*segmentStore, error) {
	dir := filepath.Join(bundlePath, meetinglog.BundleInternalDirName, segmentsDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("meeting: create segment dir: %w", err)
	}
	if segmentSeconds <= 0 {
		segmentSeconds = DefaultSegmentSeconds
	}
	return &segmentStore{dir: dir, gapSamples: segmentSeconds * audio.SampleRate}, nil
}

func (s *segmentStore) path(seq int64) string {
	return filepath.Join(s.dir, fmt.Sprintf("%09d%s", seq, segmentSuffix))
}

// Put stores one segment. Re-uploading a sequence already on disk is a no-op,
// so a client may retry any segment it did not see acknowledged. The write is
// staged and renamed, so a crashed process never leaves a half-written segment
// in the index. (The rename is not fsynced: a power loss can still land the
// rename without the data, which the client's re-push handles.)
func (s *segmentStore) Put(seq int64, data []byte) error {
	if seq < 0 {
		return fmt.Errorf("%w: negative sequence %d", ErrBadSegment, seq)
	}
	if len(data) == 0 {
		return fmt.Errorf("%w: empty body", ErrBadSegment)
	}
	if len(data)%2 != 0 {
		return fmt.Errorf("%w: %d bytes is not whole 16-bit samples", ErrBadSegment, len(data))
	}
	if len(data) > MaxSegmentBytes {
		return fmt.Errorf("%w: %d bytes exceeds the %d byte cap", ErrBadSegment, len(data), MaxSegmentBytes)
	}
	if seq > MaxSegmentSeq {
		// Sequence numbers drive loops over [0, seq] at finalize time, so an
		// absurd one is a denial of service, not a long meeting.
		return fmt.Errorf("%w: sequence %d exceeds the %d cap", ErrBadSegment, seq, MaxSegmentSeq)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	final := s.path(seq)
	if _, err := os.Stat(final); err == nil {
		return nil
	}
	staged := final + partSuffix
	if err := os.WriteFile(staged, data, 0o600); err != nil {
		return fmt.Errorf("meeting: stage segment %d: %w", seq, err)
	}
	if err := os.Rename(staged, final); err != nil {
		_ = os.Remove(staged)
		return fmt.Errorf("meeting: commit segment %d: %w", seq, err)
	}
	return nil
}

// received reads the index off disk. Called on stop and finalize only, never
// per upload.
func (s *segmentStore) received() (map[int64]bool, int64, error) {
	entries, err := os.ReadDir(s.dir)
	if errors.Is(err, os.ErrNotExist) {
		// Purged after a successful finalize: no segments is the truth, not
		// an error.
		return map[int64]bool{}, -1, nil
	}
	if err != nil {
		return nil, -1, fmt.Errorf("meeting: read segment dir: %w", err)
	}
	have := make(map[int64]bool, len(entries))
	highest := int64(-1)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, segmentSuffix) {
			continue
		}
		seq, err := strconv.ParseInt(strings.TrimSuffix(name, segmentSuffix), 10, 64)
		if err != nil {
			continue
		}
		have[seq] = true
		if seq > highest {
			highest = seq
		}
	}
	return have, highest, nil
}

// Missing reports which sequences in [0, lastSeq] never arrived, so the client
// can re-push exactly those before stopping again.
func (s *segmentStore) Missing(lastSeq int64) ([]int64, error) {
	have, _, err := s.received()
	if err != nil {
		return nil, err
	}
	var missing []int64
	for seq := int64(0); seq <= lastSeq; seq++ {
		if !have[seq] {
			missing = append(missing, seq)
		}
	}
	return missing, nil
}

// Highest reports the largest sequence on disk, or -1 when none arrived. It is
// how an interrupted meeting works out how much audio it actually has.
func (s *segmentStore) Highest() (int64, error) {
	_, highest, err := s.received()
	return highest, err
}

// sampleWriter is the sink Assemble streams into — the meeting package's WAV
// writer in production, a recorder in tests.
type sampleWriter interface {
	Write(samples []float32) error
}

// Gap is a run of audio that never arrived, located in the assembled
// recording rather than in sequence space — that is what a reader, a bookmark
// offset, and a diarization timestamp all need.
type Gap struct {
	FirstSeq   int64
	Segments   int64
	OffsetMs   int64
	DurationMs int64
}

// Assemble streams segments 0..lastSeq into dst in sequence order. Missing
// segments are replaced with silence of the nominal segment length rather than
// skipped: dropping them would slide every later sample earlier, desyncing the
// client's own bookmark and idea-span offsets — which are authoritative — from
// the audio they point at.
//
// Only one segment is held in memory at a time, so a three-hour meeting costs
// no more than a five-second one.
func (s *segmentStore) Assemble(ctx context.Context, dst sampleWriter, lastSeq int64) ([]Gap, error) {
	have, _, err := s.received()
	if err != nil {
		return nil, err
	}
	samples := make([]float32, 0, assembleChunkSamples)
	silence := make([]float32, s.gapSamples)
	var gaps []Gap
	written := int64(0)

	for seq := int64(0); seq <= lastSeq; seq++ {
		if err := ctx.Err(); err != nil {
			return gaps, err
		}
		if !have[seq] {
			if err := dst.Write(silence); err != nil {
				return gaps, fmt.Errorf("meeting: pad gap at segment %d: %w", seq, err)
			}
			gaps = appendGap(gaps, seq, written, int64(s.gapSamples))
			written += int64(s.gapSamples)
			continue
		}
		raw, err := os.ReadFile(s.path(seq))
		if err != nil {
			return gaps, fmt.Errorf("meeting: read segment %d: %w", seq, err)
		}
		if err := writePCM(dst, raw, samples); err != nil {
			return gaps, fmt.Errorf("meeting: assemble segment %d: %w", seq, err)
		}
		written += int64(len(raw) / 2)
	}
	return gaps, nil
}

// appendGap extends the previous run when this segment continues it, so a
// twenty-segment dropout is reported as one gap rather than twenty.
func appendGap(gaps []Gap, seq, offsetSamples, gapSamples int64) []Gap {
	if n := len(gaps); n > 0 && gaps[n-1].FirstSeq+gaps[n-1].Segments == seq {
		gaps[n-1].Segments++
		gaps[n-1].DurationMs += samplesToMs(gapSamples)
		return gaps
	}
	return append(gaps, Gap{
		FirstSeq: seq, Segments: 1,
		OffsetMs: samplesToMs(offsetSamples), DurationMs: samplesToMs(gapSamples),
	})
}

func samplesToMs(samples int64) int64 { return samples * 1000 / int64(audio.SampleRate) }

// writePCM converts little-endian s16 to float32 in bounded batches, reusing
// scratch so a long meeting does not churn the heap.
func writePCM(dst sampleWriter, raw []byte, scratch []float32) error {
	scratch = scratch[:0]
	for i := 0; i+1 < len(raw); i += 2 {
		v := int16(binary.LittleEndian.Uint16(raw[i:]))
		scratch = append(scratch, float32(v)/float32(0x7FFF))
		if len(scratch) == cap(scratch) {
			if err := dst.Write(scratch); err != nil {
				return err
			}
			scratch = scratch[:0]
		}
	}
	if len(scratch) == 0 {
		return nil
	}
	return dst.Write(scratch)
}

// purge drops the raw segments once they are safely inside audio.wav. The
// assembled WAV carries the same PCM, so keeping both only doubles disk use.
func (s *segmentStore) purge() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := os.RemoveAll(s.dir); err != nil {
		return fmt.Errorf("meeting: purge segments: %w", err)
	}
	return nil
}
