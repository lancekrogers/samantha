# Main Samantha Bubble Tea TUI — live Ollama list_files tool call.
# From repo root after `just build`:
#   vhs demos/tool-calls.tape

Output demos/tool-calls.gif

# Settings first (VHS requires this before Env/Type).
Set Shell "zsh"
Set FontSize 15
Set Width 1280
Set Height 720
# Brighter palette so lipgloss ANSI colors (cyan/green/blue) read clearly.
# Light theme so lipgloss accents (blue/green/pink) stay vivid in the GIF.
Set Theme "Catppuccin Latte"
Set TypingSpeed 10ms
Set PlaybackSpeed 0.85

Env HOME "/tmp/samantha-tui-demo-home"
Env VOICE_TOOLS_ENABLED "true"
Env SKILLS_ENABLED "true"
Env TERM "xterm-256color"
Env COLORTERM "truecolor"
Env CLICOLOR_FORCE "1"
Env FORCE_COLOR "1"

# Isolated HOME: empty sessions → cursor on New conversation.
Type "rm -rf /tmp/samantha-tui-demo-home && mkdir -p /tmp/samantha-tui-demo-home/.obey/agents/voice/samantha/sessions"
Enter
Type "cat > /tmp/samantha-tui-demo-home/.obey/agents/voice/samantha/config.yaml <<'EOF'"
Enter
Type "brain_provider: ollama"
Enter
Type "ollama_host: http://localhost:11434"
Enter
Type "ollama_model: gemma4:26b"
Enter
Type "voice_tools_enabled: true"
Enter
Type "skills_enabled: true"
Enter
Type "tts_provider: kokoro"
Enter
Type "tts_voice: af_sky"
Enter
Type "stt_provider: sherpa"
Enter
Type "agent_name: Samantha"
Enter
Type "language: en-US"
Enter
Type "max_history: 10"
Enter
Type "EOF"
Enter
Sleep 300ms

# MAIN Bubble Tea UI (not --no-tui)
Type "./bin/samantha -t -n"
Enter
Sleep 5s

# New conversation
Enter
Sleep 8s

# Live tool call
Type "Call list_files with path . then name the first three entries from the tool result. You must use list_files."
Enter
Sleep 40s

Type "q"
Sleep 1s
Type "q"
Sleep 1s
