package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"gopkg.in/yaml.v3"
)

// dur is a duration parsed from YAML strings like "90s" / "2m".
type dur struct{ time.Duration }

func (d *dur) UnmarshalYAML(node *yaml.Node) error {
	var s string
	if err := node.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("bad duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

// Scenario is the harness's entire customization surface: personas, models,
// voices, triggers, bridge policy, and assertions are data, not code
// (harness-design.md §3). Unknown keys are load errors — scenarios are
// contracts, and a silent typo in a `when:` clause is how a harness lies.
type Scenario struct {
	Name        string                   `yaml:"name"`
	Description string                   `yaml:"description"`
	Duration    dur                      `yaml:"duration"`
	Defaults    *InstanceSpec            `yaml:"defaults"`
	Instances   map[string]*InstanceSpec `yaml:"instances"`
	Triggers    []Trigger                `yaml:"triggers"`
	Bridge      Bridge                   `yaml:"bridge"`
	Capture     Capture                  `yaml:"capture"`
	Expect      []Expectation            `yaml:"expect"`

	dir string // scenario file directory, for relative prompt paths
}

type InstanceSpec struct {
	Persona PersonaSpec `yaml:"persona"`
	Brain   BrainSpec   `yaml:"brain"`
	TTS     TTSSpec     `yaml:"tts"`
	// Tools gates voice_tools_enabled. Default FALSE on purpose: the first
	// live run showed two tool-enabled models goading each other into
	// executing real host commands. Opt in per scenario when tool behavior
	// is the thing under test.
	Tools bool              `yaml:"tools"`
	Flags []string          `yaml:"flags"`
	Env   map[string]string `yaml:"env"`
	Pane  PaneSpec          `yaml:"pane"`
}

type PersonaSpec struct {
	DisplayName      string `yaml:"display_name"`
	SystemPrompt     string `yaml:"system_prompt"`
	SystemPromptFile string `yaml:"system_prompt_file"`
}

type BrainSpec struct {
	Provider string `yaml:"provider"`
	Model    string `yaml:"model"`
	Host     string `yaml:"host"`
}

type TTSSpec struct {
	Provider string `yaml:"provider"`
	Voice    string `yaml:"voice"`
}

type PaneSpec struct {
	Width  int `yaml:"width"`
	Height int `yaml:"height"`
}

type Trigger struct {
	ID     string `yaml:"id"`
	When   When   `yaml:"when"`
	Target string `yaml:"target"`
	Action Action `yaml:"action"`
}

// When holds exactly one firing condition.
type When struct {
	At            *dur           `yaml:"at"`
	AfterReply    *AfterReply    `yaml:"after_reply"`
	WhileSpeaking *WhileSpeaking `yaml:"while_speaking"`
}

type AfterReply struct {
	Instance string `yaml:"instance"`
	Count    int    `yaml:"count"`
}

type WhileSpeaking struct {
	Instance string `yaml:"instance"`
}

type Action struct {
	Type   string `yaml:"type"` // keys | key | pause
	Text   string `yaml:"text"`
	Typing string `yaml:"typing"` // human | instant (default human)
	Submit bool   `yaml:"submit"`
	Key    string `yaml:"key"` // named tmux key for type=key (Escape, Enter, ...)
	For    *dur   `yaml:"for"` // type=pause
}

type Bridge struct {
	Mode           string     `yaml:"mode"` // text | none
	Pairs          [][]string `yaml:"pairs"`
	Prefix         string     `yaml:"prefix"`
	MaxExchanges   int        `yaml:"max_exchanges"`
	MinGap         *dur       `yaml:"min_gap"`
	Idle           *dur       `yaml:"idle"`
	DegradedPolicy string     `yaml:"degraded_policy"` // continue | halt
}

type Capture struct {
	Audio       bool `yaml:"audio"`
	TmuxHistory bool `yaml:"tmux_history"`
}

type Expectation struct {
	Instance string  `yaml:"instance"` // empty = run-level metric
	Metric   string  `yaml:"metric"`
	Op       string  `yaml:"op"` // == != >= <= > <
	Value    float64 `yaml:"value"`
}

var nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// LoadScenario parses and validates a scenario file strictly.
func LoadScenario(path string) (*Scenario, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var s Scenario
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	s.dir = filepath.Dir(path)
	if err := s.validate(); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return &s, nil
}

func (s *Scenario) validate() error {
	if !nameRE.MatchString(s.Name) {
		return fmt.Errorf("name %q must match %s", s.Name, nameRE)
	}
	if s.Duration.Duration <= 0 {
		return fmt.Errorf("duration is required")
	}
	if len(s.Instances) < 2 {
		return fmt.Errorf("need at least 2 instances, got %d", len(s.Instances))
	}
	for id, inst := range s.Instances {
		if !nameRE.MatchString(id) {
			return fmt.Errorf("instance id %q must match %s", id, nameRE)
		}
		s.applyDefaults(inst)
		p := inst.Persona
		if p.DisplayName == "" {
			return fmt.Errorf("instance %s: persona.display_name is required", id)
		}
		if p.SystemPrompt != "" && p.SystemPromptFile != "" {
			return fmt.Errorf("instance %s: system_prompt and system_prompt_file are exclusive", id)
		}
		if p.SystemPromptFile != "" {
			full := filepath.Join(s.dir, p.SystemPromptFile)
			if _, err := os.Stat(full); err != nil {
				return fmt.Errorf("instance %s: system_prompt_file: %w", id, err)
			}
		}
	}
	for i, t := range s.Triggers {
		if _, ok := s.Instances[t.Target]; !ok && t.Action.Type != "pause" {
			return fmt.Errorf("trigger %d: unknown target %q", i, t.Target)
		}
		n := 0
		if t.When.At != nil {
			n++
		}
		if t.When.AfterReply != nil {
			n++
			if _, ok := s.Instances[t.When.AfterReply.Instance]; !ok {
				return fmt.Errorf("trigger %d: after_reply.instance %q unknown", i, t.When.AfterReply.Instance)
			}
			if t.When.AfterReply.Count < 1 {
				return fmt.Errorf("trigger %d: after_reply.count must be >= 1", i)
			}
		}
		if t.When.WhileSpeaking != nil {
			n++
			if _, ok := s.Instances[t.When.WhileSpeaking.Instance]; !ok {
				return fmt.Errorf("trigger %d: while_speaking.instance %q unknown", i, t.When.WhileSpeaking.Instance)
			}
		}
		if n != 1 {
			return fmt.Errorf("trigger %d: exactly one `when` condition required, got %d", i, n)
		}
		switch t.Action.Type {
		case "keys":
			if t.Action.Text == "" {
				return fmt.Errorf("trigger %d: action.text required for keys", i)
			}
			if ty := t.Action.Typing; ty != "" && ty != "human" && ty != "instant" {
				return fmt.Errorf("trigger %d: typing must be human|instant", i)
			}
		case "key":
			if t.Action.Key == "" {
				return fmt.Errorf("trigger %d: action.key required", i)
			}
		case "pause":
			if t.Action.For == nil {
				return fmt.Errorf("trigger %d: action.for required for pause", i)
			}
		default:
			return fmt.Errorf("trigger %d: unknown action type %q", i, t.Action.Type)
		}
	}
	switch s.Bridge.Mode {
	case "", "none":
		s.Bridge.Mode = "none"
	case "text":
		if len(s.Bridge.Pairs) == 0 {
			return fmt.Errorf("bridge.pairs required for mode text")
		}
		for _, pair := range s.Bridge.Pairs {
			if len(pair) != 2 {
				return fmt.Errorf("bridge pair %v must have exactly 2 instances", pair)
			}
			for _, id := range pair {
				if _, ok := s.Instances[id]; !ok {
					return fmt.Errorf("bridge pair references unknown instance %q", id)
				}
			}
		}
		if s.Bridge.MaxExchanges <= 0 {
			return fmt.Errorf("bridge.max_exchanges must be > 0")
		}
	default:
		return fmt.Errorf("bridge.mode must be text|none (audio is v2)")
	}
	switch s.Bridge.DegradedPolicy {
	case "":
		s.Bridge.DegradedPolicy = "continue"
	case "continue", "halt":
	default:
		return fmt.Errorf("bridge.degraded_policy must be continue|halt")
	}
	for i, e := range s.Expect {
		if e.Instance != "" {
			if _, ok := s.Instances[e.Instance]; !ok {
				return fmt.Errorf("expect %d: unknown instance %q", i, e.Instance)
			}
		}
		switch e.Op {
		case "==", "!=", ">=", "<=", ">", "<":
		default:
			return fmt.Errorf("expect %d: bad op %q", i, e.Op)
		}
		if e.Metric == "" {
			return fmt.Errorf("expect %d: metric required", i)
		}
	}
	return nil
}

// applyDefaults fills unset instance fields from scenario defaults.
func (s *Scenario) applyDefaults(inst *InstanceSpec) {
	d := s.Defaults
	if d == nil {
		return
	}
	if inst.Brain.Provider == "" {
		inst.Brain.Provider = d.Brain.Provider
	}
	if inst.Brain.Model == "" {
		inst.Brain.Model = d.Brain.Model
	}
	if inst.Brain.Host == "" {
		inst.Brain.Host = d.Brain.Host
	}
	if inst.TTS.Provider == "" {
		inst.TTS.Provider = d.TTS.Provider
	}
	if inst.TTS.Voice == "" {
		inst.TTS.Voice = d.TTS.Voice
	}
	if len(inst.Flags) == 0 {
		inst.Flags = append([]string(nil), d.Flags...)
	}
	if inst.Pane.Width == 0 {
		inst.Pane.Width = d.Pane.Width
	}
	if inst.Pane.Height == 0 {
		inst.Pane.Height = d.Pane.Height
	}
	if d.Tools {
		inst.Tools = true
	}
	for k, v := range d.Env {
		if inst.Env == nil {
			inst.Env = map[string]string{}
		}
		if _, ok := inst.Env[k]; !ok {
			inst.Env[k] = v
		}
	}
}

// SystemPromptText resolves the persona identity (inline, file, or a minimal
// default naming the agent).
func (s *Scenario) SystemPromptText(inst *InstanceSpec) (string, error) {
	p := inst.Persona
	if p.SystemPrompt != "" {
		return p.SystemPrompt, nil
	}
	if p.SystemPromptFile != "" {
		data, err := os.ReadFile(filepath.Join(s.dir, p.SystemPromptFile))
		if err != nil {
			return "", err
		}
		return string(data), nil
	}
	return fmt.Sprintf("You are %s, a voice assistant. Keep replies to 2-3 conversational sentences with no formatting.", p.DisplayName), nil
}
