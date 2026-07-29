package tts

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	managedqwen "github.com/lancekrogers/samantha/internal/qwen"
)

const (
	qwen3TTSProviderName  = "qwen3-tts"
	qwen3TTSSampleRate    = 24000
	defaultQwenTTSTimeout = 120 * time.Second
	maxQwenAudioDuration  = 2 * time.Hour
	maxWorkerOutput       = 8 << 10

	// Lab-measured stage-A warm TTFA ≈ full wall (qwen.latency.v1 worker_warmish).
	// Stall watchdog FirstAudioGrace uses p95 headroom, not the mean:
	//   warm_wall ~4–5s short phrase RTF~1.37 → p95 short ≈ 8s; long replies scale.
	// Prefer configured timeout when larger so operators can raise the ceiling.
	nativeWarmTTP95 = 8 * time.Second
	// Cold ready (~6s measured) is paid once at session start, not every turn.
	// If the warm session dies mid-conversation, restart cost is folded into grace.
	nativeColdReadyP95 = 12 * time.Second
	// Managed Python whole-utterance was ~6–9s process wall; keep prior default floor.
	managedWarmTTP95 = 15 * time.Second
)

type limitedBuffer struct {
	buf   bytes.Buffer
	mu    sync.Mutex
	limit int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	written := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining > 0 {
		if len(p) > remaining {
			p = p[:remaining]
		}
		_, _ = b.buf.Write(p)
	}
	return written, nil
}

