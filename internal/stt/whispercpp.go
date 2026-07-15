package stt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/lancekrogers/samantha/internal/audio"
	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/endpoint"
)

// WhisperCPPSTT implements utterance-final STT using whisper.cpp CLI.
type WhisperCPPSTT struct {
	cfg       *config.Config
	binary    string
	modelPath string
	vad       *audio.VAD
	capture   audioSource
}

// NewWhisperCPPSTT creates a whisper.cpp STT provider.
func NewWhisperCPPSTT(cfg *config.Config, capture audioSource, vad *audio.VAD) (*WhisperCPPSTT, error) {
	binaryPath, err := exec.LookPath(strings.TrimSpace(cfg.WhisperCPPBinary))
	if err != nil {
		return nil, fmt.Errorf("whisper.cpp binary %q not found in PATH", cfg.WhisperCPPBinary)
	}

	modelPath := strings.TrimSpace(cfg.WhisperCPPModelPath)
	if modelPath == "" {
		asset, err := config.WhisperCPPModelAsset(cfg.WhisperCPPModel)
		if err != nil {
			return nil, err
		}
		modelPath = asset.ModelPath(config.ModelsDir())
	}
	if _, err := os.Stat(modelPath); err != nil {
		return nil, fmt.Errorf("whisper.cpp model not found at %s", modelPath)
	}

	return &WhisperCPPSTT{
		cfg:       cfg,
		binary:    binaryPath,
		modelPath: modelPath,
		vad:       vad,
		capture:   capture,
	}, nil
}

// Start begins a whisper.cpp STT session. It reuses the shared utterance-final
// loop (runOfflineLoop) with the whisper.cpp CLI as the transcribe seam.
func (w *WhisperCPPSTT) Start(ctx context.Context) (Session, error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	session := &sherpaSession{
		cancel: cancel,
		events: make(chan Event, 8),
	}

	finite := sourceKind(w.capture) != audio.SourceLive
	deps := offlineLoopDeps{
		frames: asFrameSource(w.capture),
		seg:    newVADSegmenter(w.vad, preRollSamplesFromMS(w.cfg.VADPreRollMS)),
		policy: endpoint.FromConfig(w.cfg, finite),
		transcribe: func(samples []float32) (string, error) {
			return w.transcribe(sessionCtx, samples)
		},
	}

	go func() {
		defer close(session.events)
		runOfflineLoop(sessionCtx, deps, session.events)
	}()
	return session, nil
}

func (w *WhisperCPPSTT) transcribe(ctx context.Context, samples []float32) (string, error) {
	tempDir, err := os.MkdirTemp("", "samantha-whispercpp-*")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tempDir)

	audioPath := filepath.Join(tempDir, "input.wav")
	outputPrefix := filepath.Join(tempDir, "transcript")
	outputPath := outputPrefix + ".txt"

	if err := audio.WriteWAVFloat32(audioPath, audio.SampleRate, samples); err != nil {
		return "", fmt.Errorf("write temp wav: %w", err)
	}

	args := []string{
		"-m", w.modelPath,
		"-f", audioPath,
		"-of", outputPrefix,
		"-otxt",
		"-nt",
		"-np",
	}
	if lang := whisperCPPLanguage(w.cfg.Language); lang != "" {
		args = append(args, "-l", lang)
	}

	cmd := exec.CommandContext(ctx, w.binary, args...)
	if output, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("whisper.cpp: %w (%s)", err, strings.TrimSpace(string(output)))
	}

	data, err := os.ReadFile(outputPath)
	if err != nil {
		return "", fmt.Errorf("read whisper.cpp transcript: %w", err)
	}
	return normalizeTranscript(strings.TrimSpace(string(data))), nil
}

func whisperCPPLanguage(language string) string {
	language = strings.TrimSpace(language)
	if language == "" {
		return ""
	}
	parts := strings.Split(language, "-")
	return strings.ToLower(parts[0])
}

// Available returns true if whisper.cpp is ready.
func (w *WhisperCPPSTT) Available() bool {
	_, err := os.Stat(w.modelPath)
	return err == nil && w.binary != ""
}
