package meeting

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Campaign is one entry from `camp list --json` (registry campaign).
type Campaign struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Org    string `json:"org"`
	Path   string `json:"path"`
	Status string `json:"status"`
	Type   string `json:"type"`
}

// ListCampaigns runs `camp list --json` and parses the registry.
// Returns an empty slice (not an error) when camp is not on PATH.
func ListCampaigns(ctx context.Context, run Runner, look LookPath) ([]Campaign, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if run == nil {
		return nil, fmt.Errorf("meeting: ListCampaigns requires a Runner")
	}
	campBin := "camp"
	if look != nil {
		if p, err := look("camp"); err == nil && p != "" {
			campBin = p
		} else if err != nil {
			return nil, nil // camp not installed — nothing to discover
		}
	}
	out, err := run(ctx, campBin, "list", "--json")
	if err != nil {
		return nil, fmt.Errorf("meeting: camp list --json: %w", err)
	}
	return ParseCampaignListJSON(out)
}

// ParseCampaignListJSON decodes the array produced by `camp list --json`.
//
// camp may print human-readable registry verification lines on stdout before
// the JSON array (e.g. "✓ Registry cleaned: removed 1\n\n[…]"). Those prefixes
// start with multi-byte UTF-8 (checkmark → Go reports invalid character 'â').
// We extract the first JSON value from the payload so destination discovery
// still works after a registry clean.
func ParseCampaignListJSON(data []byte) ([]Campaign, error) {
	payload := extractJSONPayload(data)
	if len(payload) == 0 {
		return nil, nil
	}
	var camps []Campaign
	if err := json.Unmarshal(payload, &camps); err != nil {
		// Prefer a short preview so soft-fail UIs stay readable.
		preview := strings.TrimSpace(string(payload))
		if len(preview) > 80 {
			preview = preview[:80] + "…"
		}
		return nil, fmt.Errorf("meeting: parse camp list json: %w (got %q)", err, preview)
	}
	out := make([]Campaign, 0, len(camps))
	for _, c := range camps {
		name := strings.TrimSpace(c.Name)
		if name == "" {
			continue
		}
		c.Name = name
		out = append(out, c)
	}
	return out, nil
}

// extractJSONPayload returns the first top-level JSON array or object in data,
// or nil when none is found. Leading BOM and human log lines are skipped.
func extractJSONPayload(data []byte) []byte {
	data = bytes.TrimSpace(data)
	if len(data) >= 3 && data[0] == 0xef && data[1] == 0xbb && data[2] == 0xbf {
		data = bytes.TrimSpace(data[3:])
	}
	if len(data) == 0 {
		return nil
	}
	// Fast path: already pure JSON.
	if data[0] == '[' || data[0] == '{' {
		return data
	}
	// Find the first array or object start and take through the matching end.
	// Prefer '[' (camp list --json is an array); fall back to '{'.
	start := bytes.IndexByte(data, '[')
	if start < 0 {
		start = bytes.IndexByte(data, '{')
	}
	if start < 0 {
		return nil
	}
	slice := data[start:]
	end := jsonValueEnd(slice)
	if end < 0 {
		// Incomplete / not JSON — let Unmarshal report the problem.
		return slice
	}
	return slice[:end]
}

// jsonValueEnd returns the exclusive end index of the first complete JSON
// value in data (array or object), respecting strings and escapes. -1 if
// the structure is incomplete.
func jsonValueEnd(data []byte) int {
	if len(data) == 0 {
		return -1
	}
	open := data[0]
	var close byte
	switch open {
	case '[':
		close = ']'
	case '{':
		close = '}'
	default:
		return -1
	}
	depth := 0
	inString := false
	escape := false
	for i := 0; i < len(data); i++ {
		c := data[i]
		if inString {
			if escape {
				escape = false
				continue
			}
			if c == '\\' {
				escape = true
				continue
			}
			if c == '"' {
				inString = false
			}
			continue
		}
		switch c {
		case '"':
			inString = true
		case open:
			depth++
		case close:
			depth--
			if depth == 0 {
				return i + 1
			}
		}
	}
	return -1
}

// CampaignDestinationID builds a stable destination id for a discovered campaign.
func CampaignDestinationID(name string) string {
	return "camp:" + strings.TrimSpace(name)
}

// DestinationFromCampaign maps a registry campaign to a route target.
// Capture defaults to intent (camp idea add).
func DestinationFromCampaign(c Campaign) Destination {
	return Destination{
		ID:       CampaignDestinationID(c.Name),
		Type:     TypeCampaign,
		Campaign: c.Name,
		Capture:  "intent",
	}
}

// DestinationsFromCampaigns converts registry campaigns to destinations.
func DestinationsFromCampaigns(camps []Campaign) []Destination {
	out := make([]Destination, 0, len(camps))
	for _, c := range camps {
		out = append(out, DestinationFromCampaign(c))
	}
	return out
}

// MergeDestinations prefers configured destinations, then appends discovered
// ones that do not duplicate an existing campaign name or id.
func MergeDestinations(configured, discovered []Destination) []Destination {
	seenID := make(map[string]struct{}, len(configured)+len(discovered))
	seenCampaign := make(map[string]struct{}, len(configured)+len(discovered))
	out := make([]Destination, 0, len(configured)+len(discovered))
	for _, d := range configured {
		id := strings.TrimSpace(d.ID)
		if id != "" {
			seenID[id] = struct{}{}
		}
		if d.Type == TypeCampaign {
			if name := strings.TrimSpace(d.Campaign); name != "" {
				seenCampaign[strings.ToLower(name)] = struct{}{}
			}
		}
		out = append(out, d)
	}
	for _, d := range discovered {
		id := strings.TrimSpace(d.ID)
		if id != "" {
			if _, ok := seenID[id]; ok {
				continue
			}
		}
		if d.Type == TypeCampaign {
			name := strings.ToLower(strings.TrimSpace(d.Campaign))
			if name != "" {
				if _, ok := seenCampaign[name]; ok {
					continue
				}
				seenCampaign[name] = struct{}{}
			}
		}
		if id != "" {
			seenID[id] = struct{}{}
		}
		out = append(out, d)
	}
	return out
}

// DestinationLabel is a short picker/settings label for a destination.
func DestinationLabel(d Destination) string {
	switch d.Type {
	case TypeCampaign:
		name := strings.TrimSpace(d.Campaign)
		if name == "" {
			name = d.ID
		}
		return fmt.Sprintf("%s [campaign]", name)
	case TypeFile:
		path := strings.TrimSpace(d.Path)
		if path == "" {
			path = d.ID
		}
		return fmt.Sprintf("%s [file]", path)
	case TypeAppleNotes:
		folder := strings.TrimSpace(d.Folder)
		if folder == "" {
			folder = "Notes"
		}
		return fmt.Sprintf("%s [apple-notes]", folder)
	default:
		if d.ID != "" {
			return d.ID
		}
		return d.Type
	}
}
