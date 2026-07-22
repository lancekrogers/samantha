# Managed Qwen preset voices in the real Samantha Settings TUI.
#
# Deterministic: creates the same completion-marker/file layout inspected by
# production code, without downloading the multi-GB model for a visual demo.
# From repo root:
#   just demo-qwen-voices

Output demos/qwen-voices.gif

Set Shell "bash"
Set FontFamily "JetBrainsMono Nerd Font"
Set FontSize 15
Set Width 1120
Set Height 660
Set Padding 16
Set Margin 0
Set BorderRadius 0
Set Framerate 24
Set TypingSpeed 35ms
Set Theme {"name":"termcast","background":"#0d0d11","foreground":"#f8f8f2","cursor":"#8BE9FD","selection":"#2a2a33","black":"#16161a","red":"#ff5f57","green":"#50fa7b","yellow":"#febc2e","blue":"#57a6ff","magenta":"#bd93f9","cyan":"#8be9fd","white":"#f8f8f2","brightBlack":"#6b7280","brightRed":"#ff6e67","brightGreen":"#69ff94","brightYellow":"#ffd23f","brightBlue":"#7ab8ff","brightMagenta":"#d0a9ff","brightCyan":"#8be9fd","brightWhite":"#ffffff"}

Hide
Type "unset NO_COLOR; unset CLICOLOR; export CLICOLOR_FORCE=1 FORCE_COLOR=1" Enter
Type "export TERM=xterm-256color COLORFGBG=15\;0 SAMANTHA_COLOR_PROFILE=ansi" Enter
Type "export PATH=$PWD/bin:$PATH" Enter
Type "export HOME=/tmp/samantha-qwen-voices-demo-$$" Enter
Type "export MODELS=$HOME/.cache/samantha/models" Enter
Type "export QWEN_ROOT=$MODELS/qwen3-tts" Enter
Type "export QWEN_REV=85e237c12c027371202489a0ec509ded67b5e4b5" Enter
Type "export QWEN_MODEL=$QWEN_ROOT/models/customvoice-0.6b/$QWEN_REV" Enter
Type "rm -rf $HOME && mkdir -p $HOME/.obey/agents/voice/samantha/sessions $QWEN_ROOT/runtime/qwen-tts-0.1.1/bin $QWEN_ROOT/worker $QWEN_MODEL/speech_tokenizer" Enter
Type "touch $QWEN_ROOT/runtime/qwen-tts-0.1.1/bin/python $QWEN_ROOT/runtime/qwen-tts-0.1.1/.qwen-tts-0.1.1 $QWEN_ROOT/worker/qwen_worker.py $QWEN_MODEL/config.json $QWEN_MODEL/model.safetensors $QWEN_MODEL/speech_tokenizer/model.safetensors" Enter
Type "echo eyJzY2hlbWEiOiJzYW1hbnRoYS5xd2VuLmluc3RhbGwudjEiLCJwYWNrYWdlIjoiMC4xLjEiLCJ3b3JrZXIiOiIxIiwibW9kZWxfaWQiOiJRd2VuL1F3ZW4zLVRUUy0xMkh6LTAuNkItQ3VzdG9tVm9pY2UiLCJtb2RlbF9yZXZpc2lvbiI6Ijg1ZTIzN2MxMmMwMjczNzEyMDI0ODlhMGVjNTA5ZGVkNjdiNWU0YjUiLCJpbnN0YWxsZWRfYXQiOiIyMDI2LTA3LTIxVDAwOjAwOjAwWiJ9 | base64 -d > $QWEN_ROOT/install.json" Enter
Type "cat > $HOME/.obey/agents/voice/samantha/config.yaml <<EOF" Enter
Type "brain_provider: ollama" Enter
Type "ollama_host: http://localhost:11434" Enter
Type "ollama_model: demo-model" Enter
Type "tts_provider: kokoro" Enter
Type "tts_voice: af_heart" Enter
Type "models_dir: $MODELS" Enter
Type "agent_name: Samantha" Enter
Type "EOF" Enter
Type "clear" Enter
Show

# Launcher → Settings (New conversation, Meeting, Remote, Library, Audiobook, Settings).
Type "samantha -t -n" Sleep 300ms Enter
Sleep 1800ms
Down
Sleep 180ms
Down
Sleep 180ms
Down
Sleep 180ms
Down
Sleep 180ms
Down
Sleep 650ms
Enter
Sleep 1100ms

# Brain → Brain model → Tools → TTS.
Right
Sleep 220ms
Right
Sleep 220ms
Right
Sleep 1300ms

# Qwen is installed but Kokoro remains active until explicit selection.
Down
Sleep 1500ms
Enter
Sleep 1700ms

# Voice now contains the nine model-native Qwen speakers.
Right
Sleep 2100ms

# Select Ryan and hold the checked state.
Down
Sleep 180ms
Down
Sleep 180ms
Down
Sleep 180ms
Down
Sleep 180ms
Down
Sleep 700ms
Enter
Sleep 1800ms

# Return to the launcher: the status badge reflects Qwen + CustomVoice + Ryan.
Escape
Sleep 2300ms
Type "q"
Sleep 400ms
