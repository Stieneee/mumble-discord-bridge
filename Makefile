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

# Packet loss testing (requires sudo, uses lo interface by default)
# Usage: make test-packet-loss-start IFACE=eth0 LOSS=5
IFACE ?= lo
LOSS ?= 5

test-packet-loss-start:
	@echo "Enabling $(LOSS)% packet loss on interface $(IFACE)..."
	sudo tc qdisc add dev $(IFACE) root netem loss $(LOSS)%
	@echo "Packet loss enabled. Run 'make test-packet-loss-stop' to disable."

test-packet-loss-stop:
	@echo "Disabling packet loss on interface $(IFACE)..."
	sudo tc qdisc del dev $(IFACE) root netem 2>/dev/null || true
	@echo "Packet loss disabled."

test-packet-loss-status:
	@echo "Current tc qdisc status:"
	tc qdisc show dev $(IFACE)

.PHONY: mumble-discord-bridge dev dev-profile dev-race licenses clean lint format test-packet-loss-start test-packet-loss-stop test-packet-loss-status