func (b *limitedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// qwenCommand starts one bounded native synthesis process. Keeping this as a
// seam makes the provider testable without downloading or vendoring models.
type qwenCommand func(context.Context, string, ...string) *exec.Cmd

// Qwen3TTS implements the optional Qwen3-TTS provider.
//
// Resolution order:
//  1. Explicit native worker binary (qwen3-tts-worker) + model dir
//  2. Installed native package under models_dir/qwen3-tts/ (preferred over Python)
//  3. Managed Python CustomVoice worker (legacy until cutover)
//  4. External one-shot qwen3-tts-cli (lab/debug only)
type Qwen3TTS struct {
	binary              string
	model               string
	timeout             time.Duration
	timeoutSet          bool // true when cfg.QwenTTSTimeout was explicitly set
	command             qwenCommand
	alive               atomic.Bool
	mode                VoiceMode
	voice               string
	language            string
	instruction         string
	referenceAudio      string
	referenceTranscript string
	consent             bool
	managed             bool // managed Python JSONL worker (legacy product path)
	native              bool // native qwen3-tts-worker protocol (product path)
	workerScript        string
	startupTimeout      time.Duration
	sessionMu           sync.Mutex
	session             *managedQwenSession
	nativeSession       *nativeQwenSession
	nativeSessionRef    atomic.Pointer[nativeQwenSession]
}

// NewQwen3TTS resolves the native package, managed installation, or external CLI.
func NewQwen3TTS(cfg *config.Config) (*Qwen3TTS, error) {
	if cfg == nil {
		return nil, errors.New("qwen3-tts: nil config")
	}
	if err := config.ValidateQwenTTSConfig(cfg); err != nil {
		return nil, fmt.Errorf("qwen3-tts: %w", err)
	}

	binary := strings.TrimSpace(cfg.QwenTTSBinary)
	model := strings.TrimSpace(cfg.QwenTTSModel)
	modelsDir := config.ModelsDirFrom(cfg)
	managed := false
	native := false
	workerScript := ""

	// Prefer explicit native worker binary.
	if binary != "" && isNativeWorkerBinary(binary) {
		if model == "" {
			return nil, errors.New("qwen3-tts: qwen_tts_model is required for the native worker")
		}
		native = true
	} else if install, ok := findNativeInstall(modelsDir, cfg.QwenTTSModelTier); ok && managedqwen.UseManaged(binary, model) {
		// Product default: empty binary/model → use ensure-installed native package when present.
		binary = install.Worker
		model = install.ModelDir
		native = true
	} else if managedqwen.UseManaged(binary, model) {
		status := managedqwen.Inspect(modelsDir)
		if !status.Installed {
			return nil, errors.New("qwen3-tts: managed preset voices are not installed; open Settings → TTS and select Qwen3-TTS to install (or install native package under models/qwen3-tts)")
		}
		binary, model, workerScript = status.Python, status.Model, status.Worker
		managed = true
	} else {
		if binary == "" {
			binary = "qwen3-tts-cli"
		}
		if model == "" {
			return nil, errors.New("qwen3-tts: qwen_tts_model is required for an external worker")
		}
		if isNativeWorkerBinary(binary) {
			native = true
		}
	}

	var binaryPath string
	var err error
	if filepath.IsAbs(binary) {
		binaryPath = binary
		if _, err := os.Stat(binaryPath); err != nil {
			return nil, fmt.Errorf("qwen3-tts: native worker %q not found: %w", binary, err)
		}
	} else {
		binaryPath, err = exec.LookPath(binary)
		if err != nil {
			return nil, fmt.Errorf("qwen3-tts: native worker %q not found: %w", binary, err)
		}
		if !filepath.IsAbs(binaryPath) {
			binaryPath, err = filepath.Abs(binaryPath)
			if err != nil {
				return nil, fmt.Errorf("qwen3-tts: resolve native worker %q: %w", binary, err)
			}
		}
	}
	if isNativeWorkerBinary(binaryPath) {
		native = true
		managed = false
	}
	modelInfo, err := os.Stat(model)
	if err != nil {
		return nil, fmt.Errorf("qwen3-tts: model directory %q: %w", model, err)
	}
	if !modelInfo.IsDir() {
		return nil, fmt.Errorf("qwen3-tts: model path %q is not a directory", model)
	}

	timeout := defaultQwenTTSTimeout
	timeoutSet := false
	if cfg.QwenTTSTimeout > 0 {
		timeout = time.Duration(cfg.QwenTTSTimeout) * time.Second
		timeoutSet = true
	}

	q := newQwen3TTS(binaryPath, model, timeout, exec.CommandContext)
	q.timeoutSet = timeoutSet
	q.mode = VoiceMode(strings.TrimSpace(cfg.QwenTTSMode))
	q.voice = strings.TrimSpace(cfg.QwenTTSVoice)
	q.language = strings.TrimSpace(cfg.QwenTTSLanguage)
	q.instruction = cfg.QwenTTSInstruction
	q.referenceAudio = strings.TrimSpace(cfg.QwenTTSReferenceAudio)
	q.referenceTranscript = cfg.QwenTTSReferenceText
	q.consent = cfg.QwenTTSConsent
	q.managed = managed
	q.native = native
	q.workerScript = workerScript
	if managed || native {
		if q.mode == "" || q.mode == VoiceModeStatic {
			q.mode = VoiceModeCustomVoice
		}
		if q.voice == "" || strings.EqualFold(q.voice, "default") {
			q.voice = managedqwen.DefaultVoice
		} else if voice, ok := managedqwen.CanonicalVoice(q.voice); ok {
			q.voice = voice
		}
		if q.language == "" {
			q.language = managedqwen.DefaultLanguage
		} else if language, ok := managedqwen.CanonicalLanguage(q.language); ok {
			q.language = language
		}
	}
	q.alive.Store(true)
	startupTimeout := max(timeout, 10*time.Minute)
	q.startupTimeout = startupTimeout
	if native {
		session, err := startNativeQwenSession(context.Background(), binaryPath, model, startupTimeout)
		if err != nil {
			q.alive.Store(false)
			return nil, fmt.Errorf("qwen3-tts: %w", err)
		}
		q.nativeSession = session
		q.nativeSessionRef.Store(session)
	} else if managed {
		session, err := startManagedQwenSession(binaryPath, workerScript, model, startupTimeout)
		if err != nil {
			q.alive.Store(false)
			return nil, fmt.Errorf("qwen3-tts: %w", err)
		}
		q.session = session
	}
	return q, nil
}

func newQwen3TTS(binary, model string, timeout time.Duration, command qwenCommand) *Qwen3TTS {
	if timeout <= 0 {
		timeout = defaultQwenTTSTimeout
	}
	if command == nil {
		command = exec.CommandContext
	}
	return &Qwen3TTS{binary: binary, model: model, timeout: timeout, command: command}
}

// FirstAudioGrace reports how long the pipeline's playback-stall watchdog
// should wait for the first audible frame. Stage-A Qwen (native or managed)
// generates whole utterances before PCM, so grace is driven by measured warm
// TTFA p95 headroom, not the 8s pipeline default. An explicitly configured
// qwen_tts_timeout raises the grace when larger than p95 (long-form audio).
func (q *Qwen3TTS) FirstAudioGrace() time.Duration {
	if q == nil {
		return managedWarmTTP95
	}
	p95 := managedWarmTTP95
	if q.native {
		// Warm turn + one unexpected restart (cold ready p95) for worst-case barge recovery.
		p95 = nativeWarmTTP95 + nativeColdReadyP95
	}
	if q.timeoutSet && q.timeout > p95 {
		return q.timeout
	}
	return p95
}

// SoftCancel asks the warm worker to abandon the current request without
// killing the process (stage A: between requests; stage B: mid-synth).
func (q *Qwen3TTS) SoftCancel(_ string) error {
	if q == nil || !q.alive.Load() {
		return &ProviderError{Provider: qwen3TTSProviderName, Operation: "cancel", Kind: ProviderErrorUnavailable, Err: errors.New("provider is closed")}
	}
	if q.native {
		if session := q.nativeSessionRef.Load(); session != nil {
			return session.CancelActive()
		}
	}
	// Managed Python worker has no soft-cancel control message yet.
	return nil
}

// Synthesize streams synthesized PCM frames for the given text.
func (q *Qwen3TTS) Synthesize(ctx context.Context, text string) (*audio.PCMStream, error) {
	result, err := q.SynthesizeRequest(ctx, SynthesisRequest{
		Text:                text,
		Voice:               q.voice,
		Mode:                q.mode,
		Language:            q.language,
		Instruction:         q.instruction,
		ReferenceAudio:      q.referenceAudio,
		ReferenceTranscript: q.referenceTranscript,
	})
	if err != nil {
		return nil, err
	}
	return result.Stream, nil
}

// SynthesizeRequest validates provider-specific controls and converts the
// worker's 24 kHz WAV output into Samantha's existing float32 PCM stream.
func (q *Qwen3TTS) SynthesizeRequest(ctx context.Context, req SynthesisRequest) (SynthesisResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if !q.Available() {
		return SynthesisResult{}, &ProviderError{Provider: qwen3TTSProviderName, Operation: "synthesize", Kind: ProviderErrorUnavailable, Err: errors.New("provider is closed")}
	}
	if strings.TrimSpace(req.Text) == "" {
		return SynthesisResult{}, &ProviderError{Provider: qwen3TTSProviderName, Operation: "synthesize", Kind: ProviderErrorInput, Err: errors.New("text is empty")}
	}
	if q.managed || q.native {
		if req.Mode == "" || req.Mode == VoiceModeStatic {
			req.Mode = VoiceModeCustomVoice
		}
		if strings.TrimSpace(req.Voice) == "" || strings.EqualFold(req.Voice, "default") {
			req.Voice = q.voice
		}
		if strings.TrimSpace(req.Language) == "" {
			req.Language = q.language
		}
		if voice, ok := managedqwen.CanonicalVoice(req.Voice); ok {
			req.Voice = voice
		}
		if language, ok := managedqwen.CanonicalLanguage(req.Language); ok {
			req.Language = language
		}
		if req.Speed != 0 {
			return SynthesisResult{}, qwenUnsupported("speech speed", "the Qwen CustomVoice worker does not expose speed control")
		}
		if q.managed {
			if strings.TrimSpace(req.ReferenceAudio) != "" || strings.TrimSpace(req.ReferenceTranscript) != "" {
				return SynthesisResult{}, qwenUnsupported("reference voice", "installable approved-clone support has not landed yet")
			}
		}
		// Native stage A supports preset + optional ref_wav; transcript not required by worker.
		if err := ValidateRequest(q.Capabilities(), req); err != nil {
			return SynthesisResult{}, err
		}
	} else {
		if req.SampleRate != 0 && req.SampleRate != qwen3TTSSampleRate {
			return SynthesisResult{}, qwenUnsupported("sample rate", fmt.Sprintf("cannot resample to %d Hz (native rate %d Hz)", req.SampleRate, qwen3TTSSampleRate))
		}
		if voice := strings.TrimSpace(req.Voice); voice != "" && !strings.EqualFold(voice, "default") {
			return SynthesisResult{}, qwenUnsupported("voice selection", fmt.Sprintf("voice %q is unsupported by the external native CLI; use the model's default voice", req.Voice))
		}
		if req.Speed != 0 {
			return SynthesisResult{}, qwenUnsupported("speech speed", "unsupported by the external native CLI; omit speed")
		}
		if req.Mode != "" && req.Mode != VoiceModeStatic {
			return SynthesisResult{}, qwenUnsupported("voice mode", fmt.Sprintf("mode %q is not verified by the external native CLI", req.Mode))
		}
		if strings.TrimSpace(req.Language) != "" {
			return SynthesisResult{}, qwenUnsupported("language", "language selection is not exposed by the external native CLI")
		}
		if strings.TrimSpace(req.Instruction) != "" {
			return SynthesisResult{}, qwenUnsupported("voice instruction", "voice design is not exposed by the external native CLI")
		}
		if strings.TrimSpace(req.ReferenceAudio) != "" || strings.TrimSpace(req.ReferenceTranscript) != "" {
			return SynthesisResult{}, qwenUnsupported("reference voice", "reference-audio cloning is not exposed by the external native CLI")
		}
	}
	if !q.native {
		if err := validateQwenReference(req); err != nil {
			return SynthesisResult{}, err
		}
	} else if ref := strings.TrimSpace(req.ReferenceAudio); ref != "" {
		// Native worker accepts ref_wav without transcript; still bound file existence.
		if _, err := os.Stat(ref); err != nil {
			return SynthesisResult{}, &ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: ProviderErrorInput, Err: fmt.Errorf("reference audio is unavailable")}
		}
	}
	if req.Mode == VoiceModeApprovedClone && !q.consent {
		return SynthesisResult{}, &ProviderError{Provider: qwen3TTSProviderName, Operation: "validate request", Kind: ProviderErrorInput, Err: errors.New("explicit consent is required for approved voice cloning")}
	}

	voice := req.Voice
	if voice == "" {
		voice = "default"
	}
	stream := audio.NewPCMStream(ctx)
	go q.synthesize(ctx, req, stream)

	resultMode := req.Mode
	if resultMode == "" {
		resultMode = VoiceModeStatic
	}
	return SynthesisResult{
		Stream:     stream,
		SampleRate: qwen3TTSSampleRate,
		Provider:   qwen3TTSProviderName,
		Model:      q.model,
		Voice:      voice,
		Mode:       resultMode,
	}, nil
}

