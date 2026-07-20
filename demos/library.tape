# Samantha Calibre Library browser/viewer (launcher → browse → detail → search → audiobook).
#
# Deterministic: uses demos/fixtures/fake-calibredb (no real Calibre required).
# Full-color VHS contract:
#   - env -u NO_COLOR at the just recipe
#   - SAMANTHA_COLOR_PROFILE=ansi so lipgloss maps to the theme's bright 16 colors
#
# From repo root after `just build`:
#   just demo-library

Output demos/library.gif

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
Type "export SAMANTHA_COLOR_PROFILE=ansi" Enter
Type "export SAMANTHA_DEMO_BOOKS=/tmp/samantha-vhs/books" Enter
Type "export HOME=/tmp/samantha-library-demo-$$" Enter
Type "rm -rf $HOME && mkdir -p $HOME/.obey/agents/voice/samantha/sessions /tmp/samantha-vhs/books" Enter
Type "chmod +x demos/fixtures/fake-calibredb" Enter
Type "export PATH=$PWD/demos/fixtures:$PWD/bin:$PATH" Enter
Type "cat > $HOME/.obey/agents/voice/samantha/config.yaml <<'EOF'" Enter
Type "brain_provider: ollama" Enter
Type "ollama_host: http://localhost:11434" Enter
Type "ollama_model: demo-model" Enter
Type "tts_voice: af_heart" Enter
Type "agent_name: Samantha" Enter
Type "calibre_enabled: true" Enter
Type "calibredb_binary: fake-calibredb" Enter
Type "EOF" Enter
Type "clear" Enter
Show

# Main launcher — navigate to Library
Type "samantha -t -n" Sleep 300ms Enter
Sleep 2s

# New conversation → Meeting → Use on another device → Library (4th item)
Down
Sleep 400ms
Down
Sleep 400ms
Down
Sleep 800ms
# linger on Library so the hint reads
Sleep 1200ms
Enter
Sleep 1800ms

# Browse list is loaded — move through books
Down
Sleep 700ms
Down
Sleep 700ms
Up
Sleep 600ms

# Open book details
Enter
Sleep 2000ms

# Hold metadata + description in frame
Sleep 1800ms

# Back to list
Escape
Sleep 1000ms

# Search filter
Type "/"
Sleep 400ms
Type "go"
Sleep 500ms
Enter
Sleep 1400ms

# Select first result and send to Create audiobook
Enter
Sleep 1500ms
Type "a"
Sleep 2000ms

# Hold filled audiobook form
Sleep 1800ms

# Quit
Type "q"
Sleep 500ms
