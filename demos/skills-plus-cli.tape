# VHS tape: skills + CLI tools demo for PR #98
# Run from repo root: vhs demos/skills-plus-cli.tape
# No machine-specific paths (portable for public repo).

Output demos/skills-plus-cli.gif

Set Shell zsh
Set FontSize 14
Set Width 1100
Set Height 700
Set Theme "Catppuccin Mocha"
Set TypingSpeed 1ms
Set PlaybackSpeed 0.8

# Assume cwd is already the repo root (CI / local: cd first yourself).
Type "pwd | sed 's|.*/|…/|'"
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
