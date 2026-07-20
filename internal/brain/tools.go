package brain

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ollama/ollama/api"

	"github.com/lancekrogers/samantha/internal/skills"
)

// maxToolIterations prevents infinite tool call loops.
const maxToolIterations = 10

// skillBodyMaxBytes caps read_skill output (same budget as read_file).
const skillBodyMaxBytes = 32 * 1024

// defaultToolCommandTimeout bounds one shell command when no provider config
// is available (for example, in a direct unit-test tool session).
const defaultToolCommandTimeout = 30 * time.Second

// toolSession tracks catalog + active skill for progressive disclosure during
// a single Think* turn.
//
// Product model: skills (playbooks) select a tool allow-list after activation;
// the global voice_tools_enabled / remote_tools_enabled gates still control
// whether any tool can be called at all.
type toolSession struct {
	catalog []skills.Skill
	// active is set after a successful read_skill and controls the next
	// request's tool schemas and runtime allow-list.
	active *skills.Skill
	// commandTimeout bounds each run_command call in this session.
	commandTimeout time.Duration
	// Optional UI hooks (from StreamOptions).
	onStart func(name, summary string)
	onEnd   func(name, preview string)
}

// tools returns the full tool definitions for the next model request.
// Before a skill is activated, the model may choose from the base tools and
// read_skill. Once a skill declares allowed-tools, only that allow-list is
// advertised for subsequent requests (including read_skill — skill switching
// is not an escape hatch).
func (s *toolSession) tools() api.Tools {
	if s.active == nil || len(s.active.AllowedTools) == 0 {
		return voiceAssistantTools(s.catalog)
	}
	return filterTools(voiceAssistantTools(s.catalog), s.active.AllowedTools)
}

// execute runs a tool call. Successful read_skill activates the skill and
// applies its allow-list to subsequent tool calls.
// onEnd always runs (via defer) so a slow or failed tool cannot leave the TUI
// stuck on "🔧 tool..." without a finish event.
func (s *toolSession) execute(ctx context.Context, workDir string, call api.ToolCall) (result string) {
	name := call.Function.Name
	summary := toolArgSummary(call)
	if s.onStart != nil {
		s.onStart(name, summary)
	}
	defer func() {
		if s.onEnd != nil {
			s.onEnd(name, toolResultPreview(result))
		}
	}()
	// Enforce allow-list for every tool name, including read_skill, so a
	// restricted skill cannot be escaped by inventing another read_skill call.
	if s.active != nil && len(s.active.AllowedTools) > 0 && !skills.ToolAllowed(name, s.active.AllowedTools) {
		return fmt.Sprintf("error: tool %q is not allowed by active skill %q", name, s.active.Name)
	}
	if name == "read_skill" {
		return s.executeReadSkill(call)
	}
	return executeToolWithTimeout(ctx, workDir, call, s.catalog, s.commandTimeout)
}

// toolArgSummary is a short, non-sensitive description of tool arguments.
func toolArgSummary(call api.ToolCall) string {
	args := call.Function.Arguments.ToMap()
	switch call.Function.Name {
	case "list_files", "read_file", "write_file":
		if p, _ := args["path"].(string); p != "" {
			return p
		}
	case "run_command":
		if c, _ := args["command"].(string); c != "" {
			if len(c) > 60 {
				return c[:60] + "…"
			}
			return c
		}
	case "read_skill":
		if n, _ := args["name"].(string); n != "" {
			return n
		}
	}
	return ""
}

func toolResultPreview(result string) string {
	result = strings.TrimSpace(result)
	if result == "" {
		return "(empty)"
	}
	// One line, short.
	if i := strings.IndexByte(result, '\n'); i >= 0 {
		result = result[:i] + "…"
	}
	if len(result) > 80 {
		return result[:80] + "…"
	}
	return result
}

