# VHS tape: skills + CLI tools demo for PR #98
# Run: vhs demos/skills-plus-cli.tape
# (User said "vsh"; this repo uses Charm VHS for terminal recordings.)

Output demos/skills-plus-cli.gif
Output demos/skills-plus-cli.mp4

Set Shell zsh
Set FontSize 14
Set Width 1100
Set Height 700
Set Theme "Catppuccin Mocha"
Set TypingSpeed 1ms
Set PlaybackSpeed 0.8

Type "cd /Users/lancerogers/Dev/AI/My_Tools/projects/worktrees/samantha/ollama-skills-SO0001"
Enter
Sleep 300ms

Type "echo '--- unit: skill load keeps full CLI tools ---'"
Enter
Sleep 200ms

Type "go test ./internal/brain/ -count=1 -v -run 'TestToolSessionHintsAllowedToolsKeepsCLI|TestToolSessionUnrestricted'"
Enter
Sleep 8s

Type "echo '--- unit: catalog + allowed-tools parse ---'"
Enter
Sleep 200ms

Type "go test ./internal/skills/ -count=1 -v -run 'TestCatalogFixtureDir|TestToolAllowed'"
Enter
Sleep 5s

Type "echo '--- interactive contract demo ---'"
Enter
Sleep 200ms

Type "go run ./scripts/demo_skills_plus_cli.go"
Enter
Sleep 4s

Type "echo 'DONE'"
Enter
Sleep 1s