func qwenUnsupported(feature, detail string) error {
	return &ProviderError{
		Provider:  qwen3TTSProviderName,
		Operation: "validate request",
		Kind:      ProviderErrorInput,
		Err:       &UnsupportedFeatureError{Provider: qwen3TTSProviderName, Feature: feature, Detail: detail},
	}
}

func (q *Qwen3TTS) synthesize(ctx context.Context, req SynthesisRequest, stream *audio.PCMStream) {
	runCtx, cancel := context.WithTimeout(ctx, q.timeout)
	defer cancel()
	if q.native {
		if err := q.synthesizeNativeStream(runCtx, req, stream); err != nil {
			stream.CloseWithError(managedWorkerError(err))
			return
		}
		stream.Close()
		return
	}
	if q.managed {
		// Managed path streams float32 PCM over JSONL (no temp WAV required).
		// Model generation is still whole-utterance; see worker.py comments.
		if err := q.synthesizeManagedStream(runCtx, req, stream); err != nil {
			stream.CloseWithError(managedWorkerError(err))
			return
		}
		stream.Close()
		return
	}

	tmpDir, err := os.MkdirTemp("", "samantha-qwen3-tts-")
	if err != nil {
		stream.CloseWithError(fmt.Errorf("qwen3-tts: create output directory: %w", err))
		return
	}
	defer os.RemoveAll(tmpDir)

	outputPath := filepath.Join(tmpDir, "speech.wav")
	textPath := filepath.Join(tmpDir, "text.txt")

	args, err := q.buildArgs(req, outputPath, textPath)
	if err != nil {
		stream.CloseWithError(err)
		return
	}
	cmd := q.command(runCtx, q.binary, args...)
	if cmd == nil {
		stream.CloseWithError(&ProviderError{Provider: qwen3TTSProviderName, Operation: "start worker", Kind: ProviderErrorWorker, Err: errors.New("command factory returned nil")})
		return
	}
	configureQwenCommand(cmd)
	var stdout, stderr limitedBuffer
	stdout.limit = maxWorkerOutput
	stderr.limit = maxWorkerOutput
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if ctx.Err() != nil {
			stream.CloseWithError(&ProviderError{Provider: qwen3TTSProviderName, Operation: "cancel worker", Kind: ProviderErrorCanceled, Err: ctx.Err()})
			return
		}
		if errors.Is(runCtx.Err(), context.DeadlineExceeded) {
			stream.CloseWithError(&ProviderError{Provider: qwen3TTSProviderName, Operation: "run worker", Kind: ProviderErrorWorker, Err: fmt.Errorf("worker timed out after %s", q.timeout)})
			return
		}
		stream.CloseWithError(&ProviderError{Provider: qwen3TTSProviderName, Operation: "run worker", Kind: ProviderErrorWorker, Err: fmt.Errorf("worker failed: %w%s", err, workerOutputSuffix(stderr.String(), stdout.String()))})
		return
	}
	if err := runCtx.Err(); err != nil {
		kind := ProviderErrorWorker
		if errors.Is(err, context.Canceled) {
			kind = ProviderErrorCanceled
		}
		stream.CloseWithError(&ProviderError{Provider: qwen3TTSProviderName, Operation: "run worker", Kind: kind, Err: err})
		return
	}

	q.streamWorkerWAV(ctx, outputPath, stream)
}

