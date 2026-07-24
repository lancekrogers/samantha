# Release tasks for Samantha.
#
# Samantha uses native CGO builds. Homebrew builds macOS from source; release
# workflows also publish relocatable native artifacts for supported platforms.

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

# Produces dist/samantha-linux-amd64.tar.gz: the native CGO binary plus the
# sherpa-onnx/onnxruntime shared libraries it needs. patchelf replaces the
# module-cache RUNPATH injected by sherpa's cgo flags with $ORIGIN/lib, then an
# extraction smoke test proves the tarball runs without the builder's module
# cache or LD_LIBRARY_PATH.
#
# Build a relocatable, self-contained glibc Linux amd64 release artifact
[no-cd]
linux-artifact:
    #!/usr/bin/env bash
    set -euo pipefail

    if [[ "$(uname -s)" != "Linux" || "$(uname -m)" != "x86_64" ]]; then
        echo "linux-artifact builds a native amd64 artifact and must run on x86_64 Linux" >&2
        exit 1
    fi
    for tool in curl patchelf readelf ldd sha256sum strings; do
        command -v "$tool" >/dev/null || {
            echo "linux-artifact requires $tool (Arch: sudo pacman -S curl patchelf binutils; Debian/Ubuntu: sudo apt install curl patchelf binutils)" >&2
            exit 1
        }
    done

    platform="linux-amd64"
    stage="dist/samantha-${platform}"
    tarball="dist/samantha-${platform}.tar.gz"
    bin="$stage/samantha"
    libdir="$stage/lib"
    licensedir="$stage/licenses"

    rm -rf "$stage" "$tarball"
    mkdir -p "$libdir" "$licensedir"

    echo "Building native CGO amd64 binary…"
    CGO_ENABLED=1 GOOS=linux GOARCH=amd64 \
        go build -buildvcs=false -trimpath -o "$bin" ./cmd/samantha

    module_dir="$(
        go list -m -json github.com/k2-fsa/sherpa-onnx-go-linux |
            awk -F'"' '$2 == "Dir" { print $4; exit }'
    )"
    [[ -n "$module_dir" ]] || {
        echo "cannot locate github.com/k2-fsa/sherpa-onnx-go-linux module directory" >&2
        exit 1
    }
    native_dir="$module_dir/lib/x86_64-unknown-linux-gnu"
    for lib in libsherpa-onnx-c-api.so libonnxruntime.so; do
        [[ -f "$native_dir/$lib" ]] || {
            echo "required native library is missing: $native_dir/$lib" >&2
            exit 1
        }
        cp "$native_dir/$lib" "$libdir/$lib"
    done
    cp LICENSE "$stage/LICENSE"
    cp "$module_dir/LICENSE" "$licensedir/sherpa-onnx-go-linux-LICENSE"

    # sherpa-onnx-go-linux v1.12.29 bundles ONNX Runtime 1.23.2 without its
    # redistribution files. Pin and verify the upstream license material, and
    # fail loudly if a future module update changes the bundled runtime.
    onnxruntime_version="1.23.2"
    strings "$native_dir/libonnxruntime.so" | grep -Fx "$onnxruntime_version" >/dev/null || {
        echo "bundled ONNX Runtime version no longer matches license version $onnxruntime_version" >&2
        exit 1
    }
    onnxruntime_base="https://raw.githubusercontent.com/microsoft/onnxruntime/v${onnxruntime_version}"
    curl -fsSL "$onnxruntime_base/LICENSE" -o "$licensedir/onnxruntime-LICENSE"
    curl -fsSL "$onnxruntime_base/ThirdPartyNotices.txt" -o "$licensedir/onnxruntime-ThirdPartyNotices.txt"
    (
        cd "$licensedir"
        printf '%s  %s\n' \
            "2f07c72751aed99790b8a4869cf2311df85a860b22ded05fa22803587a48922c" \
            "onnxruntime-LICENSE" \
            "e9e90971a8e75a9a8ac0c6412e29c1202d079998389915aa485f46c816c3b4cc" \
            "onnxruntime-ThirdPartyNotices.txt" |
            sha256sum --check -
    )

    patchelf --set-rpath '$ORIGIN/lib' "$bin"
    runpath="$(patchelf --print-rpath "$bin")"
    [[ "$runpath" == '$ORIGIN/lib' ]] || {
        echo "unexpected Linux artifact RUNPATH: $runpath" >&2
        exit 1
    }
    if readelf -d "$bin" | grep -F "$module_dir"; then
        echo "Linux artifact still references the builder module cache" >&2
        exit 1
    fi
    if ldd "$bin" | grep -q 'not found'; then
        echo "Linux artifact has unresolved shared libraries:" >&2
        ldd "$bin" >&2
        exit 1
    fi

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
            sha="$(sha256sum "$stage/$rel" | cut -d' ' -f1)"
            (( first )) && first=0 || printf ',\n'
            printf '    {"path": "%s", "sha256": "%s"}' "$rel" "$sha"
        done
        printf '\n  ]\n}\n'
    } > "$stage/manifest.json"

    tar -czf "$tarball" -C dist "samantha-${platform}"

    smoke_root="$(mktemp -d)"
    trap 'rm -rf "$smoke_root"' EXIT
    tar -xzf "$tarball" -C "$smoke_root"
    smoke_bin="$smoke_root/samantha-${platform}/samantha"
    smoke_lib="$smoke_root/samantha-${platform}/lib"
    env -u LD_LIBRARY_PATH "$smoke_bin" --version
    smoke_ldd="$(env -u LD_LIBRARY_PATH ldd "$smoke_bin")"
    for lib in libsherpa-onnx-c-api.so libonnxruntime.so; do
        grep -F "$smoke_lib/$lib" <<<"$smoke_ldd" >/dev/null || {
            echo "relocation smoke test did not load bundled $lib:" >&2
            echo "$smoke_ldd" >&2
            exit 1
        }
    done

    echo
    echo "Artifact: $tarball"
    sha256sum "$tarball"
    echo "Contents:"
    tar -tzf "$tarball"

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
        go build -buildvcs=false -trimpath -o "$bin" ./cmd/samantha

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
