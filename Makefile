GOFILES=main.go mumble.go discord.go bridge.go config.go handlers.go

mumble-discord-bridge: $(GOFILES)
	goreleaser build --skip-validate --rm-dist

dev: $(GOFILES)
	goreleaser build --skip-validate --rm-dist && sudo ./dist/mumble-discord-bridge_linux_amd64/mumble-discord-bridge

dev-profile: $(GOFILES)
	goreleaser build --skip-validate --rm-dist && sudo ./dist/mumble-discord-bridge_linux_amd64/mumble-discord-bridge -cpuprofile cpu.prof

docker-latest:
	docker build -t stieneee/mumble-discord-bridge:latest .

docker-next:
	docker build -t stieneee/mumble-discord-bridge:next .
	docker push stieneee/mumble-discord-bridge:next

clean:
	rm -f mumble-discord-bridge

.PHONY: docker-latest docker-latest-push clean