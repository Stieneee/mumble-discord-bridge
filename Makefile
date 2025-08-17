GOFILES=$(shell find ./ -type f -name '*.go')
LATEST_TAG=$(shell git describe --tags `git rev-list --tags --max-count=1`)

mumble-discord-bridge: $(GOFILES)
	goreleaser build --clean --snapshot

release: $(GOFILES)
	goreleaser release --clean

dev: $(GOFILES)
	goreleaser build --clean --single-target --snapshot && ./dist/mumble-discord-bridge_linux_amd64_v1/mumble-discord-bridge

dev-race: $(GOFILES)
	go run -race ./cmd/mumble-discord-bridge

dev-profile: $(GOFILES)
	goreleaser build --skip=validate --clean --single-target --snapshot && ./dist/mumble-discord-bridge_linux_amd64_v1/mumble-discord-bridge -cpuprofile cpu.prof

test-chart: SHELL:=/bin/bash 
test-chart:
	go test ./test &
	until pidof test.test; do continue; done;
	psrecord --plot docs/test-cpu-memory.png $$(pidof mumble-discord-bridge.test)

lint:
	golangci-lint run

clean:
	rm -rf dist
	rm -rf LICENSES.zip LICENSES

.PHONY: mumble-discord-bridge release dev dev-profile dev-race test-chart clean
