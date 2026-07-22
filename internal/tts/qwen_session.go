package tts

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
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
		if msg.Protocol != s.protocol || msg.RequestID != id {
			return fmt.Errorf("malformed managed worker response: %s", msg.Message)
		}
		switch msg.Type {
		case "complete":
			return nil
		case "error":
			return &managedQwenResponseError{kind: msg.ErrorKind, message: msg.Message}
		default:
			return fmt.Errorf("unexpected managed worker response type %q", msg.Type)
		}
	case <-ctx.Done():
		s.closed = true
		s.kill()
		return ctx.Err()
	case err := <-s.wait:
		s.closed = true
		return fmt.Errorf("managed worker exited: %v%s", err, workerOutputSuffix(s.stderr.String(), ""))
	}
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
