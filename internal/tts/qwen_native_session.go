package tts

import (
	"bufio"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	managedqwen "github.com/lancekrogers/samantha/internal/qwen"
)

// nativeWorkerProtocol is frozen in projects/qwen3-tts-native/docs/PROTOCOL.md.
const nativeWorkerProtocol = "qwen3-tts-worker/v1"

// nativeQwenSession is a long-lived client for bin/qwen3-tts-worker (no Python).
// Stage A: whole-utterance PCM after synth (one pcm_meta + raw f32le + final).
// Stage B will emit multiple pcm_meta chunks; this client already concatenates.
type nativeQwenSession struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Reader
	stderr *limitedBuffer
	wait   chan error

	mu        sync.Mutex
	writeMu   sync.Mutex
	activeMu  sync.Mutex
	activeID  string
	closed    bool
	request   uint64
	modelDir  string
	protocol  string
	rate      int
	streaming bool
	presets   []string
}

type nativeReadyMsg struct {
	Type       string   `json:"type"`
	Protocol   string   `json:"protocol"`
	SampleRate int      `json:"sample_rate"`
	PCMFormat  string   `json:"pcm_format"`
	Streaming  bool     `json:"streaming"`
	Presets    []string `json:"presets,omitempty"`
	Note       string   `json:"note,omitempty"`
}

// isNativeWorkerBinary reports whether path names the product native worker.
func isNativeWorkerBinary(path string) bool {
	base := strings.ToLower(filepath.Base(strings.TrimSpace(path)))
	return base == "qwen3-tts-worker" || base == "qwen3-tts-worker.exe"
}

// nativeInstallPaths is the ensure layout under models_dir/qwen3-tts/.
type nativeInstallPaths struct {
	Root     string
	Worker   string
	ModelDir string
}

// findNativeInstall looks for a release package installed for product use.
func findNativeInstall(modelsDir, preferredTier string) (nativeInstallPaths, bool) {
	status := managedqwen.InspectNative(modelsDir, preferredTier)
	if !status.Installed {
		return nativeInstallPaths{}, false
	}
	return nativeInstallPaths{Root: status.Root, Worker: status.Worker, ModelDir: status.ModelDir}, true
}

// startNativeQwenSession launches qwen3-tts-worker against modelDir.
// tier selects the GGUF when the package is multi-tier (QWEN3_TTS_TIER); empty
// uses DefaultModelTier so multi-tier packages do not silently pick the wrong size.
func startNativeQwenSession(ctx context.Context, workerBin, modelDir, tier string, timeout time.Duration) (*nativeQwenSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	workerBin, err := filepath.Abs(workerBin)
	if err != nil {
		return nil, fmt.Errorf("resolve native worker: %w", err)
	}
	modelDir, err = filepath.Abs(modelDir)
	if err != nil {
		return nil, fmt.Errorf("resolve native model dir: %w", err)
	}
	tier = managedqwen.NormalizeModelTier(tier)
	// Lifetime is owned by the session (Background), not the startup ctx.
	// CommandContext is required because configureQwenCommand sets Cancel.
	cmd := exec.CommandContext(context.Background(), workerBin, modelDir)
	configureQwenCommand(cmd)
	libDir := filepath.Dir(workerBin)
	cmd.Env = withNativeWorkerEnv(os.Environ(), libDir, tier)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("native worker stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("native worker stdout: %w", err)
	}
	stderr := &limitedBuffer{limit: maxWorkerOutput}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		_ = stdin.Close()
		return nil, fmt.Errorf("start native worker: %w", err)
	}
	s := &nativeQwenSession{
		cmd: cmd, stdin: stdin, stdout: bufio.NewReader(stdout), stderr: stderr,
		wait: make(chan error, 1), modelDir: modelDir, protocol: nativeWorkerProtocol,
		rate: qwen3TTSSampleRate,
	}
	go func() { s.wait <- cmd.Wait() }()

	if timeout <= 0 {
		timeout = defaultQwenTTSTimeout
	}
	readyCh := make(chan nativeReadyMsg, 1)
	errCh := make(chan error, 1)
	go func() {
		line, err := s.stdout.ReadString('\n')
		if err != nil {
			errCh <- err
			return
		}
		var ready nativeReadyMsg
		if err := json.Unmarshal([]byte(line), &ready); err != nil {
			errCh <- fmt.Errorf("ready json: %w (%q)", err, strings.TrimSpace(line))
			return
		}
		readyCh <- ready
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case ready := <-readyCh:
		if ready.Type != "ready" {
			s.Close()
			return nil, fmt.Errorf("native worker handshake failed: type=%s%s", ready.Type, workerOutputSuffix(stderr.String(), ""))
		}
		if ready.Protocol != nativeWorkerProtocol {
			s.Close()
			return nil, fmt.Errorf("native worker protocol %q unsupported; want %q", ready.Protocol, nativeWorkerProtocol)
		}
		if ready.SampleRate != qwen3TTSSampleRate {
			s.Close()
			return nil, fmt.Errorf("native worker sample rate %d unsupported; want %d", ready.SampleRate, qwen3TTSSampleRate)
		}
		if ready.PCMFormat != "f32le" {
			s.Close()
			return nil, fmt.Errorf("native worker PCM format %q unsupported; want f32le", ready.PCMFormat)
		}
		s.streaming = ready.Streaming
		s.presets = append([]string(nil), ready.Presets...)
		if len(s.presets) == 0 {
			s.presets = loadNativePresetsFromDisk(modelDir)
		}
		return s, nil
	case err := <-errCh:
		s.Close()
		return nil, fmt.Errorf("native worker ready: %w%s", err, workerOutputSuffix(stderr.String(), ""))
	case err := <-s.wait:
		return nil, fmt.Errorf("native worker exited during startup: %v%s", err, workerOutputSuffix(stderr.String(), ""))
	case <-timer.C:
		s.Close()
		return nil, fmt.Errorf("native worker startup timed out after %s%s", timeout, workerOutputSuffix(stderr.String(), ""))
	case <-ctx.Done():
		s.Close()
		return nil, ctx.Err()
	}
}

