package config

import "path/filepath"

// Key specs for the groups Settings does not render itself: advanced (collapsed
// under Settings), models (rendered by the Models screen) and hidden (rendered
// nowhere — owned by a verb).

func advancedKeys() []KeySpec {
	return []KeySpec{
		{
			Key: "language", Type: TypeString, Default: "en-US",
			Group: GroupAdvanced, Title: "Language",
			Help: "The locale speech recognition and speech synthesis assume. Changing it needs models for that language.",
		},
		{
			Key: "listen_timeout", Type: TypeInt, Default: 10, Unit: "seconds", Min: num(1), Max: num(120),
			Group: GroupAdvanced, Title: "Wait for speech",
			Help: "How long she waits for you to start talking before giving up on the turn.",
		},
		{
			Key: "phrase_time_limit", Type: TypeInt, Default: 30, Unit: "seconds", Min: num(1), Max: num(300),
			Group: GroupAdvanced, Title: "Longest utterance",
			Help: "The longest single stretch of speech she will transcribe before ending the turn for you.",
		},
		{
			Key: "agent_name", Type: TypeString, Default: "Samantha",
			Group: GroupAdvanced, Title: "Agent name",
			Help:               "What she calls herself. The active persona overrides this when one sets a display name.",
			PersonaOverridable: true,
		},
		{
			Key: "compact_prompt", Type: TypeString, Default: "",
			Group: GroupAdvanced, Title: "Compact instruction",
			Help: "The prompt document used when a conversation is compacted. Empty uses the built-in one.",
		},
		{
			Key: "prompts_dir", Type: TypeString, Default: "",
			Group: GroupAdvanced, Title: "Prompts folder",
			Help: "Where prompt documents are read from. Empty uses the folder inside Samantha's install root.",
		},
		{
			Key: "calibre_enabled", Type: TypeBool, Default: false,
			Group: GroupAdvanced, Title: "Calibre library",
			Help: "Lets her read books out of your Calibre library for audiobook narration.",
		},
		{
			Key: "calibre_library_path", Type: TypeString, Default: "",
			Group: GroupAdvanced, Title: "Calibre library folder",
			Help: "The library Calibre commands run against. Empty uses Calibre's own default library.",
		},
		{
			Key: "calibredb_binary", Type: TypeString, Default: "",
			Group: GroupAdvanced, Title: "calibredb path",
			Help: "Where the calibredb command lives. Empty looks it up on PATH.",
		},
		{
			Key: "calibre_convert_binary", Type: TypeString, Default: "",
			Group: GroupAdvanced, Title: "ebook-convert path",
			Help: "Where the ebook-convert command lives. Empty looks it up on PATH.",
		},
		{
			Key: "calibre_prefer_format", Type: TypeEnum, Default: "epub", Enum: calibreFormatEnum(),
			Group: GroupAdvanced, Title: "Preferred book format",
			Help: "Which format she reads when a book has several. Anything else is converted first.",
		},
	}
}

