package speaker

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

	// Threshold is the default Manager.Search/Verify cutoff (0–1 style scores).
	// Live may override via Live.Threshold.
	Threshold float32 `mapstructure:"threshold" json:"threshold"`
}

// LiveConfig is the async conversation path.
type LiveConfig struct {
	Enabled   bool    `mapstructure:"enabled" json:"enabled"`
	Mode      string  `mapstructure:"mode" json:"mode"` // indicator | owner_verify
	Threshold float32 `mapstructure:"threshold" json:"threshold"`
	WindowMS  int     `mapstructure:"window_ms" json:"window_ms"`
}

// MeetingConfig is offline / finalize diarization.
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
	if c.Live.Mode == "" {
		c.Live.Mode = LiveModeIndicator
	}
	return c
}

// EffectiveEnabled is true only when the master switch is on.
func (c Config) EffectiveEnabled() bool {
	return c.Enabled
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