// synthesizeNativeStream serializes the product native worker and restarts once
// on unexpected process/protocol failure (same policy as managed).
func (q *Qwen3TTS) synthesizeNativeStream(ctx context.Context, req SynthesisRequest, stream *audio.PCMStream) error {
	q.sessionMu.Lock()
	defer q.sessionMu.Unlock()

	if !q.alive.Load() {
		return &ProviderError{Provider: qwen3TTSProviderName, Operation: "synthesize", Kind: ProviderErrorUnavailable, Err: errors.New("provider is closed")}
	}
	if err := q.ensureNativeSessionLocked(ctx); err != nil {
		return err
	}

	err := q.nativeSession.SynthesizeToStream(ctx, req, stream)
	if err == nil {
		return nil
	}
	q.discardNativeSessionLocked()
	if ctx.Err() != nil || !managedWorkerRestartable(err) {
		return err
	}
	if restartErr := q.ensureNativeSessionLocked(ctx); restartErr != nil {
		return fmt.Errorf("native worker failed (%v); restart failed: %w", err, restartErr)
	}
	if retryErr := q.nativeSession.SynthesizeToStream(ctx, req, stream); retryErr != nil {
		q.discardNativeSessionLocked()
		return fmt.Errorf("native worker failed after one restart: %w", retryErr)
	}
	return nil
}

