# Demos

## skills-plus-cli

Shows **skills + CLI tools**: `allowed-tools` is a soft hint; `write_file` / `run_command` stay available after skill load.

| File | Purpose |
|------|---------|
| `skills-plus-cli.gif` | Terminal recording |
| `skills-plus-cli.tape` | VHS source (regenerate with `vhs demos/skills-plus-cli.tape`) |
| `../scripts/demo_skills_plus_cli.go` | Contract script (`go run ./scripts/demo_skills_plus_cli.go`) |

```bash
go test ./internal/brain/ -count=1 -v -run TestToolSessionHintsAllowedToolsKeepsCLI
go run ./scripts/demo_skills_plus_cli.go
vhs demos/skills-plus-cli.tape   # optional: rebuild gif
```
