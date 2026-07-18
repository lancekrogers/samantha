# Real tool-calling demo (text conversation UI, not unit tests).
# Requires: Ollama up, model with tools, from repo root after `just build`.
#
#   VOICE_TOOLS_ENABLED=true vhs demos/tool-calls.tape
#
# Shows Samantha invoking list_files via the live pipeline and printing
# tool start/finish lines in the conversation UI.

Env VOICE_TOOLS_ENABLED "true"
Env SKILLS_ENABLED "true"

Output demos/tool-calls.gif

Set Shell zsh
Set FontSize 14
Set Width 1100
Set Height 640
Set Theme "Catppuccin Mocha"
Set TypingSpeed 8ms
Set PlaybackSpeed 0.85

Type "export VOICE_TOOLS_ENABLED=true SKILLS_ENABLED=true"
Enter
Sleep 200ms

# Plain conversation UI without mic (text + no-voice). Shows tool lines.
Type "./bin/samantha -t -n --no-tui"
Enter
Sleep 2500ms

Type "Call list_files with path '.' then name the first three entries from the tool result."
Enter
# Wait for model + tool round-trip (gemma4 can take a bit)
Sleep 25s

Type "/quit"
Enter
Sleep 5s

Type "echo DONE"
Enter
Sleep 1s
