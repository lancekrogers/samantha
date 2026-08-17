package config

import (
	"sort"
	"strings"
)

// SchemaVersion is the version of the `config schema`/`config get`/`config set`
// payload shape. Bump it when a field is removed or its meaning changes;
// additive fields do not need a bump because front ends decode leniently.
const SchemaVersion = 1

// ValueType names the shape of a config value as the front ends see it. It is
// deliberately coarser than Go's type system: a front end renders one control
// per ValueType and `config set` coerces by it, so the file's current value can
// never decide how the next value is typed.
type ValueType string

const (
	TypeBool       ValueType = "bool"
	TypeInt        ValueType = "int"
	TypeFloat      ValueType = "float"
	TypeString     ValueType = "string"
	TypeEnum       ValueType = "enum"
	TypeStringList ValueType = "list<string>"
	// TypeOpaque is the escape hatch for a key whose value shape none of the
	// above describes (today exactly one: meeting.route.destinations).
	// Always ships Editable=false and a ManagedBy verb.
	TypeOpaque ValueType = "opaque"
)

// Setting groups. A group is a rendering contract, not a package boundary:
// GroupModels is rendered by the Models screen, GroupHidden by nothing.
const (
	GroupVoice    = "voice"
	GroupBrain    = "brain"
	GroupSpeakers = "speakers"
	GroupMeetings = "meetings"
	GroupTools    = "tools"
	GroupRemote   = "remote"
	GroupAdvanced = "advanced"
	GroupModels   = "models"
	GroupHidden   = "hidden"
)

// groupOrder is the order groups are presented in, and the primary sort key of
// Schema(). Changing it changes the Settings screen's section order.
var groupOrder = []string{
	GroupVoice, GroupBrain, GroupSpeakers, GroupMeetings,
	GroupTools, GroupRemote, GroupAdvanced, GroupModels, GroupHidden,
}

// Groups returns the group identifiers in render order.
func Groups() []string {
	out := make([]string, len(groupOrder))
	copy(out, groupOrder)
	return out
}

// KeySpec describes one config key well enough for a front end to render a
// control for it, and for `config set` to validate a value without knowing
// anything about the key.
type KeySpec struct {
	Key     string    `json:"key"`
	Type    ValueType `json:"type"`
	Default any       `json:"default"`
	Enum    []string  `json:"enum,omitempty"`
	// AllowsEmpty reports that `config set <key> ""` is accepted: every text
	// key, plus the enums whose own default is the empty string. For those
	// enums "" is the unset state, so writing it back is how a user returns
	// the key to it — that is the pair a front end wants before offering an
	// "(App default)" choice, and Default is the second half of the pair.
	// Everything else (bool, number, list, opaque, and an enum with a real
	// default) refuses an empty value.
	AllowsEmpty        bool     `json:"allows_empty"`
	Group              string   `json:"group"`
	Title              string   `json:"title"`
	Help               string   `json:"help"`
	Unit               string   `json:"unit,omitempty"`
	Min                *float64 `json:"min,omitempty"`
	Max                *float64 `json:"max,omitempty"`
	RestartRequired    bool     `json:"restart_required"`
	RestartVerified    bool     `json:"restart_verified"`
	PersonaOverridable bool     `json:"persona_overridable"`
	Env                string   `json:"env,omitempty"`
	Editable           bool     `json:"editable"`
	ManagedBy          string   `json:"managed_by,omitempty"`
	Secret             bool     `json:"secret,omitempty"`
}

// Schema returns every config key's spec in a stable order: group (render
// order), then key. Restart truth, env bindings and editability come from the
// tables that own them, so a KeySpec literal can never disagree with the
// loader or with restart.go.
func Schema() []KeySpec {
	table := schemaTable()
	out := make([]KeySpec, 0, len(table))
	for _, spec := range table {
		out = append(out, derive(spec))
	}
	sortSchema(out)
	return out
}

