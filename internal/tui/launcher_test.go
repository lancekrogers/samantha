package tui

import (
	"regexp"
	"strings"
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
)

func TestLauncherDisplaysConfiguredBrainModel(t *testing.T) {
	tests := []struct {
		name string
		cfg  *config.Config
		want string
	}{
		{
			name: "ollama",
			cfg:  &config.Config{BrainProvider: "ollama", OllamaModel: "llama3.2", TTSVoice: "af_heart"},
			want: "Model: llama3.2",
		},
		{
			name: "grok",
			cfg:  &config.Config{BrainProvider: "grok", GrokModel: "grok-build", TTSVoice: "af_heart"},
			want: "Model: grok-build",
		},
		{
			name: "default",
			cfg:  &config.Config{BrainProvider: "claude", TTSVoice: "af_heart"},
			want: "Model: default",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			view := stripANSI(newLauncher(tt.cfg, nil).View())
			if !strings.Contains(view, tt.want) {
				t.Fatalf("launcher view missing %q:\n%s", tt.want, view)
			}
		})
	}
}

var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func stripANSI(s string) string {
	return ansiPattern.ReplaceAllString(s, "")
}