func (q *Qwen3TTS) ensureNativeSessionLocked(ctx context.Context) error {
	if q.nativeSession != nil {
		return nil
	}
	timeout := q.startupTimeout
	if timeout <= 0 {
		timeout = max(q.timeout, 10*time.Minute)
	}
	session, err := startNativeQwenSession(ctx, q.binary, q.model, timeout)
	if err != nil {
		return fmt.Errorf("start native worker: %w", err)
	}
	q.nativeSession = session
	q.nativeSessionRef.Store(session)
	return nil
}

func (q *Qwen3TTS) discardNativeSessionLocked() {
	if q.nativeSession == nil {
		return
	}
	q.nativeSessionRef.Store(nil)
	q.nativeSession.Close()
	q.nativeSession = nil
}

// synthesizeManagedStream serializes access to the persistent model worker and
// owns its recovery policy. Cancellation invalidates only the worker process;
// the provider remains available and starts a fresh worker on the next request.
// Unexpected worker/protocol failures receive one immediate restart + retry so
// conversational callers reach their configured fallback only after Qwen has
// had a chance to recover itself.
func (q *Qwen3TTS) synthesizeManagedStream(ctx context.Context, req SynthesisRequest, stream *audio.PCMStream) error {
	q.sessionMu.Lock()
	defer q.sessionMu.Unlock()

	if !q.alive.Load() {
		return &ProviderError{Provider: qwen3TTSProviderName, Operation: "synthesize", Kind: ProviderErrorUnavailable, Err: errors.New("provider is closed")}
	}
	if err := q.ensureManagedSessionLocked(ctx); err != nil {
		return err
	}

	err := q.session.SynthesizeToStream(ctx, req, stream)
	if err == nil {
		return nil
	}
	q.discardManagedSessionLocked()
	if ctx.Err() != nil || !managedWorkerRestartable(err) {
		return err
	}

	if restartErr := q.ensureManagedSessionLocked(ctx); restartErr != nil {
		return fmt.Errorf("managed worker failed (%v); restart failed: %w", err, restartErr)
	}
	if retryErr := q.session.SynthesizeToStream(ctx, req, stream); retryErr != nil {
		q.discardManagedSessionLocked()
		return fmt.Errorf("managed worker failed after one restart: %w", retryErr)
	}
	return nil
}

