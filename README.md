# Mumble Discord Bridge

Mumble Discord Bridge is an open source Go application to bridge the audio between Mumble and Discord.

It was built with the hope that people can continue to use the voice application of their choice.

## Usage

Several configuration variables must be set for the binary to function correctly.
All variables can be set using flags or in the environment.
The binary will also attempt to load .env file located in the working directory.

```bash
Usage of mumble-discord-bridge:
  -discord-cid string
        DISCORD_CID, discord cid
  -discord-gid string
        DISCORD_GID, discord gid
  -discord-token string
        DISCORD_TOKEN, discord bot token
  -mumble-address string
        MUMBLE_ADDRESS, mumble server address, example example.com
  -mumble-password string
        MUMBLE_PASSWORD, mumble password, optional
  -mumble-port int
        MUMBLE_PORT mumble port (default 64738)
  -mumble-username string
        MUMBLE_USERNAME, mumble username (default "discord-bridge")
  -mumble-insecure bool
        MUMBLE_INSECURE, allow connection to insecure (invalid TLS cert) mumble server
```

## Setup

### Creating a Discord Bot

A Discord bot is required to authenticate this application with Discord.
The guide below provides information on how to setup a Discord bot.

[Create a Discord Bot](https://discordpy.readthedocs.io/en/latest/discord.html)

Individual Discord servers need to invite the bot before it can connect.

### Binary

Prebuilt binaries are available.

```bash
curl -s https://api.github.com/repos/stieneee/mumble-discord-bridge/releases/latest | grep "mumble-discord-bridge" | grep "browser_download_url" | cut -d '"' -f 4 | wget -qi -
```

### Docker

This project is built and distributed in a docker container.
A sample docker command can be copied from below.
This service command will always attempt to restart the service due to the `--restart=always` flag even when the server is restarted.
For testing purposes it may be best to remove the restart flag.

Replace the environment variables with variable for the desired mumble server, discord bot and discord server/channel.

```bash
# Sample for testing
docker docker run -e MUMBLE_ADDRESS=example.com -e MUMBLE_PASSWORD=optional -e DISCORD_TOKEN=TOKEN -e DISCORD_GID=GID -e DISCORD_CID=CID stieneee/mumble-discord-bridge

# Run as a service
docker docker run -e MUMBLE_ADDRESS=example.com -e MUMBLE_PASSWORD=optional -e DISCORD_TOKEN=TOKEN -e DISCORD_GID=GID -e DISCORD_CID=CID --restart=always --name=mumble-discord-bridge -d stieneee/mumble-discord-bridge

# Stop the service
docker stop mumble-discord-bridge && docker rm mumble-discord-bridge
```

### Mumbler Server Setting

To ensure compatibility please edit your murmur configuration file with the following

```bash
opusthreshold=0
```

This ensures all packets are opus encoded and should not cause any compatibility issues if your users are using up to date clients.

## Building From Source

This project requires Golang to build from source.
A simple go build command is all that is needed.
Ensure the opus library is installed.

```bash
go build -o mumble-discord-bridge *.go
```

## Known Issues

Currently there is an issue opening the discord voice channel.
It is a known issue with a dependency of this project.

Audio leveling from Discord needs to be improved.

## License

Distributed under the MIT License. See LICENSE for more information.

## Contributing

Issues and PRs are welcome and encouraged.
Please consider opening an issue to discuss features and ideas.

## Acknowledgement

The project would not have been possible without:

- [gumble](https://github.com/layeh/gumble)
- [discordgo](https://github.com/bwmarrin/discordgo)