package brain

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"github.com/lancekrogers/samantha/internal/config"
	"github.com/lancekrogers/samantha/internal/prompts"
	"github.com/lancekrogers/samantha/internal/skills"
)

// personaSystemPrompt resolves the configured persona document and returns the
// assembled system prompt with {agent_name} substituted. A missing or invalid
// configured document is an error, so a bad persona surfaces at construction
// rather than mid-session.
func personaSystemPrompt(cfg *config.Config) (string, error) {
	return resolvePrompt(cfg, prompts.KindPersona)
}

// turnInstruction resolves the per-turn instruction appended to each user
// message on the Claude and Grok prompt paths.
func turnInstruction(cfg *config.Config) (string, error) {
	return resolvePrompt(cfg, prompts.KindTurn)
}

// resolvePrompt resolves a document of the given kind (explicit path, then the
// user prompts dir, then the embedded default), assembles it, and substitutes
// {agent_name}.
func resolvePrompt(cfg *config.Config, kind prompts.Kind) (string, error) {
	userDir := cfg.PromptsDir
	if userDir == "" {
		userDir = config.PromptsDir()
	}
	doc, err := prompts.Resolver{UserDir: userDir}.Resolve(kind, cfg.Persona)
	if err != nil {
		return "", fmt.Errorf("resolving %s prompt: %w", kind, err)
	}
	text, err := prompts.ResolvePlaceholders(doc.Assemble(), []string{"agent_name"}, map[string]string{"agent_name": cfg.AgentName})
	if err != nil {
		return "", fmt.Errorf("%s prompt %q: %w", kind, cfg.Persona, err)
	}
	return text, nil
}

// EnvironmentContext returns system context for grounding the model.
func EnvironmentContext(workDir string) string {
	user := os.Getenv("USER")
	hostname, _ := os.Hostname()
	return fmt.Sprintf(`
Environment:
- User: %s
- Working directory: %s
- Hostname: %s
- OS: %s/%s
- You have tools available: list_files, read_file, write_file, run_command
- All file paths are relative to the working directory unless absolute`, user, workDir, hostname, runtime.GOOS, runtime.GOARCH)
}

// SkillContext renders the progressive-disclosure skills menu for the Ollama
// system prompt: names and descriptions only. Empty catalog yields "".
// Descriptions are re-capped at skills.MaxDescriptionRunes so a pre-built
// catalog cannot blow the system prompt budget.
func SkillContext(catalog []skills.Skill) string {
	if len(catalog) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n## Available skills (call read_skill(\"<name>\") to load full instructions)\n")
	for _, s := range catalog {
		desc := skills.TruncateRunes(strings.TrimSpace(s.Description), skills.MaxDescriptionRunes)
		fmt.Fprintf(&b, "- %s: %s\n", s.Name, desc)
	}
	return b.String()
}
