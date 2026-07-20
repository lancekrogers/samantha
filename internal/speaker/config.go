package speaker

import "fmt"

// Config controls optional speaker analysis. All features default off so
// existing installs are unaffected until models and flags are set.
type Config struct {
	// Enabled is the master switch. When false, no models load and no workers start.
	Enabled bool `mapstructure:"enabled" json:"enabled"`

	Live    LiveConfig    `mapstructure:"live" json:"live"`
	Meeting MeetingConfig `mapstructure:"meeting" json:"meeting"`
	Models  ModelsConfig  `mapstructure:"models" json:"models"`

	// EnrollmentDir stores profiles + embeddings. Empty = default under config dir.
	EnrollmentDir string `mapstructure:"enrollment_dir" json:"enrollment_dir"`

	// Threshold is the default match cutoff (0–1 style scores).
	// Live inherits this when Live.Threshold is unset/zero; meeting Identify
	// uses this root value (not Live.Threshold).
	Threshold float32 `mapstructure:"threshold" json:"threshold"`
}

// LiveConfig is the async conversation path.
type LiveConfig struct {
	Enabled   bool    `mapstructure:"enabled" json:"enabled"`
	Mode      string  `mapstructure:"mode" json:"mode"` // indicator | owner_verify
	Threshold float32 `mapstructure:"threshold" json:"threshold"`
	WindowMS  int     `mapstructure:"window_ms" json:"window_ms"`
}

// MeetingConfig is offline meeting diarization.
type MeetingConfig struct {
	Enabled     bool `mapstructure:"enabled" json:"enabled"`
	RecordAudio bool `mapstructure:"record_audio" json:"record_audio"` // sidecar WAV
	// NumSpeakers is 0 for automatic clustering; >0 hints the clusterer.
	NumSpeakers int `mapstructure:"num_speakers" json:"num_speakers"`
}

// ModelsConfig holds paths under models_dir (or absolute).
type ModelsConfig struct {
	Embedding    string `mapstructure:"embedding" json:"embedding"`
	Segmentation string `mapstructure:"segmentation" json:"segmentation"`
}

// Live modes.
const (
	LiveModeIndicator   = "indicator"
	LiveModeOwnerVerify = "owner_verify"
)

// Normalize fills defaults for zero values without enabling the feature.
// Invalid Live.Mode is clamped to indicator.
func (c Config) Normalize() Config {
	if c.Threshold <= 0 {
		c.Threshold = 0.6
	}
	if c.Live.Threshold <= 0 {
		c.Live.Threshold = c.Threshold
	}
	if c.Live.WindowMS <= 0 {
		c.Live.WindowMS = 1500
	}
	switch c.Live.Mode {
	case "", LiveModeIndicator:
		c.Live.Mode = LiveModeIndicator
	case LiveModeOwnerVerify:
		// ok
	default:
		c.Live.Mode = LiveModeIndicator
	}
	return c
}

// Validate reports config errors that should fail analyzer construction.
// Unknown live modes are normalized away; this is for future hard failures.
func (c Config) Validate() error {
	c = c.Normalize()
	if c.Live.Mode != LiveModeIndicator && c.Live.Mode != LiveModeOwnerVerify {
		return fmt.Errorf("speaker: invalid live.mode %q", c.Live.Mode)
	}
	return nil
}

// AnyActive is true when master is on and at least one sub-path is enabled
// (the gate for requiring a non-nil Engine).
func (c Config) AnyActive() bool {
	return c.Enabled && (c.Live.Enabled || c.Meeting.Enabled)
}

// LiveActive is true when live analysis should run.
func (c Config) LiveActive() bool {
	c = c.Normalize()
	return c.Enabled && c.Live.Enabled
}

// MeetingActive is true when meeting diarization should run.
func (c Config) MeetingActive() bool {
	return c.Enabled && c.Meeting.Enabled
}

// LiveThreshold is the cutoff for live IdentifySegment after Normalize inherit.
func (c Config) LiveThreshold() float32 {
	return c.Normalize().Live.Threshold
}

// MeetingThreshold is the cutoff for meeting-side identification (root Threshold).
func (c Config) MeetingThreshold() float32 {
	return c.Normalize().Threshold
}