func (q *Qwen3TTS) ensureManagedSessionLocked(ctx context.Context) error {
	if q.session != nil {
		return nil
	}
	timeout := q.startupTimeout
	if timeout <= 0 {
		timeout = max(q.timeout, 10*time.Minute)
	}
	session, err := startManagedQwenSessionContext(ctx, q.binary, q.workerScript, q.model, timeout)
	if err != nil {
		return fmt.Errorf("start managed worker: %w", err)
	}
	q.session = session
	return nil
}

func (q *Qwen3TTS) discardManagedSessionLocked() {
	if q.session == nil {
		return
	}
	q.session.Close()
	q.session = nil
}

func (q *Qwen3TTS) streamWorkerWAV(ctx context.Context, outputPath string, stream *audio.PCMStream) {
	samples, sampleRate, err := audio.ReadWAVFloat32(outputPath)
	if err != nil {
		stream.CloseWithError(&ProviderError{Provider: qwen3TTSProviderName, Operation: "read worker WAV", Kind: ProviderErrorMalformed, Err: err})
		return
	}
	if len(samples) == 0 {
		stream.CloseWithError(&ProviderError{Provider: qwen3TTSProviderName, Operation: "validate worker WAV", Kind: ProviderErrorMalformed, Err: errors.New("worker returned empty audio")})
		return
	}
	if sampleRate != qwen3TTSSampleRate {
		stream.CloseWithError(&ProviderError{Provider: qwen3TTSProviderName, Operation: "validate worker WAV", Kind: ProviderErrorMalformed, Err: fmt.Errorf("worker returned %d Hz, want %d Hz", sampleRate, qwen3TTSSampleRate)})
		return
	}
	if duration := time.Duration(float64(len(samples)) / float64(sampleRate) * float64(time.Second)); duration > maxQwenAudioDuration {
		stream.CloseWithError(&ProviderError{Provider: qwen3TTSProviderName, Operation: "validate worker WAV", Kind: ProviderErrorMalformed, Err: fmt.Errorf("worker returned %s of audio, maximum is %s", duration.Round(time.Second), maxQwenAudioDuration)})
		return
	}
	if err := stream.SetSampleRate(sampleRate); err != nil {
		stream.CloseWithError(err)
		return
	}

	const chunkSize = 2048
	for start := 0; start < len(samples); start += chunkSize {
		if err := ctx.Err(); err != nil {
			stream.CloseWithError(err)
			return
		}
		end := min(start+chunkSize, len(samples))
		if err := stream.Write(samples[start:end]); err != nil {
			stream.CloseWithError(err)
			return
		}
	}
	stream.Close()
}

