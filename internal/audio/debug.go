package audio

import (
	"bufio"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

var debugAudioConfig struct {
	sync.RWMutex
	dir       string
	textWrite sync.Mutex
}

// SetDebugAudioDir enables playback capture for players created afterwards.
// An empty path disables capture. The recorder creates one timestamped child
// directory per Player so concurrent Samantha processes never overwrite data.
func SetDebugAudioDir(dir string) error {
	if dir != "" {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return fmt.Errorf("resolve audio debug directory: %w", err)
		}
		if err := os.MkdirAll(abs, 0o700); err != nil {
			return fmt.Errorf("create audio debug directory: %w", err)
		}
		dir = abs
	}
	debugAudioConfig.Lock()
	debugAudioConfig.dir = dir
	debugAudioConfig.Unlock()
	return nil
}

func debugAudioDir() string {
	debugAudioConfig.RLock()
	defer debugAudioConfig.RUnlock()
	return debugAudioConfig.dir
}

// DebugAudioDir returns the active capture root, or an empty string when
// capture is disabled.
func DebugAudioDir() string {
	return debugAudioDir()
}

type debugSynthesisMetadata struct {
	CreatedAt time.Time `json:"created_at"`
	PID       int       `json:"pid"`
	Provider  string    `json:"provider"`
	Original  string    `json:"original_text"`
	Prepared  string    `json:"prepared_text"`
}

