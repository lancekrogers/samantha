package meeting

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"
)

// CampaignSink creates a campaign intent (or note) via `camp idea add`.
type CampaignSink struct {
	Dest     Destination
	Run      Runner
	LookPath LookPath
}

func (s CampaignSink) Route(ctx context.Context, note RenderedNote) (Receipt, error) {
	if err := ctx.Err(); err != nil {
		return Receipt{}, err
	}
	if s.Run == nil {
		return Receipt{}, fmt.Errorf("meeting: campaign sink requires a Runner")
	}
	campaign := strings.TrimSpace(s.Dest.Campaign)
	if campaign == "" {
		return Receipt{}, fmt.Errorf("meeting: campaign destination %q missing campaign name", s.Dest.ID)
	}
	campBin := "camp"
	if s.LookPath != nil {
		if p, err := s.LookPath("camp"); err == nil && p != "" {
			campBin = p
		}
	}

	// Write body to a temp file so we don't depend on shell stdin quirks.
	tmp, err := os.CreateTemp("", "samantha-meeting-*.md")
	if err != nil {
		return Receipt{}, fmt.Errorf("meeting: temp body: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(note.Body); err != nil {
		_ = tmp.Close()
		return Receipt{}, fmt.Errorf("meeting: write temp body: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Receipt{}, fmt.Errorf("meeting: close temp body: %w", err)
	}

	title := note.Title
	if title == "" {
		title = IntentTitle(note.Summary)
	}
	capture := strings.TrimSpace(s.Dest.Capture)
	if capture == "" {
		capture = "intent"
	}

	args := []string{"idea", "add", title, "--body-file", tmpPath, "-c", campaign}
	if capture == "note" {
		args = append(args, "--note")
	}
	for _, tag := range s.Dest.Tags {
		if t := strings.TrimSpace(tag); t != "" {
			args = append(args, "--tag", t)
		}
	}

	out, err := s.Run(ctx, campBin, args...)
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return Receipt{}, fmt.Errorf("meeting: camp idea add: %w (%s)", err, detail)
		}
		return Receipt{}, fmt.Errorf("meeting: camp idea add: %w", err)
	}
	detail := strings.TrimSpace(string(out))
	if detail == "" {
		detail = fmt.Sprintf("campaign %s (%s)", campaign, capture)
	}
	return Receipt{
		DestinationID: s.Dest.ID,
		Type:          TypeCampaign,
		Outcome:       OutcomeRouted,
		Detail:        detail,
		At:            time.Now(),
	}, nil
}