// withNativeWorkerEnv sets DYLD/LD library paths and optional QWEN3_TTS_TIER.
// Parent QWEN3_TTS_TIER / QWEN3_TTS_MODEL are stripped so the host config wins
// and ambient shell env cannot override a fail-closed product tier.
func withNativeWorkerEnv(env []string, libDir, tier string) []string {
	out := make([]string, 0, len(env)+4)
	for _, e := range env {
		key, _, ok := strings.Cut(e, "=")
		if !ok {
			out = append(out, e)
			continue
		}
		switch key {
		case "DYLD_LIBRARY_PATH", "LD_LIBRARY_PATH", "QWEN3_TTS_TIER", "QWEN3_TTS_MODEL":
			continue
		}
		out = append(out, e)
	}
	sep := string(os.PathListSeparator)
	for _, key := range []string{"DYLD_LIBRARY_PATH", "LD_LIBRARY_PATH"} {
		prev := os.Getenv(key)
		if prev != "" {
			out = append(out, key+"="+libDir+sep+prev)
		} else {
			out = append(out, key+"="+libDir)
		}
	}
	if tier = strings.TrimSpace(tier); tier != "" {
		out = append(out, "QWEN3_TTS_TIER="+tier)
	}
	return out
}

func loadNativePresetsFromDisk(modelDir string) []string {
	path := filepath.Join(modelDir, "presets", "presets.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}
	var doc struct {
		Voices []struct {
			Name string `json:"name"`
		} `json:"voices"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil
	}
	names := make([]string, 0, len(doc.Voices))
	for _, v := range doc.Voices {
		if n := strings.TrimSpace(v.Name); n != "" {
			names = append(names, n)
		}
	}
	return names
}

// SynthesizeToStream runs one request and writes float32 PCM into stream.
func (s *nativeQwenSession) SynthesizeToStream(ctx context.Context, req SynthesisRequest, stream *audio.PCMStream) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("native worker is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	s.request++
	id := fmt.Sprintf("nqwen-%d", s.request)

	msg := map[string]string{
		"type": "synthesize",
		"id":   id,
		"text": req.Text,
	}
	if preset := strings.TrimSpace(req.Voice); preset != "" && !strings.EqualFold(preset, "default") {
		msg["preset"] = preset
	}
	if ref := strings.TrimSpace(req.ReferenceAudio); ref != "" {
		msg["ref_wav"] = ref
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	if err := ctx.Err(); err != nil {
		s.writeMu.Unlock()
		return err
	}
	s.activeMu.Lock()
	s.activeID = id
	s.activeMu.Unlock()
	_, err = s.stdin.Write(append(data, '\n'))
	s.writeMu.Unlock()
	if err != nil {
		s.clearActive(id)
		return fmt.Errorf("write native worker request: %w", err)
	}
	defer s.clearActive(id)

	rateSet := false
	for {
		if err := ctx.Err(); err != nil {
			_ = s.sendCancel(id)
			return err
		}
		line, err := s.readLine(ctx)
		if err != nil {
			return err
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var head struct {
			Type       string `json:"type"`
			ID         string `json:"id"`
			Message    string `json:"message"`
			SampleRate int    `json:"sample_rate"`
			NSamples   int    `json:"n_samples"`
		}
		if err := json.Unmarshal([]byte(line), &head); err != nil {
			return fmt.Errorf("native worker line: %w (%q)", err, line)
		}
		switch head.Type {
		case "error":
			return fmt.Errorf("native worker error: %s", head.Message)
		case "pcm_meta":
			n := head.NSamples
			if n <= 0 {
				return fmt.Errorf("native worker pcm_meta n_samples=%d", n)
			}
			rate := head.SampleRate
			if rate == 0 {
				rate = s.rate
			}
			if rate != qwen3TTSSampleRate {
				return fmt.Errorf("native worker returned %d Hz, want %d Hz", rate, qwen3TTSSampleRate)
			}
			if !rateSet {
				if err := stream.SetSampleRate(rate); err != nil {
					return err
				}
				rateSet = true
			}
			buf := make([]byte, n*4)
			if _, err := io.ReadFull(s.stdout, buf); err != nil {
				return fmt.Errorf("native worker pcm read: %w", err)
			}
			// Stage A trailing newline after raw PCM.
			if b, err := s.stdout.ReadByte(); err == nil && b != '\n' {
				_ = s.stdout.UnreadByte()
			}
			samples := make([]float32, n)
			for i := 0; i < n; i++ {
				samples[i] = math.Float32frombits(binary.LittleEndian.Uint32(buf[i*4:]))
			}
			const chunk = 2048
			for start := 0; start < len(samples); start += chunk {
				if err := ctx.Err(); err != nil {
					_ = s.sendCancel(id)
					return err
				}
				end := min(start+chunk, len(samples))
				if err := stream.Write(samples[start:end]); err != nil {
					return err
				}
			}
		case "final":
			if !rateSet {
				if err := stream.SetSampleRate(qwen3TTSSampleRate); err != nil {
					return err
				}
			}
			return nil
		case "generating":
			// progress only
		default:
			// ignore future control types
		}
	}
}

func (s *nativeQwenSession) sendCancel(id string) error {
	b, err := json.Marshal(map[string]string{"type": "cancel", "id": id})
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	_, err = s.stdin.Write(append(b, '\n'))
	return err
}

// CancelActive writes a cancellation for the session-generated request ID
// without waiting for the synthesis/read lock. Stage A workers may only act on
// it between requests, but the write remains prompt and Stage B can interrupt
// mid-synthesis.
func (s *nativeQwenSession) CancelActive() error {
	if s == nil {
		return nil
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.activeMu.Lock()
	id := s.activeID
	s.activeMu.Unlock()
	if id == "" {
		return nil
	}
	b, err := json.Marshal(map[string]string{"type": "cancel", "id": id})
	if err != nil {
		return err
	}
	_, err = s.stdin.Write(append(b, '\n'))
	return err
}

func (s *nativeQwenSession) clearActive(id string) {
	s.activeMu.Lock()
	defer s.activeMu.Unlock()
	if s.activeID == id {
		s.activeID = ""
	}
}

func (s *nativeQwenSession) readLine(ctx context.Context) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := s.stdout.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case res := <-ch:
		return res.line, res.err
	case err := <-s.wait:
		return "", fmt.Errorf("native worker exited: %v%s", err, workerOutputSuffix(s.stderr.String(), ""))
	}
}

// Close shuts down the worker process.
func (s *nativeQwenSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	if s.stdin != nil {
		s.writeMu.Lock()
		_, _ = s.stdin.Write([]byte(`{"type":"shutdown"}` + "\n"))
		s.writeMu.Unlock()
		_ = s.stdin.Close()
	}
	if s.cmd != nil && s.cmd.Process != nil {
		done := make(chan struct{})
		go func() {
			select {
			case <-s.wait:
			case <-time.After(3 * time.Second):
				_ = s.cmd.Process.Kill()
				<-s.wait
			}
			close(done)
		}()
		<-done
	}
}
