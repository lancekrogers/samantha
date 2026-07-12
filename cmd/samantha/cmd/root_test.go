//go:build !integration

package cmd

import (
	"os"
	"testing"

	"github.com/lancekrogers/samantha/internal/config"
)

// Piped stdin must route to the plain app.Run loop, never the full-screen
// TUI (D3: app.Run stays the driver for non-TTY invocations).
func TestStdinIsTerminalFalseForPipe(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	old := os.Stdin
	os.Stdin = r
	defer func() { os.Stdin = old }()

	if stdinIsTerminal() {
		t.Fatal("a pipe must not be detected as a terminal")
	}
}

func TestModelName(t *testing.T) {
	tests := []struct {
		name string
		cfg  config.Config
		want string
	}{
		{"claude", config.Config{BrainProvider: "claude"}, "claude"},
		{"grok with model", config.Config{BrainProvider: "grok", GrokModel: "grok-4"}, "grok-4"},
		{"grok default", config.Config{BrainProvider: "grok"}, "grok"},
		{"ollama", config.Config{BrainProvider: "ollama", OllamaModel: "llama3"}, "llama3"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := modelName(&tt.cfg); got != tt.want {
				t.Errorf("modelName() = %q, want %q", got, tt.want)
			}
		})
	}
}
