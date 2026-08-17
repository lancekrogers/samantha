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
		// `speaker:` with nothing under it parses as null, not as a mapping.
		// That is an empty section, so fill it in rather than refusing.
		if existing.Tag == "!!null" && len(existing.Content) == 0 {
			existing.Kind = yaml.MappingNode
			existing.Tag = "!!map"
			existing.Value = ""
			existing.Style = 0
			return existing, nil
		}
		if existing.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("config key %q is not a section", key)
		}
		return existing, nil
	}
	child := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
	parent.Content = append(parent.Content, keyNode(key), child)
	return child, nil
}

// mappingValue finds a key case-insensitively, because viper lowercases every
// key it reads: a file holding TTS_Provider is read as tts_provider, and a
// case-sensitive writer would append a second, shadowed entry beside it.
func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	if mapping == nil || mapping.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if strings.EqualFold(mapping.Content[i].Value, key) {
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
		// Structured values (a destination list, say) are encoded by the yaml
		// package itself and spliced in, so an owner can write a key whose shape
		// this writer knows nothing about.
		node := &yaml.Node{}
		if err := node.Encode(value); err != nil {
			return nil, fmt.Errorf("encoding config value of type %T: %w", value, err)
		}
		return node, nil
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
