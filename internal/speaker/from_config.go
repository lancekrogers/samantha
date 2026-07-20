package speaker

import "github.com/lancekrogers/samantha/internal/config"

// FromAppConfig maps config.Speaker into speaker.Config.
func FromAppConfig(cfg *config.Config) Config {
	if cfg == nil {
		return Config{}.Normalize()
	}
	s := cfg.Speaker
	out := Config{
		Enabled:       s.Enabled,
		Threshold:     s.Threshold,
		EnrollmentDir: s.EnrollmentDir,
		Live: LiveConfig{
			Enabled:   s.Live.Enabled,
			Mode:      s.Live.Mode,
			Threshold: s.Live.Threshold,
			WindowMS:  s.Live.WindowMS,
		},
		Meeting: MeetingConfig{
			Enabled:     s.Meeting.Enabled,
			RecordAudio: s.Meeting.RecordAudio,
			NumSpeakers: s.Meeting.NumSpeakers,
		},
		Models: ModelsConfig{
			Embedding:    s.Models.Embedding,
			Segmentation: s.Models.Segmentation,
		},
	}
	return out.Normalize()
}
