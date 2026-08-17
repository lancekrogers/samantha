package config

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"gopkg.in/yaml.v3"
)

// coerceToSpec turns a command-line string into the Go value the key's schema
// type calls for. It replaces the old "infer the type from whatever is already
// in the file" rule, which had no case for float32 keys and none for lists.
func coerceToSpec(spec KeySpec, raw string) (any, error) {
	switch spec.Type {
	case TypeBool:
		value, err := strconv.ParseBool(strings.TrimSpace(raw))
		if err != nil {
			return nil, invalidValue(spec, "expects true or false", raw)
		}
		return value, nil
	case TypeInt:
		value, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
		if err != nil {
			return nil, invalidValue(spec, "expects "+numberPhrase(spec, "a whole number"), raw)
		}
		return value, checkRange(spec, float64(value), raw)
	case TypeFloat:
		value, err := strconv.ParseFloat(strings.TrimSpace(raw), 64)
		if err != nil {
			return nil, invalidValue(spec, "expects "+numberPhrase(spec, "a number"), raw)
		}
		return value, checkRange(spec, value, raw)
	case TypeEnum:
		return canonicalEnum(spec, raw)
	case TypeStringList:
		return parseStringList(spec, raw)
	default:
		return raw, nil
	}
}

// normalizeToSpec validates an already-typed value from an in-process caller
// against the key's schema type, so the TUI and the CLI cannot write two
// different shapes for one key.
func normalizeToSpec(spec KeySpec, value any) (any, error) {
	if raw, ok := value.(string); ok && spec.Type != TypeString && spec.Type != TypeEnum {
		return coerceToSpec(spec, raw)
	}
	switch spec.Type {
	case TypeBool:
		asBool, ok := value.(bool)
		if !ok {
			return nil, invalidValue(spec, "expects true or false", fmt.Sprint(value))
		}
		return asBool, nil
	case TypeInt:
		number, ok := toFloat(value)
		if !ok || number != math.Trunc(number) {
			return nil, invalidValue(spec, "expects "+numberPhrase(spec, "a whole number"), fmt.Sprint(value))
		}
		return int64(number), checkRange(spec, number, fmt.Sprint(value))
	case TypeFloat:
		number, ok := toFloat(value)
		if !ok {
			return nil, invalidValue(spec, "expects "+numberPhrase(spec, "a number"), fmt.Sprint(value))
		}
		return number, checkRange(spec, number, fmt.Sprint(value))
	case TypeEnum, TypeString:
		text, ok := value.(string)
		if !ok {
			return nil, invalidValue(spec, "expects text", fmt.Sprint(value))
		}
		if spec.Type == TypeEnum {
			return canonicalEnum(spec, text)
		}
		return text, nil
	case TypeStringList:
		list, ok := toStringSlice(value)
		if !ok {
			return nil, invalidValue(spec, "expects a list of strings", fmt.Sprint(value))
		}
		return list, nil
	case TypeOpaque:
		// An opaque key's shape is known only to the code that owns it (today
		// meeting.route.destinations, a list of destination structs). There is
		// nothing to validate here, so the value is passed through and encoded
		// by the YAML writer. The string CLI path never reaches this: it
		// refuses opaque keys as not_editable.
		if value == nil {
			return nil, invalidValue(spec, "expects a value", "nil")
		}
		return value, nil
	default:
		return nil, invalidValue(spec, "cannot be written directly", fmt.Sprint(value))
	}
}

// canonicalEnum matches case-insensitively and stores the canonical casing. An
// enum whose values are configured data rather than contract (today only
// meeting.route.default, whose options are the ids of destinations in the file)
// ships an empty list and accepts any text.
func canonicalEnum(spec KeySpec, raw string) (any, error) {
	value := strings.TrimSpace(raw)
	// "" is a listed state, not a missing one, for a key whose default is "" —
	// it means "use the built-in behaviour". Refusing it left `stt_mode` with no
	// way back to its own default short of hand-editing config.yaml.
	if value == "" && spec.AllowsEmpty {
		return "", nil
	}
	if len(spec.Enum) == 0 {
		return value, nil
	}
	for _, candidate := range spec.Enum {
		if strings.EqualFold(candidate, value) {
			return candidate, nil
		}
	}
	return nil, invalidValue(spec, "expects one of "+strings.Join(spec.Enum, ", "), raw)
}