func (s *toolSession) executeReadSkill(call api.ToolCall) string {
	args := call.Function.Arguments.ToMap()
	name, _ := args["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return "error: name is required"
	}
	for i := range s.catalog {
		sk := &s.catalog[i]
		if sk.Name != name {
			continue
		}
		// Reject activation when allowed-tools is non-empty but maps to no
		// Samantha tools — otherwise the turn is soft-bricked with zero tools.
		if len(sk.AllowedTools) > 0 {
			if len(filterTools(voiceAssistantTools(s.catalog), sk.AllowedTools)) == 0 {
				return fmt.Sprintf(
					"error: skill %q allowed-tools %v match no Samantha tools; not activated",
					sk.Name, sk.AllowedTools,
				)
			}
		}
		s.active = sk
		body := sk.Body
		if len(body) > skillBodyMaxBytes {
			body = body[:skillBodyMaxBytes] + "\n... (truncated)"
		}
		msg := fmt.Sprintf("Skill %q (directory: %s)\n\n%s", sk.Name, sk.Dir, body)
		if len(sk.AllowedTools) > 0 {
			msg += fmt.Sprintf(
				"\n\n[skill policy: only these tools are available for this skill: %s]",
				strings.Join(sk.AllowedTools, " "),
			)
		}
		return msg
	}
	return fmt.Sprintf("error: unknown skill %q", name)
}

// voiceAssistantTools returns the tool definitions for the Ollama agent.
// When catalog is non-empty, read_skill is included so the model can load a
// skill body on demand (progressive disclosure).
func voiceAssistantTools(catalog []skills.Skill) api.Tools {
	props := func(fields map[string]api.ToolProperty) *api.ToolPropertiesMap {
		m := api.NewToolPropertiesMap()
		for k, v := range fields {
			m.Set(k, v)
		}
		return m
	}

	tools := api.Tools{
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "list_files",
				Description: "List files and directories at a given path. Returns names with / suffix for directories.",
				Parameters: api.ToolFunctionParameters{
					Type:     "object",
					Required: []string{"path"},
					Properties: props(map[string]api.ToolProperty{
						"path": {
							Type:        api.PropertyType{"string"},
							Description: "Directory path to list. Use '.' for current directory.",
						},
					}),
				},
			},
		},
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "read_file",
				Description: "Read the contents of a file. Returns the file text.",
				Parameters: api.ToolFunctionParameters{
					Type:     "object",
					Required: []string{"path"},
					Properties: props(map[string]api.ToolProperty{
						"path": {
							Type:        api.PropertyType{"string"},
							Description: "File path to read.",
						},
					}),
				},
			},
		},
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "write_file",
				Description: "Write content to a file. Creates the file if it doesn't exist, overwrites if it does.",
				Parameters: api.ToolFunctionParameters{
					Type:     "object",
					Required: []string{"path", "content"},
					Properties: props(map[string]api.ToolProperty{
						"path": {
							Type:        api.PropertyType{"string"},
							Description: "File path to write.",
						},
						"content": {
							Type:        api.PropertyType{"string"},
							Description: "Content to write to the file.",
						},
					}),
				},
			},
		},
		{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "run_command",
				Description: "Execute a shell command and return stdout and stderr. Runs in the user's working directory.",
				Parameters: api.ToolFunctionParameters{
					Type:     "object",
					Required: []string{"command"},
					Properties: props(map[string]api.ToolProperty{
						"command": {
							Type:        api.PropertyType{"string"},
							Description: "Shell command to execute.",
						},
					}),
				},
			},
		},
	}

	if len(catalog) > 0 {
		tools = append(tools, api.Tool{
			Type: "function",
			Function: api.ToolFunction{
				Name:        "read_skill",
				Description: "Load the full instructions for a named Agent Skill. Call this when a skill from the Available skills list is relevant, then follow its body. Bundled scripts live under the skill directory returned with the body. If the skill declares allowed-tools, only those tools remain available after loading.",
				Parameters: api.ToolFunctionParameters{
					Type:     "object",
					Required: []string{"name"},
					Properties: props(map[string]api.ToolProperty{
						"name": {
							Type:        api.PropertyType{"string"},
							Description: "Skill name as advertised in the Available skills list.",
						},
					}),
				},
			},
		})
	}
	return tools
}

// executeTool runs a tool call and returns the result as a string.
// catalog is used by read_skill; other tools ignore it.
// Prefer toolSession.execute so skill activation and policy are tracked.
func executeTool(ctx context.Context, workDir string, call api.ToolCall, catalog []skills.Skill) string {
	return executeToolWithTimeout(ctx, workDir, call, catalog, 0)
}

