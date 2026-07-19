# Samantha conversation TUI — voice meter animation (listening / hearing / speaking).
# Termcast recording contract: real binary, real PTY, dark branded terminal.
# From repo root after `just build`:
#   just demo-voice-meter

Output demos/voice-meter.gif

# Match termcast's real-TUI geometry and Obedience terminal palette.
Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 18
Set Width 1040
Set Height 620
Set Padding 22
Set Margin 0
Set BorderRadius 0
Set Framerate 30
Set TypingSpeed 55ms
Set Theme { "name": "termcast", "background": "#0d0d11", "foreground": "#f8f8f2", "cursor": "#F2721C", "selection": "#2a2a33", "black": "#16161a", "red": "#ff5f57", "green": "#50fa7b", "yellow": "#febc2e", "blue": "#57a6ff", "magenta": "#bd93f9", "cyan": "#8be9fd", "white": "#f8f8f2", "brightBlack": "#6b7280", "brightRed": "#ff6e67", "brightGreen": "#69ff94", "brightYellow": "#ffd23f", "brightBlue": "#7ab8ff", "brightMagenta": "#d0a9ff", "brightCyan": "#a4f0ff", "brightWhite": "#ffffff" }

Env HOME "/tmp/samantha-voice-meter-demo-home"
Env SAMANTHA_DEMO_VOICE_ANIM "1"
Env TERM "xterm-256color"
Env COLORTERM "truecolor"
Env COLORFGBG "15;0"

# Keep fixture/config setup out of the recording.
Hide

Type "rm -rf /tmp/samantha-voice-meter-demo-home && mkdir -p /tmp/samantha-voice-meter-demo-home/.obey/agents/voice/samantha/sessions"
Enter
Type "cat > /tmp/samantha-voice-meter-demo-home/.obey/agents/voice/samantha/config.yaml <<'EOF'"
Enter
Type "brain_provider: ollama"
Enter
Type "ollama_host: http://localhost:11434"
Enter
Type "ollama_model: gemma4:26b"
Enter
Type "voice_tools_enabled: false"
Enter
Type "skills_enabled: false"
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
Type "clear"
Enter

# Text + no-voice: real Bubble Tea conversation without mic/TTS hardware.
# SAMANTHA_DEMO_VOICE_ANIM scripts the meter events on the live bus.
Type "./bin/samantha -t -n"
Sleep 400ms
Enter
Sleep 4s

Show
Sleep 1500ms
Enter
Sleep 14s

Ctrl+C
Sleep 600ms
