GOFILES=main.go mumble.go discord.go

mumble-discord-bridge: $(GOFILES)
	go build -o $@ $(GOFILES)

docker-latest:
	docker build .
	docker push stieneee/mumble-bridge-latest

clean:
	rm mumble-discord-bridge

.PHONY: all push clean