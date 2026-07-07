package prompts

import (
	"fmt"
	"regexp"
	"strings"
)

var placeholderPattern = regexp.MustCompile(`\{([A-Za-z_][A-Za-z0-9_]*)\}`)

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
