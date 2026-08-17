package config

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// setYAMLValue writes value at the dotted path, creating intermediate mappings
// (speaker.live.window_ms -> speaker: {live: {window_ms: …}}), preserving the
// document's comments and key order, and tagging the scalar by its Go type
// (!!bool, !!int, !!float, !!str) or emitting a sequence for []string.
//
// The typed tag is the point: a writer that always emitted !!str turned
// barge_in_enabled: true into the string "true" and vad_pre_roll_ms: 300 into
// "300", which then failed to unmarshal back into the Config struct.
func setYAMLValue(mapping *yaml.Node, path []string, value any) error {
	if len(path) == 0 {
		return fmt.Errorf("empty config key path")
	}
	node, err := yamlNodeFor(value)
	if err != nil {
		return err
	}
	parent := mapping
	for _, segment := range path[:len(path)-1] {
		parent, err = childMapping(parent, segment)
		if err != nil {
			return err
		}
	}
	leaf := path[len(path)-1]
	if existing := mappingValue(parent, leaf); existing != nil {
		// Keep whatever the user wrote around the value they are changing.
		node.HeadComment = existing.HeadComment
		node.LineComment = existing.LineComment
		node.FootComment = existing.FootComment
		*existing = *node
		return nil
	}
	parent.Content = append(parent.Content, keyNode(leaf), node)
	return nil
}

// yamlValueAt decodes the value at the dotted path, reporting whether the
// document holds it at all.
func yamlValueAt(mapping *yaml.Node, path []string) (any, bool) {
	node := mapping
	for i, segment := range path {
		if node.Kind != yaml.MappingNode {
			return nil, false
		}
		child := mappingValue(node, segment)
		if child == nil {
			return nil, false
		}
		if i == len(path)-1 {
			var out any
			if err := child.Decode(&out); err != nil {
				return nil, false
			}
			return out, true
		}
		node = child
	}
	return nil, false
}

// childMapping returns the mapping stored at key, creating an empty one when
// the key is absent. An existing non-mapping value is an error rather than
// something to overwrite: silently replacing a scalar with a map would discard
// user config.
func childMapping(parent *yaml.Node, key string) (*yaml.Node, error) {
	if existing := mappingValue(parent, key); existing != nil {
		if existing.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("config key %q is not a section", key)
		}
		return existing, nil
	}
	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content, keyNode(key), child)
	return child, nil
}

func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}

func keyNode(key string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}
}

func yamlNodeFor(value any) (*yaml.Node, error) {
	switch typed := value.(type) {
	case bool:
		return scalar("!!bool", strconv.FormatBool(typed)), nil
	case int:
		return scalar("!!int", strconv.FormatInt(int64(typed), 10)), nil
	case int64:
		return scalar("!!int", strconv.FormatInt(typed, 10)), nil
	case float64:
		return scalar("!!float", formatYAMLFloat(typed)), nil
	case string:
		return scalar("!!str", typed), nil
	case []string:
		node := &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq"}
		if len(typed) == 0 {
			node.Style = yaml.FlowStyle
		}
		for _, item := range typed {
			node.Content = append(node.Content, scalar("!!str", item))
		}
		return node, nil
	default:
		return nil, fmt.Errorf("unsupported config value type %T", value)
	}
}

func scalar(tag, value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
}

// formatYAMLFloat keeps a whole-numbered float looking like a float, so 1.0
// round-trips as 1.0 and not as the integer 1.
func formatYAMLFloat(f float64) string {
	text := strconv.FormatFloat(f, 'f', -1, 64)
	if !strings.ContainsAny(text, ".eE") {
		text += ".0"
	}
	return text
}

// preserveBlankLines re-attaches the blank lines yaml.v3 drops when it
// re-encodes a document. Without this, the first setting a front end writes
// collapses every paragraph break in a hand-edited config — comments survive
// but the file stops being readable.
//
// yaml.v3 emits a leading blank line for any head comment that starts with a
// newline, so the source is consulted for where the breaks were and the head
// comments are prefixed to match. Call before mutating: node line numbers refer
// to the source that was parsed.
func preserveBlankLines(node *yaml.Node, source []byte) {
	if node == nil || len(source) == 0 {
		return
	}
	lines := strings.Split(string(source), "\n")
	var walk func(n *yaml.Node)
	walk = func(n *yaml.Node) {
		if n.Kind == yaml.DocumentNode {
			for _, child := range n.Content {
				walk(child)
			}
			return
		}
		if n.Kind != yaml.MappingNode {
			return
		}
		for i := 0; i+1 < len(n.Content); i += 2 {
			key := n.Content[i]
			if blankLineAbove(lines, key.Line) && !strings.HasPrefix(key.HeadComment, "\n") {
				key.HeadComment = "\n" + key.HeadComment
			}
			walk(n.Content[i+1])
		}
	}
	walk(node)
}

// blankLineAbove reports whether the source had an empty line above the node at
// the given 1-based line, looking past the node's own comment block.
func blankLineAbove(lines []string, line int) bool {
	i := line - 2
	for i >= 0 && strings.HasPrefix(strings.TrimSpace(lines[i]), "#") {
		i--
	}
	return i >= 0 && strings.TrimSpace(lines[i]) == ""
}
