# Release tasks for Samantha.
#
# Samantha ships as a build-from-source Homebrew formula (it's a cgo/native
# build, so there are no cross-compiled binaries). A release is just a tag:
# GitHub publishes the source tarball, then the tap formula points at it.

[private]
default:
    @just --list --justfile {{source_file()}}

# Tag and push a release after tests pass. Usage: just release tag v0.1.0
[no-cd]
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

# Produces dist/samantha-macos-arm64.tar.gz: the CGO-built binary plus every
# non-system sherpa-onnx/onnxruntime dylib it needs, laid out so the binary
# finds them via @executable_path/lib from any directory, with a manifest.json
# (git-describe version + per-file sha256). The Obey Voice Mac app vendors this
# artifact; signing happens in the app's release pipeline (delta D5,
# workflow/design/obey-voice-apps/PROTOCOL_DELTAS.md).
#
# Build a relocatable, self-contained macOS arm64 release artifact
[no-cd]
macos-artifact:
    #!/usr/bin/env bash
    set -euo pipefail

    if [[ "$(uname -s)" != "Darwin" || "$(uname -m)" != "arm64" ]]; then
        echo "macos-artifact builds a native arm64 artifact and must run on Apple Silicon macOS" >&2
        exit 1
    fi

    platform="macos-arm64"
    stage="dist/samantha-${platform}"
    tarball="dist/samantha-${platform}.tar.gz"
    bin="$stage/samantha"
    libdir="$stage/lib"

    rm -rf "$stage" "$tarball"
    mkdir -p "$libdir"

    echo "Building CGO arm64 binary…"
    CGO_ENABLED=1 GOOS=darwin GOARCH=arm64 \
        go build -trimpath -o "$bin" ./cmd/samantha

    # The cgo LDFLAGS bake the sherpa-onnx module-cache dir in as an LC_RPATH;
    # that is where @rpath dylibs are found before relocation.
    mapfile -t rpaths < <(otool -l "$bin" | awk '$1=="cmd"&&$2=="LC_RPATH"{g=1} g&&$1=="path"{print $2; g=0}')

    locate_dep() {
        local base="$1" rp
        for rp in "${rpaths[@]}"; do
            [[ "$rp" == @* ]] && continue
            [[ -f "$rp/$base" ]] && { echo "$rp/$base"; return 0; }
        done
        return 1
    }

    # Recursively copy every non-system (@rpath) dylib the binary and its
    # dylibs reference (otool -L), resolving each from the baked-in rpaths.
    declare -A copied
    worklist=("$bin")
    while ((${#worklist[@]})); do
        cur="${worklist[0]}"; worklist=("${worklist[@]:1}")
        while read -r dep; do
            [[ "$dep" == @rpath/* ]] || continue
            base="${dep#@rpath/}"
            [[ -n "${copied[$base]:-}" ]] && continue
            src="$(locate_dep "$base")" || { echo "cannot locate dylib $base in build rpaths" >&2; exit 1; }
            cp "$src" "$libdir/$base"
            chmod u+w "$libdir/$base"
            copied[$base]=1
            worklist+=("$libdir/$base")
        done < <(otool -L "$cur" | tail -n +2 | awk '{print $1}')
    done

    (( ${#copied[@]} )) || { echo "no bundled dylibs discovered — expected sherpa-onnx/onnxruntime" >&2; exit 1; }
    echo "Bundled ${#copied[@]} dylib(s): ${!copied[*]}"

    # Relocate: the binary finds its libs next to it; drop absolute build rpaths.
    install_name_tool -add_rpath "@executable_path/lib" "$bin"
    for rp in "${rpaths[@]}"; do
        [[ "$rp" == @* ]] && continue
        install_name_tool -delete_rpath "$rp" "$bin" 2>/dev/null || true
    done
    # Each dylib resolves its siblings from its own directory.
    for base in "${!copied[@]}"; do
        install_name_tool -add_rpath "@loader_path" "$libdir/$base" 2>/dev/null || true
    done

    # install_name_tool invalidates the signature; arm64 needs a valid one to
    # load. Ad-hoc re-sign here; the app pipeline re-signs with a real identity.
    for base in "${!copied[@]}"; do
        codesign --force --sign - "$libdir/$base"
    done
    codesign --force --sign - "$bin"

    # manifest.json: version + every packaged file's sha256 (computed before the
    # manifest exists so it never lists itself).
    version="$(git describe --tags --always --dirty 2>/dev/null || echo dev)"
    built_at="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
    mapfile -t relfiles < <(cd "$stage" && find . -type f | sed 's|^\./||' | sort)
    {
        printf '{\n'
        printf '  "name": "samantha",\n'
        printf '  "version": "%s",\n' "$version"
        printf '  "platform": "%s",\n' "$platform"
        printf '  "built_at": "%s",\n' "$built_at"
        printf '  "files": [\n'
        first=1
        for rel in "${relfiles[@]}"; do
            sha="$(shasum -a 256 "$stage/$rel" | cut -d' ' -f1)"
            (( first )) && first=0 || printf ',\n'
            printf '    {"path": "%s", "sha256": "%s"}' "$rel" "$sha"
        done
        printf '\n  ]\n}\n'
    } > "$stage/manifest.json"

    tar -czf "$tarball" -C dist "samantha-${platform}"
    echo
    echo "Artifact: $tarball"
    shasum -a 256 "$tarball"
    echo "Contents:"
    tar -tzf "$tarball"
