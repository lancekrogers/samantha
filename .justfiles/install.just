# Install recipes
binary_name := "samantha"
gobin := env_var_or_default("GOBIN", `go env GOPATH` + "/bin")

[private]
default:
    @just --list --unsorted --justfile {{source_file()}}

# Install to $GOBIN
[no-cd]
global:
    go install ./cmd/samantha
    @echo "samantha installed to {{gobin}}"

# Uninstall from $GOBIN
[no-cd]
uninstall:
    #!/usr/bin/env bash
    if [ -f {{gobin}}/{{binary_name}} ]; then
        rm {{gobin}}/{{binary_name}}
        echo "samantha uninstalled from {{gobin}}"
    else
        echo "samantha not found in {{gobin}}"
    fi
