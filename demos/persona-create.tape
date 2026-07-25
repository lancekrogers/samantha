# Personas → Create: the boxed form wizard with the full vim prompt editor.
#
# Seeds samantha under a disposable HOME, creates "Research Buddy" end to
# end: name box → starter system prompt in the vim editor (normal mode,
# dd/undo, visual yank, o + insert) → model & voice step → save → edit reopens.
#
# From repo root after `just build`:
#   just demo-persona-create

Output demos/persona-create.gif

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
Type "export HOME=/tmp/samantha-persona-create-demo-$$" Enter
Type "export PATH=$PWD/bin:$PATH" Enter
Type "rm -rf $HOME && mkdir -p $HOME/.obey/agents/voice/festival-voice/personas/samantha $HOME/.obey/agents/voice/festival-voice/sessions" Enter
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
Type "clear" Enter
Show

# ── Launcher → Personas ──────────────────────────────────────────────
Type "samantha -t -n" Sleep 300ms Enter
Sleep 2s
Down
Sleep 600ms
Enter
Sleep 1500ms

# ── n opens the create form: boxed Name field, step pills ────────────
Type "n"
Sleep 1200ms
Type "Research Buddy"
Sleep 1200ms

# ── Enter → System prompt step: vim editor seeded with the starter ───
Enter
Sleep 2s

# esc: INSERT → NORMAL (mode chip in the box title flips)
Escape
Sleep 1200ms

# dd deletes the first line…
Type "gg"
Sleep 600ms
Type "dd"
Sleep 1200ms

# …and u undoes it
Type "u"
Sleep 1500ms

# visual mode: select three words and yank
Type "v"
Sleep 500ms
Type "eee"
Sleep 900ms
Type "y"
Sleep 900ms

# o opens a line at the bottom and drops into INSERT
Type "G"
Sleep 500ms
Type "o"
Sleep 500ms
Type "You love the outdoors and planning long trail hikes."
Sleep 1200ms
Escape
Sleep 900ms

# ── tab → Model & voice step: cycle brain provider with ←/→ ──────────
Tab
Sleep 1400ms
Right
Sleep 800ms
Left
Sleep 800ms

# ── ctrl+j commits the whole form from any step (:wq also saves from
#    the prompt editor) ───────────────────────────────────────────────
Ctrl+J
Sleep 2s

# ── e reopens the new persona in the edit form, prefilled ────────────
Type "e"
Sleep 1800ms
Escape
Sleep 1000ms

# ── back to the launcher; Personas hint shows Research Buddy ─────────
Escape
Sleep 1800ms
