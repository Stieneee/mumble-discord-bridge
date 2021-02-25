# Mumble Discord Bridge

Mumble Discord Bridge is an open source Go application to bridge the audio between Mumble and Discord.

It was built with the hope that people can continue to use the voice application of their choice.

## Usage

Several configuration variables must be set for the binary to function correctly.
All variables can be set using flags or in the environment.
The binary will also attempt to load .env file located in the working directory.

```bash
Usage of ./mumble-discord-bridge:
  -discord-cid string
    	DISCORD_CID, discord cid, required
  -discord-command string
    	DISCORD_COMMAND, Discord command string, env alt DISCORD_COMMAND, optional, (defaults mumble-discord) (default "mumble-discord")
  -discord-disable-text
    	DISCORD_DISABLE_TEXT, disable sending direct messages to discord, (default false)
  -discord-gid string
    	DISCORD_GID, discord gid, required
  -discord-token string
    	DISCORD_TOKEN, discord bot token, required
  -mode string
    	MODE, [constant, manual, auto] determine which mode the bridge starts in, (default constant) (default "constant")
  -mumble-address string
    	MUMBLE_ADDRESS, mumble server address, example example.com, required
  -mumble-channel string
    	MUMBLE_CHANNEL, mumble channel to start in, using '/' to seperate nested channels, optional
  -mumble-disable-text
    	MUMBLE_DISABLE_TEXT, disable sending text to mumble, (default false)
  -mumble-insecure
    	 MUMBLE_INSECURE, mumble insecure, optional
  -mumble-certificate
       MUMBLE_CERTIFICATE, mumble client certificate, optional
  -mumble-password string
    	MUMBLE_PASSWORD, mumble password, optional
  -mumble-port int
    	MUMBLE_PORT, mumble port, (default 64738) (default 64738)
  -mumble-username string
    	MUMBLE_USERNAME, mumble username, (default: discord) (default "Discord")
  -nice
    	NICE, whether the bridge should automatically try to 'nice' itself, (default false)
  -debug
        DEBUG_LEVEL,  DISCORD debug level, optional (default: 1)
```

The bridge can be run with the follow modes:
```bash
   auto
       The bridge starts up but does not connect immediately. It can be either manually linked (see below) or will join the voice channels when there's at least one person on each side.
       The bridge will leave both voice channels once there is no one on either end
   manual
       The bridge starts up but does not connect immediately. It will join the voice channels when issued the link command and will leave with the unlink command
   constant
       The bridge starts up and immediately connects to both Discord and Mumble voice channels. It can not be controlled in this mode and quits when the program is stopped
```

In "auto" or "manual" modes, the bridge can be controlled in Discord with the following commands:

```bash
!DISCORD_COMMAND link
	Commands the bridge to join the Discord channel the user is in and the Mumble server
!DISCORD_COMMAND unlink
	Commands the bridge to leave the Discord channel the user is in and the Mumble server
!DISCORD_COMMAND refresh
	Commands the bridge to unlink, then link again.
!DISCORD_COMMAND auto
	Toggle between manual and auto mode
```
## Setup

### Creating a Discord Bot

A Discord bot is required to authenticate this application with Discord.
The guide below provides information on how to setup a Discord bot.

[Create a Discord Bot](https://discordpy.readthedocs.io/en/latest/discord.html)

Individual Discord servers need to invite the bot before it can connect.  
The bot requires the following permissions:
* View Channels
* See Messages
* Read Message History
* Voice Channel Connect
* Voice Channel Speak
* Voice Channel Use Voice Activity

### Finding Discord CID and GID

Discord GID is a unique ID linked to one Discord Server, also called Guild. CID is similarly a unique ID for a Discord Channel. To find these you need to set Discord into developer Mode.

[Instructions to enable Discord Developer Mode](https://discordia.me/en/developer-mode)

Then you can get the GID by right-clicking your server and selecting Copy-ID. Similarly the CID can be found right clicking the voice channel and selecting Copy ID.

### Generating Client Certificate

If you don't have a client certificate, you can generate one with this command:

    openssl req -x509 -nodes -days 3650 -newkey rsa:2048 -keyout cert.pem -out cert.pem -subj "/CN=mumble-discord-bridge"

### Binary

Prebuilt binaries are available.
The binaries require the opus code runtime library to be installed.

```bash
# Ubuntu
sudo apt install libopus0
```

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
#or
make mumble-discord-bridge
```

## Known Issues

Currently there is an issue opening the discord voice channel.
It is a known issue with a dependency of this project.

Audio leveling from Discord needs to be improved.

Delays in connecting to Mumble (such as from external authentication plugins) may result in extra error messages on initial connection.

## License

Distributed under the MIT License. See LICENSE for more information.

## Contributing

Issues and PRs are welcome and encouraged.
Please consider opening an issue to discuss features and ideas.

## Acknowledgement

The project would not have been possible without:

- [gumble](https://github.com/layeh/gumble)
- [discordgo](https://github.com/bwmarrin/discordgo)
