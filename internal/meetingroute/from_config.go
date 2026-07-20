package meetingroute

import "github.com/lancekrogers/samantha/internal/config"

// FromConfig maps config.Meeting into a routing Config.
func FromConfig(cfg *config.Config) Config {
	if cfg == nil {
		return Config{}.Normalize()
	}
	mc := cfg.Meeting.Route
	out := Config{
		Mode:    mc.Mode,
		Default: mc.Default,
		Body:    mc.Body,
	}
	for _, d := range mc.Destinations {
		out.Destinations = append(out.Destinations, Destination{
			ID:       d.ID,
			Type:     d.Type,
			Campaign: d.Campaign,
			Capture:  d.Capture,
			Tags:     d.Tags,
			Path:     d.Path,
			Folder:   d.Folder,
		})
	}
	return out.Normalize()
}
