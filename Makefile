.PHONY: build test fmt vet lint version version-next version-bump release install-tools hooks

# Build
build:
	go build -o truespec ./cmd/truespec

# Test
test:
	go test ./...

# Lint & format
fmt:
	gofmt -w .

vet:
	go vet ./...

lint: fmt vet

# Version management
version:
	@git describe --tags --abbrev=0 2>/dev/null || echo "v0.0.0 (no tags)"

version-next:
	go-semver-release release . --dry-run

version-bump:
	go-semver-release release .

release: version-bump
	@tag=$$(git describe --tags --abbrev=0) && \
	echo "Tagged $$tag — push with: git push origin $$tag"

# Developer setup
install-tools:
	go install github.com/evilmartians/lefthook/v2@latest
	go install github.com/s0ders/go-semver-release/v8@latest

hooks:
	lefthook install
