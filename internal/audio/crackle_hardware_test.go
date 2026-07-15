package audio

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestHardwarePlayStreamNoSoftwareCrackle is an opt-in integration test that
// opens the real default playback device, plays a speech-like 24 kHz utterance
// through PlayStream (full pump + native-rate resample + callback path), and
// scores the captured device-output.wav for software crackle signatures.
//
// This cannot prove CoreAudio itself is clean — only that Samantha's
// pre-backend PCM and callback delivery contain no underrun holes or click
// impulses. Run on an affected machine with:
//
//	SAMANTHA_HARDWARE_AUDIO=1 go test ./internal/audio -run Hardware -v
//
// Capture artifacts land under the test temp directory when the assertion
// fails so the debug bundle can be inspected with the audio corruption runbook.
func TestHardwarePlayStreamNoSoftwareCrackle(t *testing.T) {
	if os.Getenv("SAMANTHA_HARDWARE_AUDIO") == "" {
		t.Skip("set SAMANTHA_HARDWARE_AUDIO=1 to run hardware playback crackle test")
	}

	root := t.TempDir()
	if err := SetDebugAudioDir(root); err != nil {
		t.Fatalf("SetDebugAudioDir: %v", err)
	}
	t.Cleanup(func() { _ = SetDebugAudioDir("") })

	player := NewPlayer()
	t.Cleanup(func() { _ = player.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	const (
		inRate  = 24_000
		seconds = 2
		chunk   = 2_048
	)
	src := synthSpeechLike(inRate, inRate*seconds)

	stream := NewPCMStream(ctx)
	if err := stream.SetSampleRate(inRate); err != nil {
		t.Fatalf("SetSampleRate: %v", err)
	}
	go func() {
		for off := 0; off < len(src); off += chunk {
			end := off + chunk
			if end > len(src) {
				end = len(src)
			}
			if err := stream.Write(src[off:end]); err != nil {
				stream.CloseWithError(err)
				return
			}
		}
		stream.Close()
	}()

	playback, err := player.PlayStream(ctx, stream)
	if err != nil {
		t.Fatalf("PlayStream: %v", err)
	}

	select {
	case <-playback.Started():
	case <-ctx.Done():
		t.Fatalf("timed out waiting for playback start: %v", ctx.Err())
	}

	select {
	case result := <-playback.Done():
		if result.Err != nil {
			t.Fatalf("playback result error: %v", result.Err)
		}
		if result.Interrupted {
			t.Fatal("playback interrupted unexpectedly")
		}
	case <-ctx.Done():
		t.Fatalf("timed out waiting for playback done: %v", ctx.Err())
	}

	// Allow the debug writer to flush.
	_ = player.Close()
	time.Sleep(100 * time.Millisecond)

	deviceWAV, metaPath, callbacksPath, err := findDebugCapture(root)
	if err != nil {
		t.Fatalf("locate debug capture: %v", err)
	}
	t.Logf("debug capture device wav: %s", deviceWAV)

	samples, rate, err := ReadWAVFloat32(deviceWAV)
	if err != nil {
		t.Fatalf("ReadWAVFloat32(%s): %v", deviceWAV, err)
	}
	if rate <= 0 || len(samples) < rate/4 {
		t.Fatalf("device output too short: rate=%d samples=%d", rate, len(samples))
	}

	metaRaw, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read metadata: %v", err)
	}
	var meta debugAudioMetadata
	if err := json.Unmarshal(metaRaw, &meta); err != nil {
		t.Fatalf("decode metadata: %v", err)
	}
	t.Logf("device=%q requested=%d Hz actual=%d Hz channels=%d",
		meta.DeviceName, meta.RequestedSampleRate, meta.DeviceSampleRate, meta.Channels)
	// Studio Display Speakers and similar multi-channel devices must not be
	// left on a mono client format — that path produced audible crackle while
	// leaving source/callback impulse metrics clean.
	if meta.Channels < 2 {
		t.Fatalf("playback channels = %d, want ≥ 2 (stereo client format for mono TTS upmix)", meta.Channels)
	}

	// Partial callbacks: only the final tail of an utterance may mix audio with
	// trailing silence. Mid-speech partials are underruns.
	partialMids, err := countMidPartialCallbacks(callbacksPath)
	if err != nil {
		t.Fatalf("analyze callbacks: %v", err)
	}
	if partialMids > 0 {
		t.Fatalf("mid-utterance partial callbacks (underruns) = %d; capture at %s", partialMids, filepath.Dir(deviceWAV))
	}

	m := AnalyzeFloat32(samples, CrackleThresholds{})
	t.Logf("crackle metrics: %+v", m)
	if m.HasCrackle(CrackleThresholds{}) {
		t.Fatalf("hardware callback capture reported software crackle: %+v (bundle %s)", m, filepath.Dir(deviceWAV))
	}
}

func findDebugCapture(root string) (deviceWAV, metaPath, callbacksPath string, err error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return "", "", "", err
	}
	for _, e := range entries {
		if !e.IsDir() || !strings.Contains(e.Name(), "-pid") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		deviceWAV = filepath.Join(dir, "device-output.wav")
		metaPath = filepath.Join(dir, "metadata.json")
		callbacksPath = filepath.Join(dir, "callbacks.jsonl")
		if _, statErr := os.Stat(deviceWAV); statErr == nil {
			return deviceWAV, metaPath, callbacksPath, nil
		}
	}
	return "", "", "", os.ErrNotExist
}

// countMidPartialCallbacks returns how many callbacks wrote some audio and
// some silence in the same period, excluding the final callback (the natural
// short tail of an utterance).
func countMidPartialCallbacks(path string) (int, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(raw)), "\n")
	var partialIdx []int
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var cb debugCallbackMetadata
		if err := json.Unmarshal([]byte(line), &cb); err != nil {
			return 0, err
		}
		if cb.WrittenFrames > 0 && cb.SilenceFrames > 0 {
			partialIdx = append(partialIdx, i)
		}
	}
	if len(partialIdx) == 0 {
		return 0, nil
	}
	// Drop the last partial if it is the final callback line — expected tail.
	lastLine := len(lines) - 1
	for lastLine >= 0 && strings.TrimSpace(lines[lastLine]) == "" {
		lastLine--
	}
	mid := 0
	for _, idx := range partialIdx {
		if idx != lastLine {
			mid++
		}
	}
	return mid, nil
}
