# Development utilities (imported flat into root)

# Lint and format
lint:
    go fmt ./...
    go vet ./...

# Update and tidy dependencies
deps:
    go get -u ./...
    go mod tidy

# Watch for changes and rerun (requires entr)
watch:
    find . -name '*.go' | entr -r go run ./cmd/samantha
