# Install

[Back to README](../README.md)

## Requirements

- Go 1.26+
- [just](https://github.com/casey/just)
- A C compiler for source builds (`gcc` or `clang`; Samantha uses CGO)
- A working microphone and speaker for voice mode
- Claude CLI on `PATH` when `brain_provider=claude`
- Ollama running locally when `brain_provider=ollama`
- Docker or a compatible container runtime for integration tests

macOS users may need to grant microphone permission to the terminal app used to run Samantha.

## Linux amd64 archive

Tagged releases include a relocatable `samantha-linux-amd64.tar.gz` for
x86_64 glibc Linux ([latest release](https://github.com/lancekrogers/samantha/releases/latest)).
It contains the Samantha executable and its sherpa-onnx /
onnxruntime shared libraries; keep the executable and `lib/` directory
together.

```bash
tar -xzf samantha-linux-amd64.tar.gz
mkdir -p ~/.local/opt ~/.local/bin
mv samantha-linux-amd64 ~/.local/opt/samantha
ln -s ~/.local/opt/samantha/samantha ~/.local/bin/samantha
samantha --version
```

Ensure `~/.local/bin` is on `PATH`. Models are downloaded separately on first
setup:

```bash
samantha models ensure
samantha doctor --voice-devices
```

The release archive targets glibc Linux with Ubuntu 24.04 as its build and
compatibility floor. Source builds are also validated on Arch/EndeavourOS;
musl systems such as Alpine require a separate native build strategy.

## Homebrew (macOS)

```bash
brew install --HEAD lancekrogers/tap/samantha
```

Builds from source and bundles the sherpa-onnx/onnxruntime native libraries so
the binary is self-contained. `--HEAD` tracks the latest `main`; once a version
is tagged it installs without it. Grant your terminal microphone access under
System Settings → Privacy & Security → Microphone.

## From source

```bash
just install    # Build, sign on macOS when possible, and install to $GOBIN
```

For development builds:

```bash
just build
just run -- --text
```

On Linux, `just install` is a source/developer install and retains the native
libraries supplied through the Go module cache. Use the release archive when
you need a relocatable installation that remains valid after cleaning that
cache.

See [Development](development.md) for test, lint, and model-asset commands.
