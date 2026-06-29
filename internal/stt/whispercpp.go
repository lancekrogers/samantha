package stt

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/Obedience-Corp/samantha/internal/audio"
	"github.com/Obedience-Corp/samantha/internal/config"
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

// Start begins a whisper.cpp STT session.
func (w *WhisperCPPSTT) Start(ctx context.Context) (Session, error) {
	sessionCtx, cancel := context.WithCancel(ctx)
	session := &sherpaSession{
		cancel: cancel,
		events: make(chan Event, 8),
	}

	go w.runSession(sessionCtx, session.events)
	return session, nil
}

func (w *WhisperCPPSTT) runSession(ctx context.Context, events chan<- Event) {
	defer close(events)

	w.vad.Clear()
	lastPhaseAt := time.Now()
	emitPhase := func(phase string) {
		now := time.Now()
		events <- PhaseEvent{Phase: phase, Elapsed: now.Sub(lastPhaseAt).Nanoseconds()}
		lastPhaseAt = now
	}
	emitPhase("listening")

	listenDeadline := time.Now().Add(time.Duration(w.cfg.ListenTimeout) * time.Second)
	speechDetected := false
	speechStartedAt := time.Time{}

	for {
		select {
		case <-ctx.Done():
			events <- Failure{Err: ctx.Err()}
			return
		default:
		}

		if !speechDetected && time.Now().After(listenDeadline) {
			events <- Timeout{}
			return
		}

		chunk := w.capture.Read()
		if len(chunk) == 0 {
			time.Sleep(10 * time.Millisecond)
			continue
		}

		w.vad.AcceptWaveform(chunk)
		if !speechDetected && w.vad.IsSpeech() {
			speechDetected = true
			speechStartedAt = time.Now()
			emitPhase("hearing")
		}

		phraseExpired := !speechStartedAt.IsZero() &&
			time.Since(speechStartedAt) >= time.Duration(max(w.cfg.PhraseTimeLimit, 1))*time.Second
		if !w.vad.IsSpeechDetected() && !phraseExpired {
			continue
		}

		emitPhase("transcribing")

		var allSamples []float32
		for !w.vad.IsEmpty() {
			allSamples = append(allSamples, w.vad.Front()...)
			w.vad.Pop()
		}

		if len(allSamples) < minSpeechSamples {
			speechDetected = false
			speechStartedAt = time.Time{}
			listenDeadline = time.Now().Add(time.Duration(w.cfg.ListenTimeout) * time.Second)
			emitPhase("listening")
			continue
		}

		text, err := w.transcribe(ctx, allSamples)
		if err != nil {
			events <- Failure{Err: err}
			return
		}
		if text == "" {
			speechDetected = false
			speechStartedAt = time.Time{}
			listenDeadline = time.Now().Add(time.Duration(w.cfg.ListenTimeout) * time.Second)
			emitPhase("listening")
			continue
		}

		events <- FinalTranscript{Text: text}
		return
	}
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
