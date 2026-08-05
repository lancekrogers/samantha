# Samantha conversation TUI — compact voice EQ (listen / hear / speak).
#
# Color contract for VHS (critical):
#   Agent shells often export NO_COLOR=1. Unset it in-shell and force ANSI so
#   lipgloss maps to the theme's bright 16 colors (cyan/yellow/magenta).
#
# Config path is ~/.obey/agents/voice/festival-voice (AppSlug). New conversation
# opens the persona picker — confirm the seeded Samantha profile with a second
# Enter. SAMANTHA_DEMO_VOICE_ANIM scripts the meter via bus events (no mic/TTS).
#
# From repo root after `just build`:
#   just demo-voice-meter

Output demos/voice-meter.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 16
Set Width 1000
Set Height 560
Set Padding 18
Set Margin 0
Set BorderRadius 0
Set Framerate 24
Set TypingSpeed 45ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}

Hide
Type "unset NO_COLOR; unset CLICOLOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1" Enter
Type "export TERM=xterm-256color COLORFGBG=15\;0" Enter
# ANSI profile → bright theme slots (VHS paints these reliably).
Type "export SAMANTHA_COLOR_PROFILE=ansi SAMANTHA_DEMO_VOICE_ANIM=1" Enter
Type "export PATH=$PWD/bin:$PATH" Enter
Type "export HOME=/tmp/samantha-voice-meter-demo-$$" Enter
Type "rm -rf $HOME && mkdir -p $HOME/.obey/agents/voice/festival-voice/sessions $HOME/.obey/agents/voice/festival-voice/personas/samantha" Enter
Type "cat > $HOME/.obey/agents/voice/festival-voice/config.yaml <<'EOF'" Enter
Type "brain_provider: ollama" Enter
Type "ollama_host: http://localhost:11434" Enter
Type "ollama_model: gemma4:26b" Enter
Type "tts_provider: kokoro" Enter
Type "tts_voice: af_heart" Enter
Type "agent_name: Samantha" Enter
Type "active_persona: samantha" Enter
Type "persona: samantha" Enter
Type "EOF" Enter
Type "cat > $HOME/.obey/agents/voice/festival-voice/personas/samantha/persona.yaml <<'EOF'" Enter
Type "schema: festival-voice.persona.v1" Enter
Type "id: samantha" Enter
Type "display_name: Samantha" Enter
Type "builtin: true" Enter
Type "tts:" Enter
Type "  provider: kokoro" Enter
Type "  voice: af_heart" Enter
Type "prompts:" Enter
Type "  persona: samantha" Enter
Type "  turn: samantha" Enter
Type "EOF" Enter
Type "clear" Enter

# Real Bubble Tea UI in a VHS PTY. Wait for the launcher to paint before
# sending Enter — a short post-launch sleep loses keys to the shell and the
# GIF freezes on the main menu.
Type "./bin/samantha -t -n"
Sleep 400ms
Enter
Sleep 6s
Show
Sleep 2s

# New conversation → persona picker (Samantha pre-selected) → start
Enter
Sleep 1200ms
Enter
# Runtime prepare can take a few seconds; scripted cycle is ~14s after ready.
Sleep 22s
Type "q"
Sleep 400ms
Type "q"
Sleep 300ms
