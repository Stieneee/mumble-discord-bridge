GOFILES=$(shell find ./ -type f -name '*.go')

mumble-discord-bridge: $(GOFILES) .goreleaser.yml
	goreleaser build --skip-validate --rm-dist

dev: $(GOFILES) .goreleaser.yml
	goreleaser build --skip-validate --rm-dist && sudo ./dist/mumble-discord-bridge_linux_amd64/mumble-discord-bridge

dev-race: $(GOFILES) .goreleaser.yml
	go run -race ./cmd/mumble-discord-bridge

dev-profile: $(GOFILES) .goreleaser.yml
	goreleaser build --skip-validate --rm-dist && sudo ./dist/mumble-discord-bridge_linux_amd64/mumble-discord-bridge -cpuprofile cpu.prof

test-chart: SHELL:=/bin/bash 
test-chart:
	go test ./test &
	until pidof test.test; do continue; done;
	psrecord --plot docs/test-cpu-memory.png $$(pidof mumble-discord-bridge.test)

docker-latest:
	docker build -t stieneee/mumble-discord-bridge:latest .

docker-latest-run:
	docker run --env-file .env -it stieneee/mumble-discord-bridge:latest

docker-next:
	docker build -t stieneee/mumble-discord-bridge:next .
	docker push stieneee/mumble-discord-bridge:next

clean:
	rm -f mumble-discord-bridge

.PHONY: docker-latest docker-latest-push clean