func modelKeys() []KeySpec {
	return []KeySpec{
		{
			Key: "stt_provider", Type: TypeEnum, Default: "sherpa", Enum: sttProviderEnum(),
			Group: GroupModels, Title: "Transcription engine",
			Help: "Which engine turns your speech into text. Sherpa runs locally and fastest.",
		},
		{
			Key: "stt_mode", Type: TypeEnum, Default: "", Enum: sttModeEnum(),
			Group: GroupModels, Title: "Transcription mode",
			Help: "Streaming transcribes as you speak; offline waits for the end of your turn and is more accurate.",
		},
		{
			Key: "sherpa_streaming_model", Type: TypeString, Default: "en-2023-06-26",
			Group: GroupModels, Title: "Streaming model",
			Help: "The zipformer model used in streaming mode.",
		},
		{
			Key: "whisper_model", Type: TypeString, Default: "small",
			Group: GroupModels, Title: "Whisper size",
			Help: "Which Whisper model size sherpa loads. Bigger is more accurate and slower.",
		},
		{
			Key: "whisper_quantized", Type: TypeBool, Default: true,
			Group: GroupModels, Title: "Quantized weights",
			Help: "Prefers int8 weights: much less memory, marginally less accurate.",
		},
		{
			Key: "whispercpp_binary", Type: TypeString, Default: "whisper-cli",
			Group: GroupModels, Title: "whisper.cpp path",
			Help: "Where the whisper.cpp command lives. A bare name is looked up on PATH.",
		},
		{
			Key: "whispercpp_model", Type: TypeString, Default: "base.en",
			Group: GroupModels, Title: "whisper.cpp model",
			Help: "Which ggml model whisper.cpp loads.",
		},
		{
			Key: "whispercpp_model_path", Type: TypeString, Default: defaultWhisperCPPModelPath(),
			Group: GroupModels, Title: "whisper.cpp model file",
			Help: "The exact ggml file to load, when it is not in the models folder.",
		},
		{
			Key: "models_dir", Type: TypeString, Default: DefaultModelsDir(),
			Group: GroupModels, Title: "Models folder",
			Help: "Where downloaded voice and speech models are cached. Moving it re-downloads everything.",
		},
		{
			Key: "qwen_tts_binary", Type: TypeString, Default: "",
			Group: GroupModels, Title: "Qwen worker path",
			Help: "An external Qwen worker to use instead of the installed one. Empty uses Samantha's own.",
		},
		{
			Key: "qwen_tts_model", Type: TypeString, Default: "",
			Group: GroupModels, Title: "Qwen model folder",
			Help: "The model an external Qwen worker loads. Empty uses the installed package.",
		},
		{
			Key: "qwen_tts_timeout", Type: TypeInt, Default: 120, Unit: "seconds", Min: num(1), Max: num(600),
			Group: GroupModels, Title: "Qwen request timeout",
			Help: "How long one Qwen synthesis may take before it is abandoned and the fallback engine speaks.",
		},
		{
			Key: "qwen_tts_mode", Type: TypeEnum, Default: "", Enum: qwenVoiceModeEnum(),
			Group: GroupModels, Title: "Qwen voice mode",
			Help: "How the Qwen voice is chosen. Empty means the built-in presets.",
		},
		{
			Key: "qwen_tts_voice", Type: TypeEnum, Default: "", Enum: qwenVoiceEnum(),
			Group: GroupModels, Title: "Qwen voice",
			Help:               "Which Qwen preset speaks. Empty uses Vivian.",
			PersonaOverridable: true,
		},
		{
			Key: "qwen_tts_language", Type: TypeEnum, Default: "", Enum: qwenLanguageEnum(),
			Group: GroupModels, Title: "Qwen language",
			Help: "The language Qwen speaks. Auto follows the text.",
		},
		{
			Key: "qwen_tts_instruction", Type: TypeString, Default: "",
			Group: GroupModels, Title: "Qwen delivery notes",
			Help: "Free-text direction for how Qwen performs a line. The 0.6b tier ignores it.",
		},
		{
			Key: "qwen_tts_reference_audio", Type: TypeString, Default: "",
			Group: GroupModels, Title: "Reference recording",
			Help: "A WAV of the voice to clone. Only used by the cloning modes.",
		},
		{
			Key: "qwen_tts_reference_text", Type: TypeString, Default: "",
			Group: GroupModels, Title: "Reference transcript",
			Help: "Exactly what is said in the reference recording. A wrong transcript degrades the clone.",
		},
		{
			Key: "qwen_tts_consent", Type: TypeBool, Default: false,
			Group: GroupModels, Title: "Cloning consent",
			Help: "Confirms you have permission to clone the reference voice. Cloning refuses without it.",
		},
		{
			Key: "qwen_tts_model_tier", Type: TypeEnum, Default: "0.6b", Enum: qwenTierEnum(),
			Group: GroupModels, Title: "Qwen model size",
			Help:               "Which installed Qwen model speaks. 1.7b sounds better and is slower.",
			PersonaOverridable: true,
		},
		{
			Key: "qwen_tts_native_url", Type: TypeString, Default: "",
			Group: GroupModels, Title: "Qwen package URL",
			Help: "Where the Qwen package is fetched from. Empty uses the release Samantha pins.",
		},
		{
			Key: "qwen_tts_native_sha256", Type: TypeString, Default: "",
			Group: GroupModels, Title: "Qwen package digest",
			Help: "Checksum the downloaded Qwen package must match. Empty skips the check.",
		},
		{
			Key: "speaker.models.embedding", Type: TypeString, Default: "",
			Group: GroupModels, Title: "Voice embedding model",
			Help: "Overrides the model that turns a voice into a fingerprint. Empty uses the bundled one.",
		},
		{
			Key: "speaker.models.segmentation", Type: TypeString, Default: "",
			Group: GroupModels, Title: "Speaker segmentation model",
			Help: "Overrides the model that splits a recording by speaker. Empty uses the bundled one.",
		},
	}
}

func hiddenKeys() []KeySpec {
	return []KeySpec{
		{
			Key: "active_persona", Type: TypeString, Default: "samantha",
			Group: GroupHidden, Title: "Active persona",
			Help:               "Set by the Personas screen.",
			PersonaOverridable: true, ManagedBy: "persona use",
		},
		{
			Key: "persona", Type: TypeString, Default: "samantha",
			Group: GroupHidden, Title: "Persona prompt",
			Help:               "Set by the Personas screen.",
			PersonaOverridable: true, ManagedBy: "persona use",
		},
		{
			Key: "tui_mouse_enabled", Type: TypeBool, Default: false,
			Group: GroupHidden, Title: "TUI mouse capture",
			Help: "Set from the terminal app; it has no effect on this Mac app.", ManagedBy: "samantha TUI",
		},
	}
}

func defaultWhisperCPPModelPath() string {
	return filepath.Join(DefaultModelsDir(), "whispercpp", "ggml-base.en.bin")
}
