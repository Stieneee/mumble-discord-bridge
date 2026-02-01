GOFILES=$(shell find ./ -type f -name '*.go')
VERSION=$(shell git describe --tags --always --dirty)
COMMIT=$(shell git rev-parse --short HEAD)
DATE=$(shell date -u +%Y-%m-%dT%H:%M:%SZ)
LDFLAGS=-s -w -X main.version=$(VERSION) -X main.commit=$(COMMIT) -X main.date=$(DATE)

# Build binary
mumble-discord-bridge: $(GOFILES)
	CGO_ENABLED=1 go build -tags=netgo -ldflags="$(LDFLAGS)" -o mumble-discord-bridge ./cmd/mumble-discord-bridge

# Build and run for development
dev: $(GOFILES)
	CGO_ENABLED=1 go build -tags=netgo -ldflags="$(LDFLAGS)" -o mumble-discord-bridge ./cmd/mumble-discord-bridge && ./mumble-discord-bridge

# Run with race detector (development)
dev-race: $(GOFILES)
	go run -race ./cmd/mumble-discord-bridge

# Build with profiling support
dev-profile: $(GOFILES)
	CGO_ENABLED=1 go build -tags=netgo -ldflags="$(LDFLAGS)" -o mumble-discord-bridge ./cmd/mumble-discord-bridge && ./mumble-discord-bridge -cpuprofile cpu.prof

# Generate licenses
licenses:
	go install github.com/google/go-licenses@latest
	go-licenses save ./cmd/mumble-discord-bridge --force --save_path="./LICENSES"

# Run linter
lint:
	golangci-lint run

# Format code
format:
	go fmt ./...

# Clean build artifacts
clean:
	rm -f mumble-discord-bridge
	rm -rf dist
	rm -rf LICENSES.zip LICENSES

.PHONY: mumble-discord-bridge dev dev-profile dev-race licenses clean lint format
