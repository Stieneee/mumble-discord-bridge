GOFILES=main.go mumble.go discord.go

mumble-discord-bridge: $(GOFILES)
	go build -o $@ $(GOFILES)