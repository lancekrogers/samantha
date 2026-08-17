package persona

import "github.com/lancekrogers/samantha/pkg/voiceagent/config"

// overrideProbe is a value no profile field could produce. A key that still
// holds it after Apply ran was not written; a key that lost it was.
const overrideProbe = "\x00persona-override-probe"

// personaRoutes is the universe of config keys Apply may overwrite: the right
// column of the persona routing table, in the order the profile's own fields
// appear. It is a list of accessors and nothing else — which keys a given
// profile actually claims is decided by running Apply, so a badge can never
// disagree with the runtime the way a second copy of the routing conditions
// eventually would.
var personaRoutes = []struct {
	key   string
	read  func(*config.Config) string
	write func(*config.Config, string)
}{
	{"agent_name", func(c *config.Config) string { return c.AgentName },
		func(c *config.Config, v string) { c.AgentName = v }},
	{"persona", func(c *config.Config) string { return c.Persona },
		func(c *config.Config, v string) { c.Persona = v }},
	{"active_persona", func(c *config.Config) string { return c.ActivePersona },
		func(c *config.Config, v string) { c.ActivePersona = v }},
	{"brain_provider", func(c *config.Config) string { return c.BrainProvider },
		func(c *config.Config, v string) { c.BrainProvider = v }},
	{"ollama_model", func(c *config.Config) string { return c.OllamaModel },
		func(c *config.Config, v string) { c.OllamaModel = v }},
	{"grok_model", func(c *config.Config) string { return c.GrokModel },
		func(c *config.Config, v string) { c.GrokModel = v }},
	{"tts_provider", func(c *config.Config) string { return c.TTSProvider },
		func(c *config.Config, v string) { c.TTSProvider = v }},
	{"tts_voice", func(c *config.Config) string { return c.TTSVoice },
		func(c *config.Config, v string) { c.TTSVoice = v }},
	{"qwen_tts_voice", func(c *config.Config) string { return c.QwenTTSVoice },
		func(c *config.Config, v string) { c.QwenTTSVoice = v }},
	{"qwen_tts_model_tier", func(c *config.Config) string { return c.QwenTTSModelTier },
		func(c *config.Config, v string) { c.QwenTTSModelTier = v }},
}

// routingKeys are the two keys Apply reads back while routing the others: the
// model key depends on the effective brain provider and the voice key on the
// effective TTS provider. They are probed in a second pass so the first pass
// keeps the real providers and therefore the real routing.
var routingKeys = map[string]bool{"brain_provider": true, "tts_provider": true}

// OverriddenKeys returns the config keys Apply would overwrite for p, given the
// effective providers in cfg. Apply and OverriddenKeys are derived from one
// routing table — a badge that disagrees with the runtime is worse than none.
//
// A key counts as overridden when the profile writes it, even when it writes
// the value the config already holds: the point of the badge is that the
// persona decides this key, not that today's two values differ.
//
// cfg must be the un-overlaid config (config.LoadRaw), which is what
// `config get` reports. cfg is never mutated.
func OverriddenKeys(cfg *config.Config, p *Profile) []string {
	if cfg == nil || p == nil {
		return nil
	}
	written := probeWrites(cfg, p, func(key string) bool { return !routingKeys[key] })
	for key := range probeWrites(cfg, p, func(key string) bool { return routingKeys[key] }) {
		written[key] = true
	}

	out := make([]string, 0, len(written))
	for _, route := range personaRoutes {
		if written[route.key] {
			out = append(out, route.key)
		}
	}
	return out
}

// probeProfileKeys stamps the probe value on every selected key, runs Apply,
// and reports which of them Apply replaced.
func probeWrites(cfg *config.Config, p *Profile, selected func(string) bool) map[string]bool {
	probe := *cfg
	for _, route := range personaRoutes {
		if selected(route.key) {
			route.write(&probe, overrideProbe)
		}
	}
	Apply(&probe, p)

	written := map[string]bool{}
	for _, route := range personaRoutes {
		if selected(route.key) && route.read(&probe) != overrideProbe {
			written[route.key] = true
		}
	}
	return written
}

// OverridableKeys returns every key a persona could override, in routing-table
// order. It is the universe the schema's persona_overridable flag describes.
func OverridableKeys() []string {
	out := make([]string, 0, len(personaRoutes))
	for _, route := range personaRoutes {
		out = append(out, route.key)
	}
	return out
}
