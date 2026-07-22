# Settings → Persona: list and switch active voice agent.
#
# Seeds two profiles (samantha + festival) under a disposable HOME so the
# real Bubble Tea Settings Persona tab is exercised end-to-end.
#
# From repo root after `just build`:
#   just demo-persona-switch

Output demos/persona-switch.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 14
Set Width 1180
Set Height 680
Set Padding 16
Set Margin 0
Set BorderRadius 0
Set Framerate 24
Set TypingSpeed 35ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}

Hide
Type "unset NO_COLOR; unset CLICOLOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1" Enter
Type "export TERM=xterm-256color COLORFGBG=15\;0" Enter
Type "export SAMANTHA_COLOR_PROFILE=ansi" Enter
Type "export HOME=/tmp/samantha-persona-demo-$$" Enter
Type "export PATH=$PWD/bin:$PATH" Enter
Type "rm -rf $HOME && mkdir -p $HOME/.obey/agents/voice/festival-voice/personas/samantha $HOME/.obey/agents/voice/festival-voice/personas/festival $HOME/.obey/agents/voice/festival-voice/sessions" Enter
Type "cat > $HOME/.obey/agents/voice/festival-voice/config.yaml <<'EOF'" Enter
Type "brain_provider: ollama" Enter
Type "ollama_host: http://localhost:11434" Enter
Type "ollama_model: demo-model" Enter
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
Type "cat > $HOME/.obey/agents/voice/festival-voice/personas/festival/persona.yaml <<'EOF'" Enter
Type "schema: festival-voice.persona.v1" Enter
Type "id: festival" Enter
Type "display_name: Festival" Enter
Type "builtin: true" Enter
Type "tts:" Enter
Type "  provider: kokoro" Enter
Type "  voice: af_bella" Enter
Type "prompts:" Enter
Type "  persona: samantha" Enter
Type "  turn: samantha" Enter
Type "EOF" Enter
Type "clear" Enter
Show

# ── Launcher → Settings ──────────────────────────────────────────────
Type "samantha -t -n" Sleep 300ms Enter
Sleep 2s

# New conversation, Meeting, Remote, Library, Audiobook, Settings (5 downs)
Down
Sleep 250ms
Down
Sleep 250ms
Down
Sleep 250ms
Down
Sleep 250ms
Down
Sleep 900ms
Enter
Sleep 1400ms

# Settings opens on Persona (first tab) with Samantha ✓ and Festival listed
Sleep 1600ms

# Move to Festival and switch
Down
Sleep 700ms
Enter
Sleep 1600ms

# Show active check moved to Festival
Sleep 1200ms

# Esc back to launcher — active persona name is Festival
Escape
Sleep 1800ms
