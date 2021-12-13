# Mumble Discord Bridge

Mumble Discord Bridge is an open source Go application to bridge the audio between Mumble and Discord.

It was built with the hope that people can continue to use the voice application of their choice.

## Usage

Several configuration variables must be set for the binary to function correctly.
All variables can be set using flags or in the environment.
The binary will also attempt to load .env file located in the working directory.

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

Permission integer 36768768.

### Finding Discord CID and GID

Discord GID is a unique ID linked to one Discord Server, also called Guild. CID is similarly a unique ID for a Discord Channel. To find these you need to set Discord into developer Mode.

[Instructions to enable Discord Developer Mode](https://discordia.me/en/developer-mode)

Then you can get the GID by right-clicking your server and selecting Copy-ID. Similarly the CID can be found right clicking the voice channel and selecting Copy ID.

### Generating Mumble Client  (Optional)

Optionally you can specify a client certificate for mumble [Mumble Certificates](https://wiki.mumble.info/wiki/Mumble_Certificates)
If you don't have a client certificate, you can generate one with this command:

``` bash
openssl req -x509 -nodes -days 3650 -newkey rsa:2048 -keyout cert.pem -out cert.pem -subj "/CN=mumble-discord-bridge"
```

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

### OpenBSD Users

OpenBSD users should consider compiling a custom kernel to use 1000 ticks for the best possible performance.
See [issue 20](https://github.com/Stieneee/mumble-discord-bridge/issues/20) for the latest discussion about this topic.

## Jitter Buffer

The bridge implements simple jitter buffers that attempt to compensate for network, OS and hardware related jitter.
These jitter buffers are configurable in both directions.
A jitter buffer will slightly the delay the transmission of audio in order to have audio packets buffered for the next time step.
The Mumble client itself includes a jitter buffer for similar reasons.
A default jitter of 50ms should be adequate for most scenarios.
A warning will be logged if short burst or audio are seen.
A single warning can be ignored multiple warnings in short time spans would suggest the need for a larger jitter buffer.

## Monitoring the Bridge

The bridge can be started with a Prometheus metrics endpoint enabled.
The example folder contains the a docker-compose file that will spawn the bridge, Prometheus and Grafana configured to serve a single a pre-configured dashboard.

![Mumble Discord Bridge Grafana Dashboard](example/grafana-dashboard.png "Grafana Dashboard")

## Known Issues

Currently there is an issue opening the discord voice channel.
It is a known issue with a dependency of this project.

Audio leveling from Discord needs to be improved.

Delays in connecting to Mumble (such as from external authentication plugins) may result in extra error messages on initial connection.

There is an issue seen with Mumble-Server (murmur) 1.3.0 in which the bridge will loose the ability to send messages client after prolonged periods of connectivity.
This issue has been appears to be resolved by murmur 1.3.4.

## License

Distributed under the MIT License. See LICENSE for more information.

## Contributing

Issues and PRs are welcome and encouraged.
Please consider opening an issue to discuss features and ideas.

## Acknowledgement

The project would not have been possible without:

* [gumble](https://github.com/layeh/gumble)
* [discordgo](https://github.com/bwmarrin/discordgo)
