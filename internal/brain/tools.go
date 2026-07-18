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

// toolSession tracks catalog + active skill for progressive disclosure during
// a single Think* turn.
//
// Product model: skills (playbooks) stack on top of CLI tools (capability).
// allowed-tools from SKILL.md is author *hint* only — it is surfaced when a
// skill loads, but does not strip or deny list_files/read_file/write_file/
// run_command. Real safety stays at voice_tools_enabled / remote_tools_enabled.
type toolSession struct {
	catalog []skills.Skill
	// active is set after a successful read_skill (for hints / future policy).
	active *skills.Skill
}

// tools returns the full tool definitions for the next model request.
// CLI tools stay available for the whole turn, including after skill load.
func (s *toolSession) tools() api.Tools {
	return voiceAssistantTools(s.catalog)
}

// execute runs a tool call. Successful read_skill activates the skill and
// may append an allowed-tools hint; CLI tools remain fully available.
func (s *toolSession) execute(ctx context.Context, workDir string, call api.ToolCall) string {
	if call.Function.Name == "read_skill" {
		return s.executeReadSkill(call)
	}
	return executeTool(ctx, workDir, call, s.catalog)
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
		s.active = sk
		body := sk.Body
		if len(body) > skillBodyMaxBytes {
			body = body[:skillBodyMaxBytes] + "\n... (truncated)"
		}
		msg := fmt.Sprintf("Skill %q (directory: %s)\n\n%s", sk.Name, sk.Dir, body)
		if len(sk.AllowedTools) > 0 {
			// Hint only — CLI tools are not restricted by this list.
			msg += fmt.Sprintf(
				"\n\n[skill hint: allowed-tools suggests %s — CLI tools remain available; this is not a hard sandbox]",
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
				Description: "Load the full instructions for a named Agent Skill. Call this when a skill from the Available skills list is relevant, then follow its body. Bundled scripts live under the skill directory returned with the body. CLI tools (list_files, read_file, write_file, run_command) stay available after loading. Optional allowed-tools in the skill is a hint only, not a sandbox.",
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
// Prefer toolSession.execute so skill activation (and hints) are tracked.
func executeTool(ctx context.Context, workDir string, call api.ToolCall, catalog []skills.Skill) string {
	args := call.Function.Arguments.ToMap()

	switch call.Function.Name {
	case "list_files":
		return toolListFiles(workDir, args)
	case "read_file":
		return toolReadFile(workDir, args)
	case "write_file":
		return toolWriteFile(workDir, args)
	case "run_command":
		return toolRunCommand(ctx, workDir, args)
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
	command, _ := args["command"].(string)
	if command == "" {
		return "error: command is required"
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
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
