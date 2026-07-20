package meeting

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// AppleNotesSink creates a note via osascript on macOS.
// On iOS the Go core returns OutcomeDelegated for the host client (not this sink).
type AppleNotesSink struct {
	Dest     Destination
	Run      Runner
	LookPath LookPath
}

func (s AppleNotesSink) Route(ctx context.Context, note RenderedNote) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if s.Run == nil {
		return Receipt{}, fmt.Errorf("meeting: apple-notes sink requires a Runner")
	}
	osa := "osascript"
	if s.LookPath != nil {
		if p, err := s.LookPath("osascript"); err == nil && p != "" {
			osa = p
		}
	}

	title := note.Title
	if title == "" {
		title = IntentTitle(note.Summary)
	}
	folder := strings.TrimSpace(s.Dest.Folder)

	// Build AppleScript with quoted string literals (double internal quotes).
	var script string
	if folder != "" {
		script = fmt.Sprintf(
			`tell application "Notes"
	set folderName to %s
	if not (exists folder folderName) then
		make new folder with properties {name:folderName}
	end if
	tell folder folderName
		make new note with properties {name:%s, body:%s}
	end tell
end tell`,
			asString(folder), asString(title), asString(note.Body),
		)
	} else {
		script = fmt.Sprintf(
			`tell application "Notes"
	make new note with properties {name:%s, body:%s}
end tell`,
			asString(title), asString(note.Body),
		)
	}

	out, err := s.Run(ctx, osa, "-e", script)
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return Receipt{}, fmt.Errorf("meeting: apple notes: %w (%s)", err, detail)
		}
		return Receipt{}, fmt.Errorf("meeting: apple notes: %w", err)
	}
	detail := "Apple Notes"
	if folder != "" {
		detail = "Apple Notes / " + folder
	}
	return Receipt{
		DestinationID: s.Dest.ID,
		Type:          TypeAppleNotes,
		Outcome:       OutcomeRouted,
		Detail:        detail,
		At:            time.Now(),
	}, nil
}

// asString returns an AppleScript double-quoted string literal.
func asString(s string) string {
	return `"` + strings.ReplaceAll(s, `"`, `\"`) + `"`
}
