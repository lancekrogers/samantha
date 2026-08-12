package speaker

import (
	"testing"

	"github.com/lancekrogers/samantha/pkg/voiceagent/config"
)

func TestFromAppConfigNumSpeakers(t *testing.T) {
	cfg := &config.Config{}
	cfg.Speaker.Meeting.NumSpeakers = 2
	got := FromAppConfig(cfg)
	if got.Meeting.NumSpeakers != 2 {
		t.Fatalf("NumSpeakers = %d, want 2", got.Meeting.NumSpeakers)
	}
}
