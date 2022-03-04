GOFILES=$(shell find ./ -type f -name '*.go')
LATEST_TAG=$(shell git describe --tags `git rev-list --tags --max-count=1`)

mumble-discord-bridge: $(GOFILES) .goreleaser.yml
	goreleaser build --rm-dist --snapshot

release: 
	rm -rf LICENSES.zip LICENSES
	go-licenses save ./cmd/mumble-discord-bridge --save_path="./LICENSES"
	zip -r -9 LICENSES.zip ./LICENSES
	goreleaser release --rm-dist

dev: $(GOFILES) .goreleaser.yml
	goreleaser build --skip-validate --rm-dist --single-target --snapshot && sudo ./dist/mumble-discord-bridge_linux_amd64/mumble-discord-bridge

dev-race: $(GOFILES) .goreleaser.yml
	go run -race ./cmd/mumble-discord-bridge

dev-profile: $(GOFILES) .goreleaser.yml
	goreleaser build --skip-validate --rm-dist --single-target --snapshot && sudo ./dist/mumble-discord-bridge_linux_amd64/mumble-discord-bridge -cpuprofile cpu.prof

test-chart: SHELL:=/bin/bash 
test-chart:
	go test ./test &
	until pidof test.test; do continue; done;
	psrecord --plot docs/test-cpu-memory.png $$(pidof mumble-discord-bridge.test)

docker-latest:
	docker build -t stieneee/mumble-discord-bridge:latest -t stieneee/mumble-discord-bridge:$(LATEST_TAG) -t ghcr.io/stieneee/mumble-discord-bridge:latest -t ghcr.io/stieneee/mumble-discord-bridge:$(LATEST_TAG) .

docker-latest-run:
	docker run --env-file .env -it stieneee/mumble-discord-bridge:latest

docker-release: 
	docker push stieneee/mumble-discord-bridge:latest
	docker push stieneee/mumble-discord-bridge:$(LATEST_TAG)
	docker push ghcr.io/stieneee/mumble-discord-bridge:latest
	docker push ghcr.io/stieneee/mumble-discord-bridge:$(LATEST_TAG)

docker-next:
	docker build -t stieneee/mumble-discord-bridge:next -t ghcr.io/stieneee/mumble-discord-bridge:next .
	docker push stieneee/mumble-discord-bridge:next
	docker push ghcr.io/stieneee/mumble-discord-bridge:next

clean:
	rm -rf dist
	rm -rf LICENSES.zip LICENSES

.PHONY: release dev dev-profile dev-race test-chart docker-latest docker-latest-release docker-release docker-next clean