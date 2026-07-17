package audio

import (
	"encoding/binary"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestPlayerDebugRecorderCapturesSourceDeviceAndTiming(t *testing.T) {
	root := t.TempDir()
	recorder, err := newPlayerDebugRecorder(root, "Test Speakers", 24_000, 44_100, 1)
	if err != nil {
		t.Fatalf("newPlayerDebugRecorder() error = %v", err)
	}

	recorder.captureSource(24_000, []float32{0.25, -0.25})
	callback := make([]byte, 4)
	positive := int16(math.MaxInt16 / 2)
	negative := -positive
	binary.LittleEndian.PutUint16(callback[0:2], uint16(positive))
	binary.LittleEndian.PutUint16(callback[2:4], uint16(negative))
	recorder.captureCallback(callback, 2, 2)
	recorder.close()

	source, sourceRate, err := ReadWAVFloat32(filepath.Join(recorder.dir, "source-0001-24000hz.wav"))
	if err != nil {
		t.Fatalf("ReadWAVFloat32(source) error = %v", err)
	}
	if sourceRate != 24_000 || len(source) != 2 {
		t.Fatalf("source = %d Hz/%d samples, want 24000 Hz/2 samples", sourceRate, len(source))
	}
	device, deviceRate, err := ReadWAVFloat32(filepath.Join(recorder.dir, "device-output.wav"))
	if err != nil {
		t.Fatalf("ReadWAVFloat32(device) error = %v", err)
	}
	if deviceRate != 44_100 || len(device) != 2 {
		t.Fatalf("device = %d Hz/%d samples, want 44100 Hz/2 samples", deviceRate, len(device))
	}

	encoded, err := os.ReadFile(filepath.Join(recorder.dir, "callbacks.jsonl"))
	if err != nil {
		t.Fatalf("read callback metadata: %v", err)
	}
	var callbackMeta debugCallbackMetadata
	if err := json.Unmarshal(encoded, &callbackMeta); err != nil {
		t.Fatalf("decode callback metadata: %v", err)
	}
	if callbackMeta.RequestedFrames != 2 || callbackMeta.WrittenFrames != 2 || callbackMeta.SilenceFrames != 0 {
		t.Fatalf("callback metadata = %+v", callbackMeta)
	}
}

func TestRecordDebugSynthesisIncludesOriginalAndPreparedText(t *testing.T) {
	root := t.TempDir()
	if err := SetDebugAudioDir(root); err != nil {
		t.Fatalf("SetDebugAudioDir() error = %v", err)
	}
	t.Cleanup(func() { _ = SetDebugAudioDir("") })

	RecordDebugSynthesis("kokoro", "It wasn't.", "It wasnt.")
	encoded, err := os.ReadFile(filepath.Join(root, "syntheses.jsonl"))
	if err != nil {
		t.Fatalf("read syntheses: %v", err)
	}
	var event debugSynthesisMetadata
	if err := json.Unmarshal(encoded, &event); err != nil {
		t.Fatalf("decode synthesis metadata: %v", err)
	}
	if event.Provider != "kokoro" || event.Original != "It wasn't." || event.Prepared != "It wasnt." {
		t.Fatalf("synthesis event = %+v", event)
	}
	if time.Since(event.CreatedAt) > time.Minute {
		t.Fatalf("synthesis timestamp is stale: %v", event.CreatedAt)
	}
}
