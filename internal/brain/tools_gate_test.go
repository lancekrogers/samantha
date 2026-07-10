package brain

import (
	"testing"

	"github.com/lancekrogers/claude-code-go/pkg/claude"
	"github.com/lancekrogers/grok-go-sdk/pkg/grok"

	"github.com/lancekrogers/samantha/internal/config"
)

func TestClaudeRunOptionsGateToolsOnVoiceToolsEnabled(t *testing.T) {
	tests := []struct {
		name         string
		toolsEnabled bool
		wantMode     claude.PermissionMode
	}{
		{"disabled uses default permission mode", false, claude.PermissionModeDefault},
		{"enabled uses bypass permissions", true, claude.PermissionModeBypassPermissions},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &Brain{cfg: &config.Config{}, systemPrompt: "persona"}
			opts := b.runOptions(claude.StreamJSONOutput, tt.toolsEnabled)
			if opts.PermissionMode != tt.wantMode {
				t.Fatalf("PermissionMode = %q, want %q", opts.PermissionMode, tt.wantMode)
			}
			if opts.SystemPrompt != "persona" {
				t.Fatalf("SystemPrompt lost: %q", opts.SystemPrompt)
			}
			if opts.Format != claude.StreamJSONOutput {
				t.Fatalf("Format = %q", opts.Format)
			}
		})
	}
}

func TestGrokRunOptionsGateToolsOnVoiceToolsEnabled(t *testing.T) {
	tests := []struct {
		name          string
		toolsEnabled  bool
		wantMode      grok.PermissionMode
		wantDangerous bool
	}{
		{"disabled leaves default mode and no dangerous flag", false, "", false},
		{"enabled uses bypass permissions with dangerous mode", true, grok.PermissionBypassPermissions, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &GrokBrain{cfg: &config.Config{VoiceToolsEnabled: tt.toolsEnabled}, systemPrompt: "persona"}
			opts := g.runOptions(grok.PlainOutput)
			if opts.PermissionMode != tt.wantMode {
				t.Fatalf("PermissionMode = %q, want %q", opts.PermissionMode, tt.wantMode)
			}
			if opts.AllowDangerousMode != tt.wantDangerous {
				t.Fatalf("AllowDangerousMode = %v, want %v", opts.AllowDangerousMode, tt.wantDangerous)
			}
			if opts.SystemPromptOverride != "persona" {
				t.Fatalf("SystemPromptOverride lost: %q", opts.SystemPromptOverride)
			}
		})
	}
}
