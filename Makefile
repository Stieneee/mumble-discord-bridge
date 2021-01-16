GOFILES=main.go mumble.go discord.go

mumble-discord-bridge: $(GOFILES)
	goreleaser build --skip-validate --rm-dist

dev: $(GOFILES)
	goreleaser build --skip-validate --rm-dist && sudo ./dist/mumble-discord-bridge_linux_amd64/mumble-discord-bridge

dev-profile: $(GOFILES)
	goreleaser build --skip-validate --rm-dist && sudo ./dist/mumble-discord-bridge_linux_amd64/mumble-discord-bridge -cpuprofile cpu.prof

docker-latest:
	docker build -t stieneee/mumble-discord-bridge:latest .
	docker push stieneee/mumble-discord-bridge:latest

clean:
	rm -f mumble-discord-bridge

.PHONY: all push clean