// parseStringList reads the argument as a JSON document: the whole array is the
// value, so a list is set in one write rather than by append-and-hope.
func parseStringList(spec KeySpec, raw string) (any, error) {
	var list []string
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &list); err != nil {
		return nil, invalidValue(spec, `expects a JSON array of strings, e.g. ["pdf-fill","calibre"]`, raw)
	}
	if list == nil {
		list = []string{}
	}
	return list, nil
}

func checkRange(spec KeySpec, value float64, raw string) error {
	if spec.Min != nil && value < *spec.Min {
		return invalidValue(spec, "expects "+numberPhrase(spec, "a number"), raw)
	}
	if spec.Max != nil && value > *spec.Max {
		return invalidValue(spec, "expects "+numberPhrase(spec, "a number"), raw)
	}
	return nil
}

// numberPhrase names the accepted range so one message covers both a parse
// failure and an out-of-range value.
func numberPhrase(spec KeySpec, noun string) string {
	switch {
	case spec.Min != nil && spec.Max != nil:
		return fmt.Sprintf("%s between %s and %s", noun, formatBound(*spec.Min), formatBound(*spec.Max))
	case spec.Min != nil:
		return fmt.Sprintf("%s of at least %s", noun, formatBound(*spec.Min))
	case spec.Max != nil:
		return fmt.Sprintf("%s of at most %s", noun, formatBound(*spec.Max))
	default:
		return noun
	}
}

func formatBound(f float64) string { return strconv.FormatFloat(f, 'g', -1, 64) }

func invalidValue(spec KeySpec, expectation, raw string) error {
	return &SetError{
		Code:    CodeInvalidValue,
		Key:     spec.Key,
		Message: fmt.Sprintf("%s %s, got %q", spec.Key, expectation, raw),
	}
}

// sameValue reports whether the value already in the file equals the one being
// written. YAML decodes into int/float64/string/bool/[]any, so the comparison
// normalizes numbers and sequences rather than relying on Go type identity.
func sameValue(current, next any) bool {
	switch want := next.(type) {
	case bool:
		got, ok := current.(bool)
		return ok && got == want
	case string:
		got, ok := current.(string)
		return ok && got == want
	case int64:
		got, ok := toFloat(current)
		return ok && got == float64(want)
	case float64:
		got, ok := toFloat(current)
		return ok && got == want
	case []string:
		got, ok := toStringSlice(current)
		return ok && slices.Equal(got, want)
	default:
		// Structured (opaque) values: compare what each would serialize to, so
		// rewriting an unchanged destination list is still a no-op.
		return sameEncoded(current, next)
	}
}

// sameEncoded compares two values by their YAML encoding. current comes from
// the document as generic maps and slices while next is a Go struct, so their
// types never match even when the content does.
func sameEncoded(current, next any) bool {
	currentYAML, currentOK := canonicalYAML(current)
	nextYAML, nextOK := canonicalYAML(next)
	return currentOK && nextOK && bytes.Equal(currentYAML, nextYAML)
}

// canonicalYAML renders a value through a generic round trip, so a struct and
// the maps it was decoded from serialize their fields in the same order.
func canonicalYAML(value any) ([]byte, bool) {
	encoded, err := yaml.Marshal(value)
	if err != nil {
		return nil, false
	}
	var generic any
	if err := yaml.Unmarshal(encoded, &generic); err != nil {
		return nil, false
	}
	out, err := yaml.Marshal(generic)
	if err != nil {
		return nil, false
	}
	return out, true
}

func toFloat(value any) (float64, bool) {
	switch number := value.(type) {
	case int:
		return float64(number), true
	case int32:
		return float64(number), true
	case int64:
		return float64(number), true
	case uint:
		return float64(number), true
	case uint64:
		return float64(number), true
	case float32:
		return float64(number), true
	case float64:
		return number, true
	default:
		return 0, false
	}
}

func toStringSlice(value any) ([]string, bool) {
	switch list := value.(type) {
	case []string:
		return list, true
	case []any:
		out := make([]string, 0, len(list))
		for _, item := range list {
			text, ok := item.(string)
			if !ok {
				return nil, false
			}
			out = append(out, text)
		}
		return out, true
	default:
		return nil, false
	}
}
