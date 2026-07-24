package tts

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
)

const managedQwenProtocol = "samantha-qwen/v1"

type managedQwenSession struct {
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	scanner *bufio.Scanner
	stderr  *limitedBuffer
	wait    chan error

	mu        sync.Mutex
	closed    bool
	request   uint64
	model     string
	protocol  string
	voices    []string
	languages []string
}

type managedQwenMessage struct {
	Protocol    string   `json:"protocol,omitempty"`
	Type        string   `json:"type,omitempty"`
	RequestID   string   `json:"request_id,omitempty"`
	Text        string   `json:"text,omitempty"`
	OutputPath  string   `json:"output_path,omitempty"`
	Mode        string   `json:"mode,omitempty"`
	Voice       string   `json:"voice,omitempty"`
	Language    string   `json:"language,omitempty"`
	Instruction string   `json:"instruction,omitempty"`
	ErrorKind   string   `json:"error_kind,omitempty"`
	Message     string   `json:"message,omitempty"`
	Voices      []string `json:"voices,omitempty"`
	Languages   []string `json:"languages,omitempty"`
	SampleRate  int      `json:"sample_rate,omitempty"`
	// PCMf32leB64 is a base64-encoded little-endian float32 mono PCM chunk.
	// Emitted as type=audio_chunk after generate_custom_voice finishes (qwen-tts
	// does not expose true streaming waveform generation for CustomVoice yet).
	PCMf32leB64 string `json:"pcm_f32le_b64,omitempty"`
}

type managedQwenResponseError struct {
	kind    string
	message string
}

func (e *managedQwenResponseError) Error() string {
	return fmt.Sprintf("managed worker %s: %s", e.kind, e.message)
}

func startManagedQwenSession(binary, workerScript, model string, timeout time.Duration) (*managedQwenSession, error) {
	return startManagedQwenSessionContext(context.Background(), binary, workerScript, model, timeout)
}

func startManagedQwenSessionContext(ctx context.Context, binary, workerScript, model string, timeout time.Duration) (*managedQwenSession, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	cmd := exec.CommandContext(context.Background(), binary, workerScript, "serve", "--model", model)
	configureQwenCommand(cmd)
	cmd.Env = append(os.Environ(), "JOBLIB_MULTIPROCESSING=0")
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("create managed worker stdin: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		stdin.Close()
		return nil, fmt.Errorf("create managed worker stdout: %w", err)
	}
	stderr := &limitedBuffer{limit: maxWorkerOutput}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		stdin.Close()
		return nil, fmt.Errorf("start managed worker: %w", err)
	}
	s := &managedQwenSession{
		cmd: cmd, stdin: stdin, scanner: bufio.NewScanner(stdout), stderr: stderr,
		wait: make(chan error, 1), model: model, protocol: managedQwenProtocol,
	}
	s.scanner.Buffer(make([]byte, 64<<10), 1<<20)
	go func() { s.wait <- cmd.Wait() }()

	ready := make(chan managedQwenMessage, 1)
	go func() {
		msg, err := scanManagedQwenMessage(s.scanner)
		if err != nil {
			msg.Message = "worker exited before ready: " + err.Error()
		}
		ready <- msg
	}()
	if timeout <= 0 {
		timeout = defaultQwenTTSTimeout
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case msg := <-ready:
		if msg.Protocol != managedQwenProtocol || msg.Type != "ready" {
			s.kill()
			return nil, fmt.Errorf("managed worker handshake failed: %s%s", msg.Message, workerOutputSuffix(stderr.String(), ""))
		}
		s.voices = append([]string(nil), msg.Voices...)
		s.languages = append([]string(nil), msg.Languages...)
		return s, nil
	case err := <-s.wait:
		return nil, fmt.Errorf("managed worker exited during startup: %v%s", err, workerOutputSuffix(stderr.String(), ""))
	case <-timer.C:
		s.kill()
		return nil, fmt.Errorf("managed worker startup timed out after %s%s", timeout, workerOutputSuffix(stderr.String(), ""))
	case <-ctx.Done():
		s.kill()
		return nil, ctx.Err()
	}
}

