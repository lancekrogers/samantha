// Package ideas resolves spoken idea spans into filed Inbox intents.
//
// A span is bracketed by idea_start/idea_end control events (Label = the
// client's span id). After transcription, the utterances overlapping the span
// become the idea's text and the span is filed through the injected FileFunc —
// the same intent sink `POST /v1/intent` writes to, so a spoken idea and a
// typed one land identically.
//
// Everything here is re-run safe: a filed span gets a TypeIdeaFiled marker in
// the bundle, and later passes skip marked spans. A pipeline that failed after
// filing two of three spans files exactly the third on reprocess.
package ideas

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// Resolved is one span ready to file.
type Resolved struct {
	SpanID  string
	StartMS int64
	EndMS   int64
	// Body is the transcript slice, prefixed by any text the client attached
	// to idea_end (the typed-context fallback).
	Body string
}

// Report says what a resolution pass did, for logs and the bundle note.
type Report struct {
	Filed        int
	AlreadyFiled int
	// Unresolved spans had no speech in their window; they are surfaced as a
	// bundle note rather than silently dropped.
	Unresolved int
	// Failed spans hit a filing error and stay unmarked for the next pass.
	Failed int
	// MarkerFailed counts filed spans whose idea_filed marker could not be
	// persisted. The filing itself is safe — the sink's deterministic
	// create-if-absent key is the durable receipt — but the miss is reported
	// rather than swallowed so operators see the bundle is missing markers.
	MarkerFailed int
}

// FileFunc files one resolved idea. An error leaves the span unmarked so a
// later pass retries it.
type FileFunc func(ctx context.Context, idea Resolved) error

// Resolve reads the bundle's events, pairs idea spans, slices the transcript,
// and files unfiled spans. The writer must still be open: filed markers and
// unresolved notes are appended through it.
func Resolve(ctx context.Context, bundlePath string, writer *meetinglog.Writer, file FileFunc) (Report, error) {
	if err := ctx.Err(); err != nil {
		return Report{}, err
	}
	if file == nil {
		return Report{}, fmt.Errorf("ideas: FileFunc is required")
	}
	events, err := readEvents(filepath.Join(bundlePath, meetinglog.BundleInternalDirName, meetinglog.BundleEventsName))
	if err != nil {
		return Report{}, err
	}

	spans, filed := pairSpans(events)
	var report Report
	for _, span := range spans {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		if filed[span.SpanID] {
			report.AlreadyFiled++
			continue
		}
		span.Body = sliceTranscript(events, span)
		if strings.TrimSpace(span.Body) == "" {
			report.Unresolved++
			// Best-effort note; the count above is the authoritative signal.
			if err := writer.AppendControl(meetinglog.TypeNote, span.EndMS, "idea_unresolved",
				fmt.Sprintf("idea span %s (%s–%s) had no speech; nothing was filed",
					span.SpanID, clock(span.StartMS), clock(span.EndMS))); err != nil {
				report.MarkerFailed++
			}
			continue
		}
		if err := file(ctx, span); err != nil {
			// Leave the span unmarked: the next pass retries. The meeting
			// itself must not fail over one intent.
			report.Failed++
			continue
		}
		report.Filed++
		// The marker is the fast-path skip for re-runs; the durable dedupe
		// lives in the sink's deterministic key. A marker failure therefore
		// degrades performance, not correctness — but it is counted, not
		// ignored: a bundle whose markers keep failing has a real problem.
		if err := writer.AppendControl(meetinglog.TypeIdeaFiled, span.EndMS, span.SpanID, preview(span.Body)); err != nil {
			report.MarkerFailed++
		}
	}
	return report, nil
}

// pairSpans matches idea_start/idea_end by label; unlabeled events pair FIFO.
// A start without an end runs to the last event offset — the user marked a
// moment and forgot to close it, and a generous slice beats a lost idea.
func pairSpans(events []meetinglog.Event) (spans []Resolved, filed map[string]bool) {
	filed = make(map[string]bool)
	open := make(map[string]int64)
	var openOrder []string
	var lastOffset int64
	anonymous := 0

	for _, event := range events {
		lastOffset = max(lastOffset, event.OffsetMs, event.EndMS)
		switch event.Type {
		case meetinglog.TypeIdeaStart:
			id := event.Label
			if id == "" {
				anonymous++
				id = fmt.Sprintf("span-%d", anonymous)
			}
			if _, dup := open[id]; !dup {
				open[id] = event.OffsetMs
				openOrder = append(openOrder, id)
			}
		case meetinglog.TypeIdeaEnd:
			id := event.Label
			if id == "" && len(openOrder) > 0 {
				id = openOrder[0]
			}
			start, ok := open[id]
			if !ok {
				continue // end without a start: nothing to slice
			}
			delete(open, id)
			openOrder = remove(openOrder, id)
			spans = append(spans, Resolved{SpanID: id, StartMS: start, EndMS: event.OffsetMs, Body: event.Text})
		case meetinglog.TypeIdeaFiled:
			filed[event.Label] = true
		}
	}
	// Unclosed spans run to the end of the meeting.
	for _, id := range openOrder {
		spans = append(spans, Resolved{SpanID: id, StartMS: open[id], EndMS: lastOffset})
	}
	sort.Slice(spans, func(i, j int) bool { return spans[i].StartMS < spans[j].StartMS })
	return spans, filed
}

// sliceTranscript joins utterances overlapping the span window, after any
// typed text the client attached to idea_end.
func sliceTranscript(events []meetinglog.Event, span Resolved) string {
	var parts []string
	if t := strings.TrimSpace(span.Body); t != "" {
		parts = append(parts, t)
	}
	for _, event := range events {
		if event.Type != meetinglog.TypeUtterance {
			continue
		}
		if event.EndMS < span.StartMS || event.StartMS > span.EndMS {
			continue
		}
		if t := strings.TrimSpace(event.Text); t != "" {
			parts = append(parts, t)
		}
	}
	return strings.Join(parts, " ")
}

func readEvents(path string) ([]meetinglog.Event, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("ideas: open events: %w", err)
	}
	defer f.Close()
	var events []meetinglog.Event
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1<<16), 1<<22)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var event meetinglog.Event
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			continue // tolerate unknown or partial lines; they are not ours
		}
		events = append(events, event)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("ideas: read events: %w", err)
	}
	return events, nil
}

func remove(list []string, id string) []string {
	for i, v := range list {
		if v == id {
			return append(list[:i], list[i+1:]...)
		}
	}
	return list
}

func preview(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 120 {
		return s[:120] + "…"
	}
	return s
}

func clock(ms int64) string {
	seconds := ms / 1000
	return fmt.Sprintf("%d:%02d", seconds/60, seconds%60)
}
