# Samantha conversation TUI — voice stage (listening / hearing / speaking).
#
# Festival/termcast recording contract:
#   - real binary, real PTY, dark branded theme
#   - color env exported *inside* the shell (must clear NO_COLOR — agent/CI
#     shells often export NO_COLOR=1 which lipgloss honors as monochrome)
#
# From repo root after `just build`:
#   just demo-voice-meter

Output demos/voice-meter.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 15
Set Width 1100
Set Height 720
Set Padding 16
Set Margin 0
Set BorderRadius 0
Set Framerate 24
Set TypingSpeed 50ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#F2721C","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}

Hide
# Clear monochrome agent/CI env, then force a color-capable terminal.
Type "unset NO_COLOR; unset CLICOLOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1" Enter
Type "export TERM=xterm-256color COLORTERM=truecolor COLORFGBG=15\;0" Enter
Type "export PATH=$PWD/bin:$PATH" Enter
Type "export SAMANTHA_DEMO_VOICE_ANIM=1 SAMANTHA_COLOR_PROFILE=truecolor" Enter
Type "export HOME=/tmp/samantha-voice-meter-demo-home-$$" Enter
Type "rm -rf $HOME && mkdir -p $HOME/.obey/agents/voice/samantha/sessions" Enter
Type "cat > $HOME/.obey/agents/voice/samantha/config.yaml <<'EOF'" Enter
Type "brain_provider: ollama" Enter
Type "ollama_host: http://localhost:11434" Enter
Type "ollama_model: gemma4:26b" Enter
Type "voice_tools_enabled: false" Enter
Type "skills_enabled: false" Enter
Type "tts_provider: kokoro" Enter
Type "tts_voice: af_sky" Enter
Type "stt_provider: sherpa" Enter
Type "agent_name: Samantha" Enter
Type "language: en-US" Enter
Type "max_history: 10" Enter
Type "EOF" Enter
Type "export PS1=$'\033[38;5;208m❯\033[0m '" Enter
Type "clear" Enter
Show

# Real Bubble Tea UI (text+no-voice hardware; meter is bus-scripted for the demo).
Type "samantha -t -n" Sleep 300ms Enter
Sleep 2500ms

# New conversation
Enter
Sleep 1500ms

# Scripted meter cycle: listen → hear → think → speak → listen
Sleep 14000ms

Type "q"
Sleep 500ms
Type "q"
Sleep 400ms