// RecordDebugSynthesis stores the exact text entering a TTS provider and any
// provider-specific prepared form. It is intentionally called off the audio
// callback thread.
func RecordDebugSynthesis(provider, original, prepared string) {
	dir := debugAudioDir()
	if dir == "" {
		return
	}
	event, err := json.Marshal(debugSynthesisMetadata{
		CreatedAt: time.Now(),
		PID:       os.Getpid(),
		Provider:  provider,
		Original:  original,
		Prepared:  prepared,
	})
	if err != nil {
		return
	}

	debugAudioConfig.textWrite.Lock()
	defer debugAudioConfig.textWrite.Unlock()
	file, err := os.OpenFile(filepath.Join(dir, "syntheses.jsonl"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	_, _ = file.Write(append(event, '\n'))
	_ = file.Close()
}

type debugAudioEvent struct {
	callback   []byte
	source     []float32
	sampleRate int
	elapsed    time.Duration
	requested  int
	written    int
	sequence   uint64
}

type playerDebugRecorder struct {
	dir       string
	started   time.Time
	events    chan debugAudioEvent
	pool      sync.Pool
	mu        sync.RWMutex
	closed    bool
	closeOnce sync.Once
	wg        sync.WaitGroup
	sequence  atomic.Uint64
	dropped   atomic.Uint64
}

type debugAudioMetadata struct {
	CreatedAt           time.Time `json:"created_at"`
	DeviceName          string    `json:"device_name"`
	RequestedSampleRate int       `json:"requested_sample_rate"`
	DeviceSampleRate    int       `json:"device_sample_rate"`
	Channels            int       `json:"channels"`
	Format              string    `json:"format"`
	PID                 int       `json:"pid"`
}

type debugCallbackMetadata struct {
	Sequence        uint64 `json:"sequence"`
	ElapsedMicros   int64  `json:"elapsed_micros"`
	RequestedFrames int    `json:"requested_frames"`
	WrittenFrames   int    `json:"written_frames"`
	SilenceFrames   int    `json:"silence_frames"`
}

func newPlayerDebugRecorder(root, deviceName string, requestedRate, deviceRate, deviceChannels int) (*playerDebugRecorder, error) {
	stamp := time.Now().Format("20060102T150405.000000000")
	dir := filepath.Join(root, fmt.Sprintf("%s-pid%d", stamp, os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create player debug directory: %w", err)
	}
	if deviceChannels <= 0 {
		deviceChannels = playbackChannels
	}
	metadata := debugAudioMetadata{
		CreatedAt:           time.Now(),
		DeviceName:          deviceName,
		RequestedSampleRate: requestedRate,
		DeviceSampleRate:    deviceRate,
		Channels:            deviceChannels,
		Format:              "pcm_s16le",
		PID:                 os.Getpid(),
	}
	encoded, err := json.MarshalIndent(metadata, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(dir, "metadata.json"), append(encoded, '\n'), 0o600); err != nil {
		return nil, fmt.Errorf("write audio debug metadata: %w", err)
	}

	r := &playerDebugRecorder{
		dir:     dir,
		started: time.Now(),
		events:  make(chan debugAudioEvent, 512),
	}
	r.pool.New = func() any { return make([]byte, 0, 4096) }
	r.wg.Add(1)
	go r.run(deviceRate, deviceChannels)
	return r, nil
}

func (r *playerDebugRecorder) captureSource(sampleRate int, samples []float32) {
	if r == nil || len(samples) == 0 {
		return
	}
	copySamples := append([]float32(nil), samples...)
	event := debugAudioEvent{
		source:     copySamples,
		sampleRate: sampleRate,
		sequence:   r.sequence.Add(1),
	}
	r.enqueue(event)
}

// captureCallback copies the exact pre-silenced buffer returned to miniaudio.
// It never performs file I/O on the real-time callback thread. If the debug
// writer cannot keep up, the event is dropped and counted in summary.json.
func (r *playerDebugRecorder) captureCallback(output []byte, requested, written int) {
	if r == nil {
		return
	}
	pooled := r.pool.Get().([]byte)
	if cap(pooled) < len(output) {
		pooled = make([]byte, len(output))
	} else {
		pooled = pooled[:len(output)]
	}
	copy(pooled, output)
	event := debugAudioEvent{
		callback:  pooled,
		elapsed:   time.Since(r.started),
		requested: requested,
		written:   written,
		sequence:  r.sequence.Add(1),
	}
	if !r.enqueue(event) {
		r.pool.Put(pooled[:0])
	}
}

func (r *playerDebugRecorder) enqueue(event debugAudioEvent) bool {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.closed {
		return false
	}
	select {
	case r.events <- event:
		return true
	default:
		r.dropped.Add(1)
		return false
	}
}

func (r *playerDebugRecorder) close() {
	if r == nil {
		return
	}
	r.closeOnce.Do(func() {
		r.mu.Lock()
		r.closed = true
		close(r.events)
		r.mu.Unlock()
		r.wg.Wait()
	})
}

func (r *playerDebugRecorder) run(deviceRate, deviceChannels int) {
	defer r.wg.Done()
	if deviceChannels <= 0 {
		deviceChannels = playbackChannels
	}
	wav, wavErr := newPCM16WAVWriter(filepath.Join(r.dir, "device-output.wav"), deviceRate, deviceChannels)
	callbacks, callbackErr := os.OpenFile(filepath.Join(r.dir, "callbacks.jsonl"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if callbackErr == nil {
		defer callbacks.Close()
	}
	var callbackWriter *bufio.Writer
	if callbackErr == nil {
		callbackWriter = bufio.NewWriter(callbacks)
		defer callbackWriter.Flush()
	}

	var sourceIndex int
	for event := range r.events {
		switch {
		case len(event.source) > 0:
			sourceIndex++
			path := filepath.Join(r.dir, fmt.Sprintf("source-%04d-%dhz.wav", sourceIndex, event.sampleRate))
			_ = WriteWAVFloat32(path, event.sampleRate, event.source)
			_ = os.Chmod(path, 0o600)
		case len(event.callback) > 0:
			if wavErr == nil {
				_ = wav.Write(event.callback)
			}
			if callbackWriter != nil {
				meta := debugCallbackMetadata{
					Sequence:        event.sequence,
					ElapsedMicros:   event.elapsed.Microseconds(),
					RequestedFrames: event.requested,
					WrittenFrames:   event.written,
					SilenceFrames:   event.requested - event.written,
				}
				encoded, _ := json.Marshal(meta)
				_, _ = callbackWriter.Write(append(encoded, '\n'))
			}
			r.pool.Put(event.callback[:0])
		}
	}
	if wavErr == nil {
		_ = wav.Close()
	}
	summary, _ := json.MarshalIndent(map[string]any{
		"dropped_debug_events": r.dropped.Load(),
		"source_segments":      sourceIndex,
	}, "", "  ")
	_ = os.WriteFile(filepath.Join(r.dir, "summary.json"), append(summary, '\n'), 0o600)
}

type pcm16WAVWriter struct {
	file       *os.File
	sampleRate int
	channels   int
	dataBytes  uint32
	closed     bool
}

func newPCM16WAVWriter(path string, sampleRate, channels int) (*pcm16WAVWriter, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	w := &pcm16WAVWriter{file: file, sampleRate: sampleRate, channels: channels}
	if err := w.writeHeader(0); err != nil {
		file.Close()
		return nil, err
	}
	return w, nil
}

func (w *pcm16WAVWriter) Write(p []byte) error {
	if w == nil || w.closed || len(p) == 0 {
		return nil
	}
	n, err := w.file.Write(p)
	w.dataBytes += uint32(n)
	return err
}

func (w *pcm16WAVWriter) Close() error {
	if w == nil || w.closed {
		return nil
	}
	w.closed = true
	if _, err := w.file.Seek(0, 0); err != nil {
		_ = w.file.Close()
		return err
	}
	if err := w.writeHeader(w.dataBytes); err != nil {
		_ = w.file.Close()
		return err
	}
	return w.file.Close()
}

func (w *pcm16WAVWriter) writeHeader(dataBytes uint32) error {
	byteRate := uint32(w.sampleRate * w.channels * 2)
	blockAlign := uint16(w.channels * 2)
	values := []any{
		[]byte("RIFF"), uint32(36) + dataBytes, []byte("WAVE"),
		[]byte("fmt "), uint32(16), uint16(1), uint16(w.channels),
		uint32(w.sampleRate), byteRate, blockAlign, uint16(16),
		[]byte("data"), dataBytes,
	}
	for _, value := range values {
		if err := binary.Write(w.file, binary.LittleEndian, value); err != nil {
			return err
		}
	}
	return nil
}
