# Install
[private]
default:
    @just --list --unsorted --justfile {{source_file()}}

# Install to $GOBIN
[no-cd]
global:
    go install ./cmd/samantha
