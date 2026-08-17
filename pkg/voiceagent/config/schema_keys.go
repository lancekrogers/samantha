package config

// This file holds the key specs for the groups Settings renders itself.
// schema_keys_models.go holds the rest. Titles are sentence case and name the
// thing rather than the key; help is one sentence stating the consequence.

func voiceKeys() []KeySpec {
	return []KeySpec{
		{
			Key: "tts_provider", Type: TypeEnum, Default: "kokoro", Enum: ttsProviderEnum(),
			Group: GroupVoice, Title: "Speech engine",
			Help:               "Which engine speaks. Kokoro is fastest; Qwen sounds richer and costs latency.",
			PersonaOverridable: true,
		},
		{
			Key: "tts_voice", Type: TypeEnum, Default: "af_heart", Enum: kokoroVoiceEnum(),
			Group: GroupVoice, Title: "Kokoro voice",
			Help:               "The voice Kokoro speaks in. Qwen ignores this and uses its own preset.",
			PersonaOverridable: true,
		},
		{
			Key: "voice_fallback_provider", Type: TypeEnum, Default: "kokoro", Enum: ttsProviderEnum(),
			Group: GroupVoice, Title: "Fallback engine",
			Help: "Speaks one sentence in this engine's voice when the main engine fails, instead of going silent.",
		},
		{
			Key: "speech_speed", Type: TypeFloat, Default: 0.95, Unit: "×", Min: num(0.5), Max: num(2.0),
			Group: GroupVoice, Title: "Speaking rate",
			Help: "How fast she talks. Above about 1.3 the words start to slur.",
		},
		{
			Key: "output_device", Type: TypeString, Default: "",
			Group: GroupVoice, Title: "Speaker",
			Help: "Where her voice plays. Empty follows the system output device.",
		},
		{
			Key: "input_device", Type: TypeString, Default: "",
			Group: GroupVoice, Title: "Microphone",
			Help: "Which microphone she listens to. Empty follows the system input device.",
		},
		{
			Key: "vad_enabled", Type: TypeBool, Default: true,
			Group: GroupVoice, Title: "Detect speech automatically",
			Help: "Ends your turn when you stop talking. Off means every turn waits for the listen timeout.",
		},
		{
			Key: "vad_silence_duration", Type: TypeFloat, Default: 0.5, Unit: "seconds", Min: num(0.1), Max: num(3.0),
			Group: GroupVoice, Title: "End-of-turn silence",
			Help: "Seconds of quiet that end your turn. Lower feels snappier; too low cuts you off mid-sentence.",
		},
		{
			Key: "vad_threshold", Type: TypeFloat, Default: 0.6, Min: num(0.0), Max: num(1.0),
			Group: GroupVoice, Title: "Speech confidence",
			Help: "How sure she must be that a sound is speech. Raise it in a noisy room; too high drops quiet talkers.",
		},
		{
			Key: "vad_min_speech_duration", Type: TypeFloat, Default: 0.25, Unit: "seconds", Min: num(0.05), Max: num(2.0),
			Group: GroupVoice, Title: "Shortest utterance",
			Help: "Sound shorter than this is treated as noise, so a cough never starts a turn.",
		},
		{
			Key: "vad_pre_roll_ms", Type: TypeInt, Default: 300, Unit: "ms", Min: num(0), Max: num(2000),
			Group: GroupVoice, Title: "Pre-speech audio",
			Help: "Audio kept from just before speech is detected, so your first word isn't clipped.",
		},
		{
			Key: "voice_frontend_enabled", Type: TypeBool, Default: false,
			Group: GroupVoice, Title: "Voice front-end",
			Help: "Runs echo cancellation and noise suppression on this Mac's microphone. Required for barge-in.",
		},
		{
			Key: "barge_in_enabled", Type: TypeBool, Default: false,
			Group: GroupVoice, Title: "Barge-in",
			Help: "Lets you talk over her. Needs the voice front-end on, and only affects this Mac's microphone — phone clients still need the interrupt button.",
		},
		{
			Key: "backchannel_enabled", Type: TypeBool, Default: false,
			Group: GroupVoice, Title: "Thinking sounds",
			Help: "Plays a short \"mm-hm\" while a slow answer is still coming, so the pause doesn't feel dead.",
		},
	}
}