// derive fills the fields a KeySpec literal must never carry itself: restart
// truth from restart.go, the env binding from the loader's own table, and
// editability from whether a verb owns the key. One truth each, so a literal
// cannot disagree with the code that acts on it.
func derive(spec KeySpec) KeySpec {
	spec.RestartRequired = RestartRequired(spec.Key)
	spec.RestartVerified = RestartVerified(spec.Key)
	spec.Env = envBindings[spec.Key]
	spec.Editable = spec.ManagedBy == ""
	spec.AllowsEmpty = allowsEmpty(spec)
	spec.Enum = copyStrings(spec.Enum)
	return spec
}

// allowsEmpty answers what the coercer does, not what a second hand-kept list
// says: a text key takes any string including the empty one, and an enum takes
// "" only when "" is its own default — the unset state, which writing "" back
// returns it to. stt_mode is the case that found this: it legitimately holds ""
// and could not be cleared.
//
// It deliberately does not claim more than that. A first cut reported
// allows_empty only for the empty-defaulted keys, which had the schema and the
// README both claiming an empty value was an error everywhere else while
// `config set agent_name ""` quietly succeeded. Found by adversarial review.
func allowsEmpty(spec KeySpec) bool {
	switch spec.Type {
	case TypeString:
		return true
	case TypeEnum:
		text, ok := spec.Default.(string)
		return ok && text == ""
	default:
		return false
	}
}

// SchemaFor is Schema with the enums that only a loaded config can fill.
// Today that is meeting.route.default, whose accepted values are the ids of
// the destinations the user has configured. Callers that render or validate
// against real config (the CLI) use this; callers that only need the static
// contract use Schema.
func SchemaFor(cfg *Config) []KeySpec {
	out := Schema()
	if cfg == nil {
		return out
	}
	ids := make([]string, 0, len(cfg.Meeting.Route.Destinations))
	for _, dest := range cfg.Meeting.Route.Destinations {
		if id := strings.TrimSpace(dest.ID); id != "" {
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return out
	}
	for i := range out {
		if out[i].Key == "meeting.route.default" {
			out[i].Enum = ids
			break
		}
	}
	return out
}

// SpecFor looks a key up case-insensitively.
//
// It scans the unsorted table and derives only the match, rather than building
// and sorting the whole schema for one lookup. The table itself is not cached:
// two defaults (models_dir, whispercpp_model_path) are derived from the home
// directory, which tests relocate.
func SpecFor(key string) (KeySpec, bool) {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, spec := range schemaTable() {
		if spec.Key == key {
			return derive(spec), true
		}
	}
	return KeySpec{}, false
}

// SchemaKeys returns every schema key in Schema order. Used by callers that
// only need the key set (coverage checks, did-you-mean suggestions).
func SchemaKeys() []string {
	schema := Schema()
	out := make([]string, 0, len(schema))
	for _, spec := range schema {
		out = append(out, spec.Key)
	}
	return out
}

// schemaTable assembles the per-group tables. It is rebuilt per call rather
// than cached because two defaults (models_dir, whispercpp_model_path) are
// derived from the home directory, which tests relocate.
func schemaTable() []KeySpec {
	var out []KeySpec
	out = append(out, voiceKeys()...)
	out = append(out, brainKeys()...)
	out = append(out, speakerKeys()...)
	out = append(out, meetingKeys()...)
	out = append(out, toolKeys()...)
	out = append(out, remoteKeys()...)
	out = append(out, advancedKeys()...)
	out = append(out, modelKeys()...)
	out = append(out, hiddenKeys()...)
	return out
}

func sortSchema(specs []KeySpec) {
	rank := make(map[string]int, len(groupOrder))
	for i, group := range groupOrder {
		rank[group] = i
	}
	sort.SliceStable(specs, func(i, j int) bool {
		gi, gj := rank[specs[i].Group], rank[specs[j].Group]
		if gi != gj {
			return gi < gj
		}
		return specs[i].Key < specs[j].Key
	})
}

// num is the pointer helper for Min/Max bounds.
func num(f float64) *float64 { return &f }

func copyStrings(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	out := make([]string, len(in))
	copy(out, in)
	return out
}
