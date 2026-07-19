# Samantha main launcher + Meeting recorder (notes, ★ bookmarks, voice EQ).
#
# Full-color VHS contract (critical for agent/CI shells):
#   - env -u NO_COLOR at the just recipe
#   - unset NO_COLOR inside the shell
#   - SAMANTHA_COLOR_PROFILE=ansi so lipgloss maps to the theme's bright 16 colors
#   - SAMANTHA_DEMO_MEETING=1 scripts STT events without a live mic
#
# From repo root after `just build`:
#   just demo-meeting

Output demos/meeting.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 15
Set Width 1040
Set Height 640
Set Padding 16
Set Margin 0
Set BorderRadius 0
Set Framerate 24
Set TypingSpeed 40ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#a4f0ff","brightWhite":"#ffffff"}

Hide
Type "unset NO_COLOR; unset CLICOLOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1" Enter
Type "export TERM=xterm-256color COLORFGBG=15\;0" Enter
Type "export SAMANTHA_COLOR_PROFILE=ansi SAMANTHA_DEMO_MEETING=1" Enter
Type "export PATH=$PWD/bin:$PATH" Enter
Type "export HOME=/tmp/samantha-meeting-demo-$$" Enter
Type "rm -rf $HOME && mkdir -p $HOME/.obey/agents/voice/samantha/sessions $HOME/.obey/agents/voice/samantha/meetings" Enter
Type "cat > $HOME/.obey/agents/voice/samantha/config.yaml <<'EOF'" Enter
Type "brain_provider: ollama" Enter
Type "ollama_host: http://localhost:11434" Enter
Type "ollama_model: gemma4:26b" Enter
Type "tts_voice: af_sky" Enter
Type "agent_name: Samantha" Enter
Type "EOF" Enter
Type "clear" Enter
Show

# Main launcher — restyled brand plate + Meeting entry
Type "samantha -t -n" Sleep 300ms Enter
Sleep 2s

# Move to Meeting (no sessions → New conversation is first, Meeting is second)
Down
Sleep 600ms
# linger on Meeting selection so the hint reads
Sleep 1400ms
Enter
Sleep 1000ms

# Title screen
Type "Q3 planning sync"
Sleep 600ms
Enter
Sleep 1500ms

# Live recorder: scripted speech lands; type a note
Sleep 3500ms
Type "follow up with finance on headcount"
Sleep 400ms
Enter
Sleep 900ms

# Mark an important moment with a caption
Type "budget decision"
Sleep 350ms
Ctrl+B
Sleep 1500ms

# Hold the EQ + timeline in frame
Sleep 2500ms

# Stop → back to launcher
Ctrl+C
Sleep 1200ms

# Show launcher again briefly
Sleep 1000ms
Type "q"
Sleep 400ms
