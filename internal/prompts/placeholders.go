package prompts

import (
	"fmt"
	"regexp"
	"strings"
)

var placeholderPattern = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// Placeholder describes one brace token the persona/turn prompt assembly
// substitutes at brain construction time.
type Placeholder struct {
	Name        string // bare name, e.g. "agent_name"
	Description string // short help for the Personas editor completion UI
}

// PersonaPlaceholders is the catalog of variables authors may insert in
// persona and turn system prompts. Keep ResolvePlaceholders callers in sync
// via PlaceholderNames / PlaceholderValues.
//
// When adding a name: extend this list, supply a value in PlaceholderValues,
// and the Personas editor tab-completer + colorizer pick it up automatically.
var PersonaPlaceholders = []Placeholder{
	{
		Name:        "agent_name",
		Description: "Active persona display name (from the profile)",
	},
}

// PlaceholderNames returns the bare names from PersonaPlaceholders.
func PlaceholderNames() []string {
	out := make([]string, len(PersonaPlaceholders))
	for i, p := range PersonaPlaceholders {
		out[i] = p.Name
	}
	return out
}

// PlaceholderValues maps each persona placeholder to its runtime value for cfg.
// Unknown/empty agent names still produce a value so ResolvePlaceholders does
// not fail on an allowed but empty field.
func PlaceholderValues(agentName string) map[string]string {
	return map[string]string{
		"agent_name": agentName,
	}
}

// IsKnownPlaceholder reports whether name is in PersonaPlaceholders.
func IsKnownPlaceholder(name string) bool {
	name = strings.TrimSpace(name)
	for _, p := range PersonaPlaceholders {
		if p.Name == name {
			return true
		}
	}
	return false
}

// FilterPlaceholders returns catalog names that have prefix as a prefix
// (case-sensitive, matching the brace token grammar). Empty prefix returns all.
func FilterPlaceholders(prefix string) []string {
	names := PlaceholderNames()
	if prefix == "" {
		return names
	}
	var out []string
	for _, n := range names {
		if strings.HasPrefix(n, prefix) {
			out = append(out, n)
		}
	}
	return out
}

// ResolvePlaceholders substitutes brace tokens like {agent_name} in text.
// A token not in the allowed set, or an allowed token with no value, is an
// error — never silent passthrough.
func ResolvePlaceholders(text string, allowed []string, values map[string]string) (string, error) {
	allowedSet := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		allowedSet[name] = true
	}
	var resolveErr error
	out := placeholderPattern.ReplaceAllStringFunc(text, func(token string) string {
		if resolveErr != nil {
			return token
		}
		name := token[1 : len(token)-1]
		if !allowedSet[name] {
			resolveErr = fmt.Errorf("unknown placeholder {%s} (allowed: %s)", name, strings.Join(allowed, ", "))
			return token
		}
		value, ok := values[name]
		if !ok {
			resolveErr = fmt.Errorf("unresolved placeholder {%s}: no value provided", name)
			return token
		}
		return value
	})
	if resolveErr != nil {
		return "", resolveErr
	}
	return out, nil
}

// FindPlaceholders returns [start,end) byte ranges of complete {name} tokens
// on a single line (no newlines). end is exclusive.
func FindPlaceholders(line string) [][2]int {
	matches := placeholderPattern.FindAllStringIndex(line, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([][2]int, 0, len(matches))
	for _, m := range matches {
		out = append(out, [2]int{m[0], m[1]})
	}
	return out
}

// PlaceholderNameAt returns the bare name for a complete token covering col,
// or "" if col is not inside a {name} token.
func PlaceholderNameAt(line string, col int) string {
	for _, m := range placeholderPattern.FindAllStringSubmatchIndex(line, -1) {
		// m: full start, full end, name start, name end
		if col >= m[0] && col < m[1] {
			return line[m[2]:m[3]]
		}
	}
	return ""
}
