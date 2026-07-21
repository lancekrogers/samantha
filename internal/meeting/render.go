package meeting

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	meetinglog "github.com/lancekrogers/samantha/internal/meeting/log"
)

// Render builds a markdown document from a Summary + JSONL event stream.
// body is notes | full (default notes). Both scopes include the full
// transcript text so campaign intents and other sinks remain self-contained
// (local path pointers alone are not useful off-machine). Pure over the JSONL
// so route-later works on any past meeting.
func Render(summary meetinglog.Summary, body string) (RenderedNote, error) {
	if body == "" {
		body = BodyNotes
	}
	events, err := ReadEvents(summary.JSONLFile)
	if err != nil {
		return RenderedNote{}, err
	}
	return RenderEvents(summary, events, body), nil
}

// ReadEvents parses a meeting JSONL file into events (skips blank lines).
func ReadEvents(jsonlPath string) ([]meetinglog.Event, error) {
	if strings.TrimSpace(jsonlPath) == "" {
		return nil, fmt.Errorf("meeting: empty jsonl path")
	}
	f, err := os.Open(jsonlPath)
	if err != nil {
		return nil, fmt.Errorf("meeting: open jsonl: %w", err)
	}
	defer f.Close()

	var events []meetinglog.Event
	sc := bufio.NewScanner(f)
	// Meeting transcripts can have long utterances; allow large lines.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var e meetinglog.Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			return nil, fmt.Errorf("meeting: jsonl line %d: %w", lineNo, err)
		}
		events = append(events, e)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("meeting: read jsonl: %w", err)
	}
	return events, nil
}

// RenderEvents is the pure renderer (test-friendly).
func RenderEvents(summary meetinglog.Summary, events []meetinglog.Event, body string) RenderedNote {
	if body == "" {
		body = BodyNotes
	}
	title := IntentTitle(summary)
	var b strings.Builder

	b.WriteString("# ")
	b.WriteString(title)
	b.WriteString("\n\n")

	b.WriteString("**Started:** ")
	b.WriteString(formatTime(summary.StartedAt))
	b.WriteString("  \n")
	b.WriteString("**Ended:** ")
	b.WriteString(formatTime(summary.EndedAt))
	b.WriteString("  \n")
	b.WriteString("**Duration:** ")
	b.WriteString(summary.Duration().Round(time.Second).String())
	b.WriteString("  \n")
	fmt.Fprintf(&b, "**Utterances:** %d · **Notes:** %d · **Bookmarks:** %d\n\n",
		summary.Utterances, summary.Notes, summary.Bookmarks)

	if summary.File != "" {
		b.WriteString("_Local copy:_ `")
		b.WriteString(summary.File)
		b.WriteString("`\n\n")
	}

	var notes, bookmarks, utterances []meetinglog.Event
	for _, e := range events {
		switch e.Type {
		case meetinglog.TypeNote:
			notes = append(notes, e)
		case meetinglog.TypeBookmark:
			bookmarks = append(bookmarks, e)
		case meetinglog.TypeUtterance:
			utterances = append(utterances, e)
		}
	}

	if len(notes) > 0 {
		b.WriteString("## Notes\n\n")
		for _, e := range notes {
			fmt.Fprintf(&b, "- 📝 %s%s\n", offsetLabel(e.OffsetMs), e.Text)
		}
		b.WriteString("\n")
	}

	if len(bookmarks) > 0 {
		b.WriteString("## Bookmarks\n\n")
		for _, e := range bookmarks {
			label := e.Label
			if label == "" {
				label = "important"
			}
			line := fmt.Sprintf("- ★ %s%s", offsetLabel(e.OffsetMs), strings.ToUpper(label))
			if e.Text != "" {
				line += ": " + e.Text
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Always embed the transcript so routed intents/files are self-contained.
	// body=full is retained for compatibility; content is the same as notes.
	_ = body
	b.WriteString("## Transcript\n\n")
	if len(utterances) == 0 {
		b.WriteString("_No utterances recorded._\n")
	} else {
		for _, e := range utterances {
			fmt.Fprintf(&b, "- %s%s\n", offsetLabel(e.OffsetMs), e.Text)
		}
	}
	b.WriteString("\n")

	return RenderedNote{
		Title:       title,
		Body:        b.String(),
		Summary:     summary,
		BodyScope:   body,
		SourceJSONL: summary.JSONLFile,
		SourceLog:   summary.File,
	}
}

// IntentTitle builds the default campaign-intent title.
// Format: Meeting: <description> (<date>)
func IntentTitle(summary meetinglog.Summary) string {
	desc := strings.TrimSpace(summary.Description)
	if desc == "" {
		desc = "meeting"
	}
	day := summary.StartedAt
	if day.IsZero() {
		day = time.Now()
	}
	return fmt.Sprintf("Meeting: %s (%s)", desc, day.Format("2006-01-02"))
}

func formatTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.Format(time.RFC3339)
}

func offsetLabel(ms int64) string {
	if ms <= 0 {
		return ""
	}
	d := time.Duration(ms) * time.Millisecond
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	if h > 0 {
		return fmt.Sprintf("[%d:%02d:%02d] ", h, m, s)
	}
	return fmt.Sprintf("[%02d:%02d] ", m, s)
}
