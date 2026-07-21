# Meeting route picker + Speaker settings (PR UX flow).
#
# Full-color VHS contract:
#   - env -u NO_COLOR at the just recipe
#   - unset NO_COLOR inside the shell
#   - SAMANTHA_COLOR_PROFILE=ansi so lipgloss maps to bright 16 colors
#   - demos/fixtures/camp stubs `camp list --json` + `camp idea add`
#   - SAMANTHA_DEMO_MEETING=1 scripts STT without a live mic
#
# From repo root after `just build`:
#   just demo-meeting-route-speaker

Output demos/meeting-route-speaker.gif

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
Type "export SAMANTHA_COLOR_PROFILE=ansi SAMANTHA_DEMO_MEETING=1" Enter
Type "export HOME=/tmp/samantha-route-demo-$$" Enter
Type "rm -rf $HOME && mkdir -p $HOME/.obey/agents/voice/samantha/sessions $HOME/.obey/agents/voice/samantha/meetings" Enter
Type "chmod +x demos/fixtures/camp" Enter
Type "export PATH=$PWD/demos/fixtures:$PWD/bin:$PATH" Enter
Type "cat > $HOME/.obey/agents/voice/samantha/config.yaml <<'EOF'" Enter
Type "brain_provider: ollama" Enter
Type "ollama_host: http://localhost:11434" Enter
Type "ollama_model: demo-model" Enter
Type "tts_voice: af_sky" Enter
Type "agent_name: Samantha" Enter
Type "meeting:" Enter
Type "  route:" Enter
Type "    mode: ask" Enter
Type "    body: notes" Enter
Type "    destinations: []" Enter
Type "speaker:" Enter
Type "  enabled: false" Enter
Type "  meeting:" Enter
Type "    enabled: false" Enter
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
Sleep 1200ms

# Settings opens on Brain — tab to Meeting (7 rights)
Right
Sleep 200ms
Right
Sleep 200ms
Right
Sleep 200ms
Right
Sleep 200ms
Right
Sleep 200ms
Right
Sleep 200ms
Right
Sleep 1400ms

# Meeting tab: destinations discovery + refresh
Down
Sleep 300ms
Down
Sleep 300ms
Down
Sleep 300ms
Down
Sleep 300ms
Down
Sleep 700ms
# Refresh destinations (camp list + config)
Enter
Sleep 1800ms

# Tab to Speaker
Right
Sleep 1400ms

# Enable speaker analysis
Enter
Sleep 900ms
# Enable meeting diarization
Down
Sleep 400ms
Enter
Sleep 1200ms

# Back to launcher
Escape
Sleep 1200ms

# ── Meeting start → route picker ─────────────────────────────────────
# Settings is selected; go up to Meeting (Settings→Audiobook→Library→Remote→Meeting = 4 ups)
Up
Sleep 250ms
Up
Sleep 250ms
Up
Sleep 250ms
Up
Sleep 900ms
Enter
Sleep 1000ms

# Step 1/2 — title
Type "Q3 planning sync"
Sleep 700ms
Enter
Sleep 1600ms

# Step 2/2 — destinations (keep local, ask, My_Tools, obey-campaign, JobSearch)
# Default cursor is "ask"; move to My_Tools campaign
Down
Sleep 600ms
Sleep 1000ms
# linger so the campaign list reads
Sleep 1500ms
Enter
Sleep 1800ms

# Live recorder (demo STT) — brief hold
Sleep 3200ms
Type "route notes to campaign"
Sleep 350ms
Enter
Sleep 1200ms

# Stop → auto-route to chosen campaign destination
Ctrl+C
Sleep 2000ms

# Launcher banner with route result
Sleep 1400ms
Type "q"
Sleep 400ms
