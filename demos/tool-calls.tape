# Main Samantha Bubble Tea TUI — live Ollama tool call.
# From repo root after `just build`:
#   vhs demos/tool-calls.tape
#
# Isolated HOME → empty sessions → cursor on "New conversation".

Output demos/tool-calls.gif

Set Shell "zsh"
Set FontSize 14
Set Width 1200
Set Height 700
Set Theme "Catppuccin Mocha"
Set TypingSpeed 10ms
Set PlaybackSpeed 0.85

Env HOME "/tmp/samantha-tui-demo-home"
Env VOICE_TOOLS_ENABLED "true"
Env SKILLS_ENABLED "true"
Env TERM "xterm-256color"

# Prepare isolated config (no sessions → New conversation is first)
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

# Launch MAIN TUI (not --no-tui)
Type "./bin/samantha -t -n"
Enter
Sleep 5s

# Launcher: New conversation selected → enter
Enter
# Runtime build / conversation screen
Sleep 8s

# Composer: request a real tool call
Type "Call list_files with path . then name the first three entries from the tool result. You must use list_files."
Enter
# Ollama + tool round-trip
Sleep 40s

# Quit conversation then launcher
Type "q"
Sleep 1s
Type "q"
Sleep 1s
Type "echo DEMO_DONE"
Enter
Sleep 1s
