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
)

// maxToolIterations prevents infinite tool call loops.
const maxToolIterations = 10

// voiceAssistantTools returns the tool definitions for the Ollama agent.
func voiceAssistantTools() api.Tools {
	props := func(fields map[string]api.ToolProperty) *api.ToolPropertiesMap {
		m := api.NewToolPropertiesMap()
		for k, v := range fields {
			m.Set(k, v)
		}
		return m
	}

	return api.Tools{
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
}

// executeTool runs a tool call and returns the result as a string.
func executeTool(ctx context.Context, workDir string, call api.ToolCall) string {
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
	default:
		return fmt.Sprintf("unknown tool: %s", call.Function.Name)
	}
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