// SynthesizeToStream runs one managed synthesis and writes float32 PCM frames
// as audio_chunk messages arrive. Generation is still whole-utterance at the
// model layer; chunks avoid temp-WAV I/O and feed the player immediately after.
func (s *managedQwenSession) SynthesizeToStream(ctx context.Context, req SynthesisRequest, stream *audio.PCMStream) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("managed worker is closed")
	}
	s.request++
	id := fmt.Sprintf("qwen-%d", s.request)
	message := managedQwenMessage{
		Protocol: s.protocol, Type: "synthesize", RequestID: id,
		Text: req.Text, Mode: string(req.Mode),
		Voice: req.Voice, Language: req.Language, Instruction: req.Instruction,
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := s.stdin.Write(data); err != nil {
		return fmt.Errorf("write managed worker request: %w", err)
	}

	rateSet := false
	for {
		msg, err := s.readNextResponse(ctx)
		if err != nil {
			return err
		}
		if msg.Protocol != s.protocol || msg.RequestID != id {
			return fmt.Errorf("malformed managed worker response: %s", msg.Message)
		}
		switch msg.Type {
		case "audio_chunk":
			samples, sampleRate, err := decodeManagedPCMChunk(msg)
			if err != nil {
				return err
			}
			if !rateSet {
				if sampleRate == 0 {
					sampleRate = qwen3TTSSampleRate
				}
				if sampleRate != qwen3TTSSampleRate {
					return fmt.Errorf("managed worker returned %d Hz, want %d Hz", sampleRate, qwen3TTSSampleRate)
				}
				if err := stream.SetSampleRate(sampleRate); err != nil {
					return err
				}
				rateSet = true
			}
			if len(samples) == 0 {
				continue
			}
			if err := stream.Write(samples); err != nil {
				return err
			}
		case "complete":
			if !rateSet {
				// Worker may complete without chunks when audio is empty.
				if err := stream.SetSampleRate(qwen3TTSSampleRate); err != nil {
					return err
				}
			}
			return nil
		case "error":
			return &managedQwenResponseError{kind: msg.ErrorKind, message: msg.Message}
		default:
			return fmt.Errorf("unexpected managed worker response type %q", msg.Type)
		}
	}
}

// Synthesize is the legacy whole-file path kept for one-shot CLI / fixtures.
func (s *managedQwenSession) Synthesize(ctx context.Context, req SynthesisRequest, outputPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return errors.New("managed worker is closed")
	}
	s.request++
	id := fmt.Sprintf("qwen-%d", s.request)
	message := managedQwenMessage{
		Protocol: s.protocol, Type: "synthesize", RequestID: id,
		Text: req.Text, OutputPath: outputPath, Mode: string(req.Mode),
		Voice: req.Voice, Language: req.Language, Instruction: req.Instruction,
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	if _, err := s.stdin.Write(data); err != nil {
		return fmt.Errorf("write managed worker request: %w", err)
	}

	for {
		msg, err := s.readNextResponse(ctx)
		if err != nil {
			return err
		}
		if msg.Protocol != s.protocol || msg.RequestID != id {
			return fmt.Errorf("malformed managed worker response: %s", msg.Message)
		}
		switch msg.Type {
		case "audio_chunk":
			// Optional when output_path is set; ignore progressive PCM.
			continue
		case "complete":
			return nil
		case "error":
			return &managedQwenResponseError{kind: msg.ErrorKind, message: msg.Message}
		default:
			return fmt.Errorf("unexpected managed worker response type %q", msg.Type)
		}
	}
}

