package meeting

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Capture modes for campaign destinations.
const (
	// CaptureMeeting imports via `camp idea notes import-meeting` into
	// notes/meetings/ (CI0009). This is the default and preferred path.
	CaptureMeeting = "meeting"
	// CaptureIntent is the legacy path: `camp idea add` lifecycle intent.
	// Kept for explicit opt-in only; misfiles meetings as triage work.
	CaptureIntent = "intent"
	// CaptureNote is the legacy path: `camp idea add --note` (non-meeting note).
	CaptureNote = "note"
)

// CampaignSink routes a meeting to a camp campaign.
//
// Default capture is "meeting": runs
//
//	camp idea notes import-meeting <bundle> --title … --summary-file … --json
//
// inside the target campaign root so notes land under notes/meetings/ with a
// transcript sidecar (Obedience-Corp/camp CI0009 / PR #537+#539).
//
// Legacy CaptureIntent / CaptureNote still shell out to `camp idea add`.
type CampaignSink struct {
	Dest     Destination
	Run      Runner
	LookPath LookPath
	// ResolveCampaignDir returns the absolute path for Dest.Campaign so
	// import-meeting can run with cmd.Dir set (camp loads config from cwd).
	// Production (Router.sinkFor) always injects resolveCampaignDir via
	// camp list. Tests inject a stub: empty string forces the injected Run
	// path (argv capture); a real path forces runCommand with that Dir.
	ResolveCampaignDir func(ctx context.Context, campaignName string) (string, error)
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

	capture := normalizeCampaignCapture(s.Dest.Capture)
	switch capture {
	case CaptureIntent, CaptureNote:
		return s.routeIdeaAdd(ctx, campBin, campaign, capture, note)
	default:
		return s.routeImportMeeting(ctx, campBin, campaign, note)
	}
}

func normalizeCampaignCapture(raw string) string {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", CaptureMeeting, "import-meeting", "import_meeting", "notes/meetings":
		return CaptureMeeting
	case CaptureIntent:
		return CaptureIntent
	case CaptureNote:
		return CaptureNote
	default:
		// Unknown values prefer the safe meetings importer.
		return CaptureMeeting
	}
}

func (s CampaignSink) routeImportMeeting(ctx context.Context, campBin, campaign string, note RenderedNote) (Receipt, error) {
	bundle := strings.TrimSpace(note.Summary.Bundle)
	if bundle == "" {
		// Fail closed: import-meeting needs the .meeting bundle. Route-later
		// callers must re-resolve Summary.Bundle from the meeting directory.
		return Receipt{}, fmt.Errorf("meeting: import-meeting requires Summary.Bundle (path to the .meeting directory); re-select the recording or use capture: note explicitly")
	}
	if _, err := os.Stat(bundle); err != nil {
		return Receipt{}, fmt.Errorf("meeting: meeting bundle %q: %w", bundle, err)
	}

	title := note.Title
	if title == "" {
		title = IntentTitle(note.Summary)
	}

	// import-meeting has no --tag flag (unlike idea add). Preserve configured
	// destination tags in the summary body so they are not silently dropped.
	summaryBody := note.Body
	if tags := nonEmptyTags(s.Dest.Tags); len(tags) > 0 {
		summaryBody = summaryBody + "\n\n**Route tags:** " + strings.Join(tags, ", ") + "\n"
	}

	tmp, err := os.CreateTemp("", "samantha-meeting-summary-*.md")
	if err != nil {
		return Receipt{}, fmt.Errorf("meeting: temp summary: %w", err)
	}
	tmpPath := tmp.Name()
	defer os.Remove(tmpPath)
	if _, err := tmp.WriteString(summaryBody); err != nil {
		_ = tmp.Close()
		return Receipt{}, fmt.Errorf("meeting: write temp summary: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return Receipt{}, fmt.Errorf("meeting: close temp summary: %w", err)
	}

	args := []string{
		"idea", "notes", "import-meeting", bundle,
		"--title", title,
		"--summary-file", tmpPath,
		"--json",
	}

	// import-meeting resolves the campaign from cwd (no -c flag yet).
	dir, err := s.campaignDir(ctx, campaign)
	if err != nil {
		return Receipt{}, err
	}

	out, err := s.runCamp(ctx, dir, campBin, args...)
	if err != nil {
		detail := strings.TrimSpace(string(out))
		if detail != "" {
			return Receipt{}, fmt.Errorf("meeting: camp idea notes import-meeting: %w (%s)", err, detail)
		}
		return Receipt{}, fmt.Errorf("meeting: camp idea notes import-meeting: %w", err)
	}
	detail := strings.TrimSpace(string(out))
	if detail == "" {
		detail = fmt.Sprintf("campaign %s (notes/meetings via import-meeting)", campaign)
	}
	return Receipt{
		DestinationID: s.Dest.ID,
		Type:          TypeCampaign,
		Outcome:       OutcomeRouted,
		Detail:        detail,
		At:            time.Now(),
	}, nil
}

func (s CampaignSink) routeIdeaAdd(ctx context.Context, campBin, campaign, capture string, note RenderedNote) (Receipt, error) {
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

	args := []string{"idea", "add", title, "--body-file", tmpPath, "-c", campaign}
	if capture == CaptureNote {
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

func (s CampaignSink) campaignDir(ctx context.Context, campaignName string) (string, error) {
	if s.ResolveCampaignDir != nil {
		return s.ResolveCampaignDir(ctx, campaignName)
	}
	return resolveCampaignDir(ctx, s.Run, s.LookPath, campaignName)
}

// resolveCampaignDir maps a registry campaign name/id to its absolute path.
func resolveCampaignDir(ctx context.Context, run Runner, look LookPath, campaignName string) (string, error) {
	campaignName = strings.TrimSpace(campaignName)
	if campaignName == "" {
		return "", fmt.Errorf("meeting: empty campaign name")
	}
	if filepath.IsAbs(campaignName) {
		return campaignName, nil
	}
	camps, err := ListCampaigns(ctx, run, look)
	if err != nil {
		return "", fmt.Errorf("meeting: resolve campaign %q: %w", campaignName, err)
	}
	want := strings.ToLower(campaignName)
	for _, c := range camps {
		nameMatch := strings.ToLower(c.Name) == want
		// Exact ID only (no prefix match — avoids ambiguous partial IDs).
		idMatch := strings.EqualFold(strings.TrimSpace(c.ID), campaignName)
		if nameMatch || idMatch {
			if p := strings.TrimSpace(c.Path); p != "" {
				return p, nil
			}
		}
	}
	return "", fmt.Errorf("meeting: campaign %q not found in camp list (need a registered path for import-meeting)", campaignName)
}

func nonEmptyTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		if s := strings.TrimSpace(t); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// runCamp runs camp. Non-empty dir sets cmd.Dir so import-meeting loads the
// correct campaign. Empty dir uses the injected Runner (unit tests asserting argv).
func (s CampaignSink) runCamp(ctx context.Context, dir, campBin string, args ...string) ([]byte, error) {
	if strings.TrimSpace(dir) == "" {
		return s.Run(ctx, campBin, args...)
	}
	return runCommand(ctx, dir, campBin, args...)
}
