//go:build !integration

package meeting

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lancekrogers/samantha/internal/speaker"
)

// AnalysisStatus describes a post-capture speaker-analysis job.
type AnalysisStatus string

const (
	AnalysisDisabled AnalysisStatus = "disabled"
	AnalysisQueued   AnalysisStatus = "queued"
	AnalysisRunning  AnalysisStatus = "running"
	AnalysisComplete AnalysisStatus = "complete"
	AnalysisError    AnalysisStatus = "error"
)

// AnalysisResult is metadata only. Source PCM remains owned by the caller and
// the timeline contains no audio or embedding data.
type AnalysisResult struct {
	Status       AnalysisStatus   `json:"status"`
	Timeline     speaker.Timeline `json:"timeline"`
	Error        string           `json:"error,omitempty"`
	Artifact     string           `json:"artifact,omitempty"`
	AudioFile    string           `json:"audio_file,omitempty"`
	SpeakerCount int              `json:"speaker_count,omitempty"`
}

// SpeakerAnalyzer is the small boundary shared by real and deterministic
// speaker analyzers.
type SpeakerAnalyzer interface {
	Finalize(context.Context, []float32) (speaker.Timeline, error)
}

// AnalyzeRecording runs after capture has completed. It never mutates the
// source samples and returns a partial-safe result when the analyzer fails.
func AnalyzeRecording(ctx context.Context, analyzer SpeakerAnalyzer, samples []float32) AnalysisResult {
	if analyzer == nil {
		return AnalysisResult{Status: AnalysisDisabled}
	}
	if len(samples) == 0 {
		return AnalysisResult{Status: AnalysisError, Error: "no meeting audio captured for speaker analysis"}
	}
	result := AnalysisResult{Status: AnalysisRunning}
	timeline, err := analyzer.Finalize(ctx, samples)
	if err != nil {
		result.Status = AnalysisError
		result.Error = err.Error()
		return result
	}
	result.Status = AnalysisComplete
	result.Timeline = timeline.Clone()
	return result
}

// WriteAnalysis persists only the status/timeline JSON beside a meeting
// artifact. The write is atomic and private by default.
func WriteAnalysis(path string, result AnalysisResult) error {
	if path == "" {
		return fmt.Errorf("meeting: empty speaker analysis path")
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return fmt.Errorf("meeting: marshal speaker analysis: %w", err)
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("meeting: create speaker analysis directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("meeting: create speaker analysis: %w", err)
	}
	tmpName := tmp.Name()
	defer func() {
		_ = os.Remove(tmpName)
	}()
	if err := tmp.Chmod(0o600); err != nil {
		_ = tmp.Close()
		return err
	}
	if _, err := tmp.Write(append(data, '\n')); err != nil {
		_ = tmp.Close()
		return fmt.Errorf("meeting: write speaker analysis: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("meeting: close speaker analysis: %w", err)
	}
	if err := os.Rename(tmpName, path); err != nil {
		return fmt.Errorf("meeting: commit speaker analysis: %w", err)
	}
	return nil
}

// TranscriptSegment is a timestamped utterance to attribute.
type TranscriptSegment struct {
	ID      string `json:"id,omitempty"`
	StartMS int64  `json:"start_ms"`
	EndMS   int64  `json:"end_ms"`
	Text    string `json:"text"`
}

// AttributedSegment preserves the original transcript plus the selected
// observation. Unknown/rejected/overlap states are never guessed into a name.
type AttributedSegment struct {
	TranscriptSegment
	Speaker    string         `json:"speaker"`
	Confidence float32        `json:"confidence"`
	State      speaker.State  `json:"state"`
	Source     speaker.Source `json:"source,omitempty"`
}

// AttributeTranscript assigns a label by greatest timestamp overlap. A tie
// or an explicitly overlapping observation is represented as unknown/overlap.
func AttributeTranscript(segments []TranscriptSegment, timeline speaker.Timeline) []AttributedSegment {
	result := make([]AttributedSegment, len(segments))
	for i, segment := range segments {
		result[i] = AttributedSegment{TranscriptSegment: segment, Speaker: speaker.LabelUnknown, State: speaker.StateRejected}
		best := -1
		bestOverlap := int64(0)
		tie := false
		for j, obs := range timeline.Observations {
			overlap := min64(segment.EndMS, obs.EndMS) - max64(segment.StartMS, obs.StartMS)
			if overlap <= 0 {
				continue
			}
			if overlap > bestOverlap {
				best, bestOverlap, tie = j, overlap, false
			} else if overlap == bestOverlap {
				tie = true
			}
		}
		if best < 0 {
			continue
		}
		obs := timeline.Observations[best]
		if tie || obs.State == speaker.StateOverlap {
			result[i].State = speaker.StateOverlap
			result[i].Speaker = speaker.LabelUnknown
			result[i].Confidence = obs.Confidence
			result[i].Source = obs.Source
			continue
		}
		result[i].Speaker = obs.Label
		result[i].Confidence = obs.Confidence
		result[i].State = obs.State
		result[i].Source = obs.Source
	}
	return result
}

func min64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
