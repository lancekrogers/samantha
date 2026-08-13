//go:build !integration

package meeting

import (
	"context"
	"errors"
	"fmt"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
	"github.com/lancekrogers/samantha/pkg/voiceagent/audio"
)

// AnalyzeBundleAudio diarizes a finished bundle's audio file and persists the
// analysis sidecar plus the additive human/JSONL sections — the same artifacts
// SpeakerSession.Finalize produces for a live desktop recording.
//
// It exists for recordings that arrived as a finished file rather than through
// a live capture subscription (serve-hosted remote meetings, reprocessing).
// Failures are returned but never destructive: the transcript and audio are
// already on disk and stay there.
func AnalyzeBundleAudio(ctx context.Context, analyzer SpeakerAnalyzer, writer *meetinglog.Writer, bundlePath, audioPath string) (AnalysisResult, error) {
	if analyzer == nil {
		return AnalysisResult{Status: AnalysisDisabled}, nil
	}
	if err := ctx.Err(); err != nil {
		return AnalysisResult{Status: AnalysisError, Error: err.Error()}, err
	}

	samples, _, err := audio.ReadWAVFloat32(audioPath)
	if err != nil {
		return AnalysisResult{Status: AnalysisError, Error: err.Error()},
			fmt.Errorf("read meeting audio: %w", err)
	}

	artifact := speakerArtifactPath(bundlePath)
	result := AnalyzeRecording(ctx, analyzer, samples)
	result.Artifact = artifact
	result.AudioFile = audioPath
	result.SpeakerCount = distinctSpeakerCount(result.Timeline)

	var persistErr error
	if err := WriteAnalysis(artifact, result); err != nil {
		persistErr = errors.Join(persistErr, err)
	}
	if writer != nil {
		if err := writer.WriteSpeakerAnalysis(logAnalysis(result, writer.Transcripts())); err != nil {
			persistErr = errors.Join(persistErr, err)
		}
	}
	if persistErr != nil {
		result.Error = joinErrorText(result.Error, persistErr)
		if result.Status == AnalysisComplete {
			result.Status = AnalysisError
		}
	}
	return result, persistErr
}
