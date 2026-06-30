# Release tasks for Samantha.
#
# Samantha ships as a build-from-source Homebrew formula (it's a cgo/native
# build, so there are no cross-compiled binaries). A release is just a tag:
# GitHub publishes the source tarball, then the tap formula points at it.

[private]
default:
    @just --list --justfile {{source_file()}}

# Tag and push a release after tests pass. Usage: just release tag v0.1.0
tag version:
    #!/usr/bin/env bash
    set -euo pipefail
    [ -z "$(git status --porcelain)" ] || { echo "working tree is dirty — commit or stash first" >&2; exit 1; }
    echo "Running tests…"
    go test ./internal/... ./cmd/...
    git tag -a "{{version}}" -m "Samantha {{version}}"
    git push origin "{{version}}"
    echo
    echo "Tagged {{version}}. Next:"
    echo "  just release publish {{version}}      # create the GitHub release"
    echo "  just release formula-sha {{version}}  # values for the Homebrew formula"

# Create the GitHub release for a pushed tag (auto-generated notes).
publish version:
    gh release create "{{version}}" --title "Samantha {{version}}" --generate-notes

# Print the url + sha256 to paste into the Homebrew tap formula.
formula-sha version:
    #!/usr/bin/env bash
    set -euo pipefail
    url="https://github.com/lancekrogers/samantha/archive/refs/tags/{{version}}.tar.gz"
    sha="$(curl -fsSL "$url" | shasum -a 256 | cut -d' ' -f1)"
    echo "lancekrogers/homebrew-tap → Formula/samantha.rb:"
    echo "  url \"$url\""
    echo "  sha256 \"$sha\""
