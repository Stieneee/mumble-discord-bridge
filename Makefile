GOFILES=$(shell find ./ -type f -name '*.go')

mumble-discord-bridge: $(GOFILES)
	goreleaser build --skip-validate --rm-dist

dev: $(GOFILES)
	goreleaser build --skip-validate --rm-dist && sudo ./dist/mumble-discord-bridge_linux_amd64/mumble-discord-bridge

dev-race: $(GOFILES)
	go run -race $(GOFILES)

dev-profile: $(GOFILES)
	goreleaser build --skip-validate --rm-dist && sudo ./dist/mumble-discord-bridge_linux_amd64/mumble-discord-bridge -cpuprofile cpu.prof

test-chart: SHELL:=/bin/bash 
test-chart:
	go test &
	until pidof mumble-discord-bridge.test; do continue; done;
	psrecord --plot docs/test-cpu-memory.png $$(pidof mumble-discord-bridge.test)

docker-latest:
	docker build -t stieneee/mumble-discord-bridge:latest .

docker-next:
	docker build -t stieneee/mumble-discord-bridge:next .
	docker push stieneee/mumble-discord-bridge:next

clean:
	rm -f mumble-discord-bridge

.PHONY: docker-latest docker-latest-push clean