package main

import (
	"flag"
	"log"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
	"layeh.com/gumble/gumble"
	_ "layeh.com/gumble/opus"
)

var BridgeConf *BridgeConfig
var Bridge *BridgeState

func main() {
	godotenv.Load()

	mumbleAddr := flag.String("mumble-address", lookupEnvOrString("MUMBLE_ADDRESS", ""), "MUMBLE_ADDRESS, mumble server address, example example.com")
	mumblePort := flag.Int("mumble-port", lookupEnvOrInt("MUMBLE_PORT", 64738), "MUMBLE_PORT mumble port")
	mumbleUsername := flag.String("mumble-username", lookupEnvOrString("MUMBLE_USERNAME", "discord-bridge"), "MUMBLE_USERNAME, mumble username")
	mumblePassword := flag.String("mumble-password", lookupEnvOrString("MUMBLE_PASSWORD", ""), "MUMBLE_PASSWORD, mumble password, optional")
	mumbleInsecure := flag.Bool("mumble-insecure", lookupEnvOrBool("MUMBLE_INSECURE", false), "mumble insecure,  env alt MUMBLE_INSECURE")

	discordToken := flag.String("discord-token", lookupEnvOrString("DISCORD_TOKEN", ""), "DISCORD_TOKEN, discord bot token")
	discordGID := flag.String("discord-gid", lookupEnvOrString("DISCORD_GID", ""), "DISCORD_GID, discord gid")
	discordCID := flag.String("discord-cid", lookupEnvOrString("DISCORD_CID", ""), "DISCORD_CID, discord cid")
	discordCommand := flag.String("discord-command", lookupEnvOrString("DISCORD_COMMAND", "mumble-discord"), "Discord command string, env alt DISCORD_COMMAND, optional, defaults to mumble-discord")
	autoMode := flag.Bool("auto", lookupEnvOrBool("AUTO_MODE", false), "bridge starts in auto mode")
	flag.Parse()
	log.Printf("app.config %v\n", getConfig(flag.CommandLine))

	if *mumbleAddr == "" {
		log.Fatalln("missing mumble address")
	}
	if *mumbleUsername == "" {
		log.Fatalln("missing mumble username")
	}

	if *discordToken == "" {
		log.Fatalln("missing discord bot token")
	}
	if *discordGID == "" {
		log.Fatalln("missing discord gid")
	}
	if *discordCID == "" {
		log.Fatalln("missing discord cid")
	}

	// DISCORD Setup

	discord, err := discordgo.New("Bot " + *discordToken)
	if err != nil {
		log.Println(err)
		return
	}

	// Open Websocket
	discord.LogLevel = 2
	discord.StateEnabled = true
	discord.Identify.Intents = discordgo.MakeIntent(discordgo.IntentsAllWithoutPrivileged)
	// register handlers
	discord.AddHandler(ready)
	discord.AddHandler(messageCreate)
	discord.AddHandler(guildCreate)
	discord.AddHandler(voiceUpdate)
	err = discord.Open()
	if err != nil {
		log.Println(err)
		return
	}
	defer discord.Close()

	log.Println("Discord Bot Connected")
	log.Printf("Discord bot looking for command !%v", *discordCommand)
	config := gumble.NewConfig()
	config.Username = *mumbleUsername
	config.Password = *mumblePassword
	config.AudioInterval = time.Millisecond * 10

	BridgeConf = &BridgeConfig{
		Config:         config,
		MumbleAddr:     *mumbleAddr + ":" + strconv.Itoa(*mumblePort),
		MumbleInsecure: *mumbleInsecure,
		Auto:           *autoMode,
		Command:        *discordCommand,
		GID:            *discordGID,
		CID:            *discordCID,
	}
	Bridge = &BridgeState{
		ActiveConn:       make(chan bool),
		Connected:        false,
		MumbleUserCount:  0,
		DiscordUserCount: 0,
	}
	userCount := make(chan int)
	go pingMumble(*mumbleAddr, strconv.Itoa(*mumblePort), userCount)
	go discordStatusUpdate(discord, userCount)
	if *autoMode {
		Bridge.AutoChan = make(chan bool)
		go AutoBridge(discord)
	}
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt, os.Kill)
	<-sc
	log.Println("Bot shutting down")
}
