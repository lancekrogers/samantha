package meetingroute

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// FileSink copies the rendered markdown into a user-chosen directory.
type FileSink struct {
	Dest Destination
}

func (s FileSink) Route(ctx context.Context, note RenderedNote) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	dir := expandHome(strings.TrimSpace(s.Dest.Path))
	if dir == "" {
		return Receipt{}, fmt.Errorf("meetingroute: file destination %q has empty path", s.Dest.ID)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return Receipt{}, fmt.Errorf("meetingroute: create dest dir: %w", err)
	}
	name := exportFilename(note)
	path := filepath.Join(dir, name)
	// 0o600: meeting notes may contain private speech.
	if err := os.WriteFile(path, []byte(note.Body), 0o600); err != nil {
		return Receipt{}, fmt.Errorf("meetingroute: write note: %w", err)
	}
	return Receipt{
		DestinationID: s.Dest.ID,
		Type:          TypeFile,
		Outcome:       OutcomeRouted,
		Detail:        path,
		At:            time.Now(),
	}, nil
}

func exportFilename(note RenderedNote) string {
	slug := slugify(note.Summary.Description)
	ts := note.Summary.StartedAt
	if ts.IsZero() {
		ts = time.Now()
	}
	return fmt.Sprintf("%s-%s.md", slug, ts.Format("20060102-150405"))
}

func slugify(description string) string {
	var b strings.Builder
	dash := false
	for _, r := range strings.ToLower(description) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			dash = false
		default:
			if !dash && b.Len() > 0 {
				b.WriteByte('-')
				dash = true
			}
		}
	}
	s := strings.Trim(b.String(), "-")
	if len(s) > 60 {
		s = strings.Trim(s[:60], "-")
	}
	if s == "" {
		return "meeting"
	}
	return s
}

func expandHome(path string) string {
	if path == "" {
		return path
	}
	if path == "~" {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return home
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}