// buildArgs is the one-shot argument seam used by the external compatibility
// adapter and managed-worker fixtures. Live managed synthesis uses the JSONL
// session so the model remains loaded.
func (q *Qwen3TTS) buildArgs(req SynthesisRequest, outputPath string, textPaths ...string) ([]string, error) {
	if q.managed {
		textPath := ""
		if len(textPaths) > 0 {
			textPath = textPaths[0]
		}
		if strings.TrimSpace(textPath) == "" {
			return nil, errors.New("qwen3-tts: managed worker requires a text file")
		}
		if err := ValidateRequest(q.Capabilities(), req); err != nil {
			return nil, err
		}
		args := []string{
			q.workerScript, "synthesize",
			"--model", q.model,
			"--text-file", textPath,
			"--output", outputPath,
			"--speaker", req.Voice,
			"--language", req.Language,
		}
		if strings.TrimSpace(req.Instruction) != "" {
			args = append(args, "--instruction", req.Instruction)
		}
		return args, nil
	}
	if req.Mode != "" && req.Mode != VoiceModeStatic {
		return nil, qwenUnsupported("voice mode", fmt.Sprintf("mode %q is not verified by the native CLI", req.Mode))
	}
	if strings.TrimSpace(req.Voice) != "" && !strings.EqualFold(req.Voice, "default") {
		return nil, qwenUnsupported("voice selection", fmt.Sprintf("voice %q is unsupported by the native CLI", req.Voice))
	}
	if req.Language != "" || req.Instruction != "" || req.ReferenceAudio != "" || req.ReferenceTranscript != "" {
		return nil, qwenUnsupported("voice controls", "the verified native CLI exposes no voice-mode flags")
	}
	return []string{"-m", q.model, "-t", req.Text, "-o", outputPath}, nil
}

func validateQwenReference(req SynthesisRequest) error {
	path := strings.TrimSpace(req.ReferenceAudio)
	if path == "" {
		if strings.TrimSpace(req.ReferenceTranscript) != "" {
			return &ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: ProviderErrorInput, Err: errors.New("reference transcript requires reference audio")}
		}
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return &ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: ProviderErrorInput, Err: fmt.Errorf("reference audio is unavailable")}
	}
	if !info.Mode().IsRegular() {
		return &ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: ProviderErrorInput, Err: errors.New("reference audio must be a regular file")}
	}
	if info.Size() > 50<<20 {
		return &ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: ProviderErrorInput, Err: errors.New("reference audio exceeds the 50 MiB limit")}
	}
	samples, rate, err := audio.ReadWAVFloat32(path)
	if err != nil || len(samples) == 0 || rate <= 0 {
		return &ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: ProviderErrorInput, Err: errors.New("reference audio must be a non-empty readable WAV")}
	}
	duration := time.Duration(float64(len(samples)) / float64(rate) * float64(time.Second))
	if duration < time.Second || duration > 30*time.Second {
		return &ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: ProviderErrorInput, Err: errors.New("reference audio duration must be between 1 and 30 seconds")}
	}
	if strings.TrimSpace(req.ReferenceTranscript) == "" {
		return &ProviderError{Provider: qwen3TTSProviderName, Operation: "validate reference", Kind: ProviderErrorInput, Err: errors.New("reference transcript is required")}
	}
	return nil
}

func workerOutputSuffix(stderr, stdout string) string {
	if detail := strings.TrimSpace(stderr); detail != "" {
		return ": " + detail
	}
	if detail := strings.TrimSpace(stdout); detail != "" {
		return ": " + detail
	}
	return ""
}

// Available returns true while this provider may start native workers.
func (q *Qwen3TTS) Available() bool { return q.alive.Load() }

// ListVoices reports speakers advertised by the managed/native handshake.
// The external CLI contract has no verified voice discovery mechanism.
func (q *Qwen3TTS) ListVoices(locale, gender string) []Voice {
	if !q.managed && !q.native {
		return nil
	}
	var voices []Voice
	for _, mode := range q.Capabilities().Modes {
		if mode.ID != VoiceModeCustomVoice {
			continue
		}
		for _, voice := range mode.Voices {
			if locale != "" && !strings.EqualFold(locale, voice.Locale) {
				continue
			}
			if gender != "" && !strings.EqualFold(gender, voice.Gender) {
				continue
			}
			voices = append(voices, voice)
		}
	}
	return voices
}

