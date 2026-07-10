package brain

import (
	"slices"
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
		wantTools    []string
	}{
		{"disabled removes tools in default permission mode", false, claude.PermissionModeDefault, []string{""}},
		{"enabled uses bypass permissions and default tools", true, claude.PermissionModeBypassPermissions, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b := &Brain{cfg: &config.Config{}, systemPrompt: "persona"}
			opts := b.runOptions(claude.StreamJSONOutput, tt.toolsEnabled)
			if opts.PermissionMode != tt.wantMode {
				t.Fatalf("PermissionMode = %q, want %q", opts.PermissionMode, tt.wantMode)
			}
			if !slices.Equal(opts.Tools, tt.wantTools) {
				t.Fatalf("Tools = %#v, want %#v", opts.Tools, tt.wantTools)
			}
			if opts.SystemPrompt != "persona" {
				t.Fatalf("SystemPrompt lost: %q", opts.SystemPrompt)
			}
			if opts.Format != claude.StreamJSONOutput {
				t.Fatalf("Format = %q", opts.Format)
			}
			assertToolsArg(t, claude.BuildArgs("prompt", opts), !tt.toolsEnabled)
		})
	}
}

func TestGrokRunOptionsGateToolsOnVoiceToolsEnabled(t *testing.T) {
	tests := []struct {
		name          string
		toolsEnabled  bool
		wantMode      grok.PermissionMode
		wantDangerous bool
		wantTools     []string
	}{
		{"disabled removes tools in default mode", false, "", false, []string{""}},
		{"enabled uses bypass permissions with dangerous mode", true, grok.PermissionBypassPermissions, true, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			g := &GrokBrain{cfg: &config.Config{}, systemPrompt: "persona"}
			opts := g.runOptions(grok.PlainOutput, tt.toolsEnabled)
			if opts.PermissionMode != tt.wantMode {
				t.Fatalf("PermissionMode = %q, want %q", opts.PermissionMode, tt.wantMode)
			}
			if opts.AllowDangerousMode != tt.wantDangerous {
				t.Fatalf("AllowDangerousMode = %v, want %v", opts.AllowDangerousMode, tt.wantDangerous)
			}
			if !slices.Equal(opts.AllowedTools, tt.wantTools) {
				t.Fatalf("AllowedTools = %#v, want %#v", opts.AllowedTools, tt.wantTools)
			}
			if opts.SystemPromptOverride != "persona" {
				t.Fatalf("SystemPromptOverride lost: %q", opts.SystemPromptOverride)
			}
			assertToolsArg(t, grok.BuildArgs("prompt", opts), !tt.toolsEnabled)
		})
	}
}

func assertToolsArg(t *testing.T, args []string, wantDisabled bool) {
	t.Helper()
	i := slices.Index(args, "--tools")
	if !wantDisabled {
		if i >= 0 {
			t.Fatalf("unexpected --tools argument in enabled mode: %#v", args)
		}
		return
	}
	if i < 0 || i+1 >= len(args) || args[i+1] != "" {
		t.Fatalf("disabled mode args = %#v, want --tools followed by an empty value", args)
	}
}