func brainKeys() []KeySpec {
	return []KeySpec{
		{
			Key: "brain_provider", Type: TypeEnum, Default: "ollama", Enum: brainProviderEnum(),
			Group: GroupBrain, Title: "Model provider",
			Help:               "Who answers. Ollama runs on this Mac; Claude and Grok call out to their CLIs.",
			PersonaOverridable: true,
		},
		{
			Key: "ollama_model", Type: TypeString, Default: "",
			Group: GroupBrain, Title: "Ollama model",
			Help:               "The local model Ollama loads. Empty picks the first installed model.",
			PersonaOverridable: true,
		},
		{
			Key: "grok_model", Type: TypeString, Default: "",
			Group: GroupBrain, Title: "Grok model",
			Help:               "The model the Grok CLI is asked for. Empty uses the CLI's own default.",
			PersonaOverridable: true,
		},
		{
			Key: "ollama_host", Type: TypeString, Default: "http://localhost:11434",
			Group: GroupBrain, Title: "Ollama server",
			Help: "Where Ollama is running. Point it at another machine to answer from there.",
		},
		{
			Key: "ollama_num_ctx", Type: TypeInt, Default: 8192, Unit: "tokens", Min: num(0), Max: num(131072),
			Group: GroupBrain, Title: "Context window",
			Help: "How much conversation the local model sees. 0 uses the server's own size, which silently drops the oldest turns.",
		},
		{
			Key: "ollama_keep_alive", Type: TypeString, Default: "10m",
			Group: GroupBrain, Title: "Model residency",
			Help: "How long Ollama keeps the model in memory between turns. Shorter frees RAM but pays the load cost again.",
		},
		{
			Key: "ollama_think", Type: TypeEnum, Default: "false", Enum: ollamaThinkEnum(),
			Group: GroupBrain, Title: "Reasoning",
			Help: "Lets reasoning models think before answering. Costs latency, and some models then reply with nothing speakable.",
		},
		{
			Key: "claude_max_session_tokens", Type: TypeInt, Default: 0, Unit: "tokens", Min: num(0), Max: num(1000000),
			Group: GroupBrain, Title: "Claude session cap",
			Help: "Starts a fresh Claude session once the replayed prompt passes this size. 0 keeps resuming forever.",
		},
		{
			Key: "claude_session_warn_tokens", Type: TypeInt, Default: 60000, Unit: "tokens", Min: num(0), Max: num(1000000),
			Group: GroupBrain, Title: "Claude session warning",
			Help: "Warns once when a Claude session's replayed prompt passes this size. Nothing is dropped. 0 disables the warning.",
		},
		{
			Key: "environment_context_enabled", Type: TypeBool, Default: true,
			Group: GroupBrain, Title: "Machine context",
			Help: "Tells the model your user name, host and OS so it can answer questions about this machine.",
		},
		{
			Key: "max_history", Type: TypeInt, Default: 10, Min: num(1), Max: num(100),
			Group: GroupBrain, Title: "Remembered turns",
			Help: "How many past exchanges she carries into the next answer. More context costs latency.",
		},
	}
}

func speakerKeys() []KeySpec {
	return []KeySpec{
		{
			Key: "speaker.enabled", Type: TypeBool, Default: true,
			Group: GroupSpeakers, Title: "Speaker recognition",
			Help: "Master switch. Off means no speaker models load and nobody is ever labelled.",
		},
		{
			Key: "speaker.threshold", Type: TypeFloat, Default: 0.6, Min: num(0.0), Max: num(1.0),
			Group: GroupSpeakers, Title: "Match confidence",
			Help: "How close a voice must be to an enrolled profile to count as that person. Higher misses more; lower mislabels more.",
		},
		{
			Key: "speaker.enrollment_dir", Type: TypeString, Default: "",
			Group: GroupSpeakers, Title: "Enrolled voices folder",
			Help: "Where enrolled voice profiles are stored. Empty uses the folder inside Samantha's install root.",
		},
		{
			Key: "speaker.live.enabled", Type: TypeBool, Default: true,
			Group: GroupSpeakers, Title: "Live speaker labels",
			Help: "Labels who is speaking during a conversation, not just afterwards in meetings.",
		},
		{
			Key: "speaker.live.mode", Type: TypeEnum, Default: "indicator", Enum: []string{"indicator", "owner_verify"},
			Group: GroupSpeakers, Title: "Live label mode",
			Help: "Indicator only shows who is talking; owner-verify also ignores turns from anyone but you when it is confident.",
		},
		{
			Key: "speaker.live.threshold", Type: TypeFloat, Default: 0.0, Min: num(0.0), Max: num(1.0),
			Group: GroupSpeakers, Title: "Live match confidence",
			Help: "Match cutoff for live labels. 0 inherits the general match confidence.",
		},
		{
			Key: "speaker.live.window_ms", Type: TypeInt, Default: 1500, Unit: "ms", Min: num(500), Max: num(10000),
			Group: GroupSpeakers, Title: "Live analysis window",
			Help: "Must match the window used when speakers were enrolled — a mismatch roughly doubles missed matches.",
		},
	}
}

