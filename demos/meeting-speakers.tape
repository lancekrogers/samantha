# Multi-voice meeting + speaker analysis status (VHS, full color).
#
# Scripts a product-style multi-speaker conversation and shows speaker
# analysis status progressing queued → running → complete. No live mic.
#
#   just demo-meeting-speakers

Output demos/meeting-speakers.gif

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
Type "export SAMANTHA_COLOR_PROFILE=ansi SAMANTHA_DEMO_MEETING_SPEAKERS=1" Enter
Type "export HOME=/tmp/samantha-speakers-demo-$$" Enter
Type "rm -rf $HOME && mkdir -p $HOME/.obey/agents/voice/samantha/sessions $HOME/.obey/agents/voice/samantha/meetings" Enter
Type "export PATH=$PWD/bin:$PATH" Enter
Type "cat > $HOME/.obey/agents/voice/samantha/config.yaml <<'EOF'" Enter
Type "brain_provider: ollama" Enter
Type "ollama_host: http://localhost:11434" Enter
Type "ollama_model: demo-model" Enter
Type "tts_voice: af_sky" Enter
Type "agent_name: Samantha" Enter
Type "speaker:" Enter
Type "  enabled: true" Enter
Type "  meeting:" Enter
Type "    enabled: true" Enter
Type "    num_speakers: 3" Enter
Type "EOF" Enter
Type "clear" Enter
Show

Type "samantha -t -n" Sleep 300ms Enter
Sleep 2s

# Open Meeting (second item after New conversation)
Down
Sleep 800ms
Enter
Sleep 900ms

# Title
Type "Product marketing sync"
Sleep 600ms
Enter
Sleep 1400ms

# Route step: keep local
Sleep 800ms
Enter
Sleep 1800ms

# Multi-speaker dialogue plays (demo STT)
Sleep 9000ms

# Hold complete analysis status + labeled turns
Sleep 2500ms

Ctrl+C
Sleep 1500ms
Type "q"
Sleep 400ms