// Capabilities reports the managed/native worker handshake or the baseline
// guarantees of the external CLI adapter.
func (q *Qwen3TTS) Capabilities() ProviderCapabilities {
	caps := ProviderCapabilities{
		Provider:               qwen3TTSProviderName,
		Model:                  q.model,
		ModelReady:             q.model != "" && q.Available(),
		SampleRates:            []int{qwen3TTSSampleRate},
		SupportsCancellation:   true,
		SupportsReferenceAudio: q.native, // native stage A accepts ref_wav
		SupportsSpeed:          false,
	}
	if !q.managed && !q.native {
		return caps
	}
	q.sessionMu.Lock()
	defer q.sessionMu.Unlock()
	registry := managedqwen.CustomVoices()
	voiceNames := make([]string, 0, len(registry))
	switch {
	case q.native && q.nativeSession != nil && len(q.nativeSession.presets) > 0:
		voiceNames = append(voiceNames, q.nativeSession.presets...)
	case q.managed && q.session != nil && len(q.session.voices) > 0:
		voiceNames = append(voiceNames, q.session.voices...)
	case q.native:
		if names := loadNativePresetsFromDisk(q.model); len(names) > 0 {
			voiceNames = names
		} else {
			for _, voice := range registry {
				voiceNames = append(voiceNames, voice.Name)
			}
		}
	default:
		for _, voice := range registry {
			voiceNames = append(voiceNames, voice.Name)
		}
	}
	voices := make([]Voice, 0, len(voiceNames))
	for _, name := range voiceNames {
		voice := managedqwen.Voice{Name: name}
		for _, candidate := range registry {
			if strings.EqualFold(candidate.Name, name) {
				voice = candidate
				break
			}
		}
		friendly := voice.Description
		if friendly == "" {
			friendly = name
		}
		voices = append(voices, Voice{
			Name: voice.Name, FriendlyName: friendly,
			Gender: "preset", Locale: voice.NativeLanguage,
		})
	}
	caps.Modes = []VoiceModeCapability{{
		ID: VoiceModeCustomVoice, Voices: voices, SupportsInstruction: false,
		RequiresReferenceAudio: false,
	}}
	if q.managed && q.session != nil && len(q.session.languages) > 0 {
		caps.Languages = append([]string(nil), q.session.languages...)
	} else {
		caps.Languages = managedqwen.SupportedLanguages()
	}
	// Stage A: streaming=false at protocol layer; PCM still arrives as one chunk post-synth.
	if q.native && q.nativeSession != nil {
		caps.SupportsStreaming = q.nativeSession.streaming
	}
	caps.SupportsPreview = true
	return caps
}

func (q *Qwen3TTS) Status() ProviderStatus {
	available := q.Available()
	detail := "external native CLI and model configured"
	if q.native {
		detail = "native qwen3-tts-worker warm session ready"
	} else if q.managed {
		detail = "managed CustomVoice runtime and preset speakers ready"
	}
	if !available {
		detail = "provider is closed"
	} else if q.native {
		q.sessionMu.Lock()
		workerReady := q.nativeSession != nil
		q.sessionMu.Unlock()
		if !workerReady {
			detail = "native worker stopped; it will restart on the next request"
		}
	} else if q.managed {
		q.sessionMu.Lock()
		workerReady := q.session != nil
		q.sessionMu.Unlock()
		if !workerReady {
			detail = "managed worker stopped; it will restart on the next request"
		}
	}
	return ProviderStatus{Provider: qwen3TTSProviderName, Available: available, ModelReady: q.model != "" && available, Detail: detail}
}

// Delete prevents new work. Existing workers are still bounded by their
// request context; callers should cancel that context during shutdown.
func (q *Qwen3TTS) Delete() {
	q.alive.Store(false)
	q.sessionMu.Lock()
	defer q.sessionMu.Unlock()
	q.discardManagedSessionLocked()
	q.discardNativeSessionLocked()
}