func meetingKeys() []KeySpec {
	return []KeySpec{
		{
			Key: "meeting.dir", Type: TypeString, Default: "",
			Group: GroupMeetings, Title: "Meetings folder",
			Help: "Where recorded meetings and their notes are written. Empty uses the folder inside Samantha's install root.",
		},
		{
			Key: "meeting.route.mode", Type: TypeEnum, Default: "ask", Enum: []string{"ask", "auto", "off"},
			Group: GroupMeetings, Title: "Export notes",
			Help: "Ask prompts after every meeting, auto exports straight to the default destination, off never exports.",
		},
		{
			Key: "meeting.route.default", Type: TypeEnum, Default: "",
			Group: GroupMeetings, Title: "Default destination",
			Help: "The destination auto exports to, and the one preselected when she asks.",
		},
		{
			Key: "meeting.route.body", Type: TypeEnum, Default: "full", Enum: []string{"notes", "full"},
			Group: GroupMeetings, Title: "Exported body",
			Help: "Both options include the full transcript in what gets exported.",
		},
		{
			Key: "meeting.route.destinations", Type: TypeOpaque, Default: []any{},
			Group: GroupMeetings, Title: "Route destinations",
			Help: "Named export targets for meeting notes.", ManagedBy: "meeting destinations",
		},
		{
			Key: "speaker.meeting.enabled", Type: TypeBool, Default: true,
			Group: GroupMeetings, Title: "Identify speakers in meetings",
			Help: "Splits a finished recording by speaker so the transcript says who said what.",
		},
		{
			Key: "speaker.meeting.record_audio", Type: TypeBool, Default: false,
			Group: GroupMeetings, Title: "Keep meeting audio",
			Help: "Keeps the meeting audio file. Without it, speakers can't be re-analysed after the meeting ends.",
		},
		{
			Key: "speaker.meeting.num_speakers", Type: TypeInt, Default: 0, Min: num(0), Max: num(20),
			Group: GroupMeetings, Title: "Expected speakers",
			Help: "Fixes how many people the analysis splits the room into. 0 lets it work that out.",
		},
		{
			Key: "speaker.meeting.live", Type: TypeBool, Default: true,
			Group: GroupMeetings, Title: "Label while recording",
			Help: "Shows provisional speaker labels during the meeting; the final pass still corrects them.",
		},
	}
}

func toolKeys() []KeySpec {
	return []KeySpec{
		{
			Key: "voice_tools_enabled", Type: TypeBool, Default: true,
			Group: GroupTools, Title: "Local tools",
			Help: "Lets her read files and run commands on this Mac for you. Off makes tool-capable models look broken.",
		},
		{
			Key: "tool_command_timeout", Type: TypeInt, Default: 30, Unit: "seconds", Min: num(1), Max: num(120),
			Group: GroupTools, Title: "Command timeout",
			Help: "How long one command may run before it is killed. Long builds need a bigger number.",
		},
		{
			Key: "skills_enabled", Type: TypeBool, Default: true,
			Group: GroupTools, Title: "Skills",
			Help: "Loads SKILL.md instruction packs into her prompt. Ollama only.",
		},
		{
			Key: "skills_dir", Type: TypeString, Default: "",
			Group: GroupTools, Title: "Skills folder",
			Help: "An extra folder scanned for skills. Empty uses the folder inside Samantha's install root.",
		},
		{
			Key: "skills_disabled", Type: TypeStringList, Default: []string{},
			Group: GroupTools, Title: "Disabled skills",
			Help: "Skills the agent ignores even when discovered.",
		},
		{
			Key: "ollama_embedding_model", Type: TypeString, Default: "nomic-embed-text",
			Group: GroupTools, Title: "Skill matching model",
			Help: "The embedding model that decides which skill fits a request. Empty falls back to name matching only.",
		},
		{
			Key: "skills_similarity_threshold", Type: TypeFloat, Default: 0.55, Min: num(0.0), Max: num(1.0),
			Group: GroupTools, Title: "Skill match cutoff",
			Help: "How close a request must be to a skill before it is offered. Higher offers fewer, more relevant skills.",
		},
	}
}

func remoteKeys() []KeySpec {
	return []KeySpec{
		{
			Key: "remote_tools_enabled", Type: TypeBool, Default: false,
			Group: GroupRemote, Title: "Tools for network turns",
			Help: "Lets turns that arrive over the network run tools on this Mac. Off by default — a phone or browser can otherwise run commands here.",
		},
	}
}