func executeToolWithTimeout(ctx context.Context, workDir string, call api.ToolCall, catalog []skills.Skill, commandTimeout time.Duration) string {
	args := call.Function.Arguments.ToMap()

	switch call.Function.Name {
	case "list_files":
		return toolListFiles(workDir, args)
	case "read_file":
		return toolReadFile(workDir, args)
	case "write_file":
		return toolWriteFile(workDir, args)
	case "run_command":
		return toolRunCommandWithTimeout(ctx, workDir, args, commandTimeout)
	case "read_skill":
		// Without a session, activation is not tracked (tests / legacy).
		return toolReadSkill(catalog, args)
	default:
		return fmt.Sprintf("unknown tool: %s", call.Function.Name)
	}
}

// toolReadSkill returns the capped skill body and skill directory for scripts.
// Does not track activation; use toolSession.execute for allow-list lifecycle.
func toolReadSkill(catalog []skills.Skill, args map[string]any) string {
	name, _ := args["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return "error: name is required"
	}
	for _, s := range catalog {
		if s.Name != name {
			continue
		}
		body := s.Body
		if len(body) > skillBodyMaxBytes {
			body = body[:skillBodyMaxBytes] + "\n... (truncated)"
		}
		return fmt.Sprintf("Skill %q (directory: %s)\n\n%s", s.Name, s.Dir, body)
	}
	return fmt.Sprintf("error: unknown skill %q", name)
}

func resolvePath(workDir, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	return filepath.Join(workDir, path)
}

func toolListFiles(workDir string, args map[string]any) string {
	path, _ := args["path"].(string)
	if path == "" {
		path = "."
	}
	resolved := resolvePath(workDir, path)

	entries, err := os.ReadDir(resolved)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}

	var names []string
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() {
			name += "/"
		}
		names = append(names, name)
	}
	return strings.Join(names, "\n")
}

func toolReadFile(workDir string, args map[string]any) string {
	path, _ := args["path"].(string)
	if path == "" {
		return "error: path is required"
	}
	resolved := resolvePath(workDir, path)

	data, err := os.ReadFile(resolved)
	if err != nil {
		return fmt.Sprintf("error: %v", err)
	}

	// Limit output to prevent context overflow.
	const maxBytes = 32 * 1024
	if len(data) > maxBytes {
		return string(data[:maxBytes]) + "\n... (truncated)"
	}
	return string(data)
}

func toolWriteFile(workDir string, args map[string]any) string {
	path, _ := args["path"].(string)
	content, _ := args["content"].(string)
	if path == "" {
		return "error: path is required"
	}
	resolved := resolvePath(workDir, path)

	dir := filepath.Dir(resolved)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Sprintf("error creating directory: %v", err)
	}

	if err := os.WriteFile(resolved, []byte(content), 0o644); err != nil {
		return fmt.Sprintf("error: %v", err)
	}
	return fmt.Sprintf("wrote %d bytes to %s", len(content), path)
}

func toolRunCommand(ctx context.Context, workDir string, args map[string]any) string {
	return toolRunCommandWithTimeout(ctx, workDir, args, 0)
}

func toolRunCommandWithTimeout(ctx context.Context, workDir string, args map[string]any, timeout time.Duration) string {
	command, _ := args["command"].(string)
	if command == "" {
		return "error: command is required"
	}

	timeout = clampCommandTimeout(timeout)
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = workDir

	output, err := cmd.CombinedOutput()
	result := string(output)
	if err != nil {
		result += fmt.Sprintf("\nexit error: %v", err)
	}

	// Limit output.
	const maxBytes = 16 * 1024
	if len(result) > maxBytes {
		result = result[:maxBytes] + "\n... (truncated)"
	}

	// Return as JSON for structured parsing.
	resp, _ := json.Marshal(map[string]string{
		"output":  result,
		"command": command,
	})
	return string(resp)
}

func filterTools(tools api.Tools, allowed []string) api.Tools {
	filtered := make(api.Tools, 0, len(tools))
	for _, tool := range tools {
		if skills.ToolAllowed(tool.Function.Name, allowed) {
			filtered = append(filtered, tool)
		}
	}
	return filtered
}

// clampCommandTimeout caps one run_command at 120s. Zero/negative fall back to
// the default 30s. Sub-second values are allowed (tests / callers with short
// bounds); user config is clamped to whole seconds in config.ClampToolCommandTimeout.
func clampCommandTimeout(timeout time.Duration) time.Duration {
	const maxTimeout = 120 * time.Second
	if timeout <= 0 {
		return defaultToolCommandTimeout
	}
	if timeout > maxTimeout {
		return maxTimeout
	}
	return timeout
}