func (s *managedQwenSession) readNextResponse(ctx context.Context) (managedQwenMessage, error) {
	response := make(chan managedQwenMessage, 1)
	go func() {
		msg, err := scanManagedQwenMessage(s.scanner)
		if err != nil {
			msg.Message = "worker exited without a synthesis response: " + err.Error()
		}
		response <- msg
	}()
	select {
	case msg := <-response:
		if msg.Message != "" && msg.Type == "" {
			return msg, errors.New(msg.Message)
		}
		return msg, nil
	case <-ctx.Done():
		s.closed = true
		s.kill()
		return managedQwenMessage{}, ctx.Err()
	case err := <-s.wait:
		s.closed = true
		return managedQwenMessage{}, fmt.Errorf("managed worker exited: %v%s", err, workerOutputSuffix(s.stderr.String(), ""))
	}
}

func decodeManagedPCMChunk(msg managedQwenMessage) ([]float32, int, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(msg.PCMf32leB64))
	if err != nil {
		return nil, 0, fmt.Errorf("decode pcm chunk: %w", err)
	}
	if len(raw)%4 != 0 {
		return nil, 0, fmt.Errorf("pcm chunk length %d is not a multiple of 4", len(raw))
	}
	n := len(raw) / 4
	samples := make([]float32, n)
	for i := 0; i < n; i++ {
		bits := binary.LittleEndian.Uint32(raw[i*4 : i*4+4])
		samples[i] = math.Float32frombits(bits)
	}
	return samples, msg.SampleRate, nil
}

// scanManagedQwenMessage ignores non-protocol chatter from transitive model
// libraries. The worker redirects those diagnostics to stderr as well, but the
// reader remains defensive so one upstream print cannot corrupt a long-lived
// JSONL session.
func scanManagedQwenMessage(scanner *bufio.Scanner) (managedQwenMessage, error) {
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(strings.TrimSpace(string(line))) == 0 {
			continue
		}
		var msg managedQwenMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}
		if msg.Protocol == managedQwenProtocol && strings.TrimSpace(msg.Type) != "" {
			return msg, nil
		}
	}
	if err := scanner.Err(); err != nil {
		return managedQwenMessage{}, err
	}
	return managedQwenMessage{}, io.EOF
}

func managedWorkerRestartable(err error) bool {
	if err == nil || errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var responseErr *managedQwenResponseError
	if !errors.As(err, &responseErr) {
		return true
	}
	switch strings.ToLower(strings.TrimSpace(responseErr.kind)) {
	case "unsupported_input", "invalid_request", "canceled", "cancelled":
		return false
	default:
		return true
	}
}

func (s *managedQwenSession) Close() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	s.closed = true
	data, _ := json.Marshal(managedQwenMessage{Protocol: s.protocol, Type: "shutdown"})
	_, _ = s.stdin.Write(append(data, '\n'))
	_ = s.stdin.Close()
	select {
	case <-s.wait:
	case <-time.After(qwenProcessWaitDelay):
		s.kill()
	}
}

func (s *managedQwenSession) kill() {
	if s.cmd != nil && s.cmd.Cancel != nil {
		_ = s.cmd.Cancel()
	} else if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
	}
}

func managedWorkerError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, context.Canceled) {
		return &ProviderError{Provider: qwen3TTSProviderName, Operation: "run managed worker", Kind: ProviderErrorCanceled, Err: err}
	}
	var responseErr *managedQwenResponseError
	if errors.As(err, &responseErr) {
		switch strings.ToLower(strings.TrimSpace(responseErr.kind)) {
		case "unsupported_input", "invalid_request":
			return &ProviderError{Provider: qwen3TTSProviderName, Operation: "run managed worker", Kind: ProviderErrorInput, Err: err}
		case "canceled", "cancelled":
			return &ProviderError{Provider: qwen3TTSProviderName, Operation: "run managed worker", Kind: ProviderErrorCanceled, Err: err}
		}
	}
	kind := ProviderErrorWorker
	if strings.Contains(err.Error(), "malformed") {
		kind = ProviderErrorMalformed
	}
	return &ProviderError{Provider: qwen3TTSProviderName, Operation: "run managed worker", Kind: kind, Err: err}
}
