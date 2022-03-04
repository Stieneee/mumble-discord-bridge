package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"runtime/pprof"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/joho/godotenv"
	"github.com/stieneee/gumble/gumble"
	"github.com/stieneee/gumble/gumbleutil"
	"github.com/stieneee/mumble-discord-bridge/internal/bridge"
)

var (
	// Build vars
	version string
	commit  string
	date    string
)

func main() {
	var err error

	fmt.Println("Mumble-Discord-Bridge")
	fmt.Println(version + " " + commit + " " + date)

	godotenv.Load()

	mumbleAddr := flag.String("mumble-address", lookupEnvOrString("MUMBLE_ADDRESS", ""), "MUMBLE_ADDRESS, mumble server address, example example.com, required")
	mumblePort := flag.Int("mumble-port", lookupEnvOrInt("MUMBLE_PORT", 64738), "MUMBLE_PORT, mumble port, (default 64738)")
	mumbleUsername := flag.String("mumble-username", lookupEnvOrString("MUMBLE_USERNAME", "Discord"), "MUMBLE_USERNAME, mumble username, (default: discord)")
	mumblePassword := flag.String("mumble-password", lookupEnvOrString("MUMBLE_PASSWORD", ""), "MUMBLE_PASSWORD, mumble password, optional")
	mumbleInsecure := flag.Bool("mumble-insecure", lookupEnvOrBool("MUMBLE_INSECURE", false), " MUMBLE_INSECURE, mumble insecure, optional")
	mumbleCertificate := flag.String("mumble-certificate", lookupEnvOrString("MUMBLE_CERTIFICATE", ""), "MUMBLE_CERTIFICATE, client certificate to use when connecting to the Mumble server")
	mumbleChannel := flag.String("mumble-channel", lookupEnvOrString("MUMBLE_CHANNEL", ""), "MUMBLE_CHANNEL, mumble channel to start in, using '/' to separate nested channels, optional")
	mumbleSendBuffer := flag.Int("to-mumble-buffer", lookupEnvOrInt("TO_MUMBLE_BUFFER", 50), "TO_MUMBLE_BUFFER, Jitter buffer from Discord to Mumble to absorb timing issues related to network, OS and hardware quality. (Increments of 10ms)")
	mumbleDisableText := flag.Bool("mumble-disable-text", lookupEnvOrBool("MUMBLE_DISABLE_TEXT", false), "MUMBLE_DISABLE_TEXT, disable sending text to mumble, (default false)")
	discordToken := flag.String("discord-token", lookupEnvOrString("DISCORD_TOKEN", ""), "DISCORD_TOKEN, discord bot token, required")
	discordGID := flag.String("discord-gid", lookupEnvOrString("DISCORD_GID", ""), "DISCORD_GID, discord gid, required")
	discordCID := flag.String("discord-cid", lookupEnvOrString("DISCORD_CID", ""), "DISCORD_CID, discord cid, required")
	discordSendBuffer := flag.Int("to-discord-buffer", lookupEnvOrInt("TO_DISCORD_BUFFER", 50), "TO_DISCORD_BUFFER, Jitter buffer from Mumble to Discord to absorb timing issues related to network, OS and hardware quality. (Increments of 10ms)")
	discordCommand := flag.String("discord-command", lookupEnvOrString("DISCORD_COMMAND", "mumble-discord"), "DISCORD_COMMAND, Discord command string, env alt DISCORD_COMMAND, optional, (defaults mumble-discord)")
	discordDisableText := flag.Bool("discord-disable-text", lookupEnvOrBool("DISCORD_DISABLE_TEXT", false), "DISCORD_DISABLE_TEXT, disable sending direct messages to discord, (default false)")
	discordDisableBotStatus := flag.Bool("discord-disable-bot-status", lookupEnvOrBool("DISCORD_DISABLE_BOT_STATUS", false), "DISCORD_DISABLE_BOT_STATUS, disable updating bot status, (default false)")
	mode := flag.String("mode", lookupEnvOrString("MODE", "constant"), "MODE, [constant, manual, auto] determine which mode the bridge starts in, (default constant)")
	nice := flag.Bool("nice", lookupEnvOrBool("NICE", false), "NICE, whether the bridge should automatically try to 'nice' itself, (default false)")
	debug := flag.Int("debug-level", lookupEnvOrInt("DEBUG", 1), "DEBUG_LEVEL, Discord debug level, optional, (default 1)")
	promEnable := flag.Bool("prometheus-enable", lookupEnvOrBool("PROMETHEUS_ENABLE", false), "PROMETHEUS_ENABLE, Enable prometheus metrics")
	promPort := flag.Int("prometheus-port", lookupEnvOrInt("PROMETHEUS_PORT", 9559), "PROMETHEUS_PORT, Prometheus metrics port, optional, (default 9559)")

	cpuprofile := flag.String("cpuprofile", "", "write cpu profile to `file`")

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
	if *mode == "" {
		log.Fatalln("missing mode set")
	}
	if *nice {
		err := syscall.Setpriority(syscall.PRIO_PROCESS, os.Getpid(), -5)
		if err != nil {
			log.Println("Unable to set priority. ", err)
		}
	}

	if *promEnable {
		go bridge.StartPromServer(*promPort)
	}

	// Optional CPU Profiling
	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal("could not create CPU profile: ", err)
		}
		defer f.Close() // error handling omitted for example
		if err := pprof.StartCPUProfile(f); err != nil {
			log.Fatal("could not start CPU profile: ", err)
		}
		defer pprof.StopCPUProfile()
	}

	// Buffer Math
	if *discordSendBuffer < 10 {
		*discordSendBuffer = 10
	}

	if *mumbleSendBuffer < 10 {
		*mumbleSendBuffer = 10
	}

	var discordStartStreamingCount int = int(math.Round(float64(*discordSendBuffer) / 10.0))
	log.Println("To Discord Jitter Buffer: ", discordStartStreamingCount*10, " ms")

	var mumbleStartStreamCount int = int(math.Round(float64(*mumbleSendBuffer) / 10.0))
	log.Println("To Mumble Jitter Buffer: ", mumbleStartStreamCount*10, " ms")

	// BRIDGE SETUP

	Bridge := &bridge.BridgeState{
		BridgeConfig: &bridge.BridgeConfig{
			// MumbleConfig:   config,
			MumbleAddr:                 *mumbleAddr + ":" + strconv.Itoa(*mumblePort),
			MumbleInsecure:             *mumbleInsecure,
			MumbleCertificate:          *mumbleCertificate,
			MumbleChannel:              strings.Split(*mumbleChannel, "/"),
			MumbleStartStreamCount:     mumbleStartStreamCount,
			MumbleDisableText:          *mumbleDisableText,
			Command:                    *discordCommand,
			GID:                        *discordGID,
			CID:                        *discordCID,
			DiscordStartStreamingCount: discordStartStreamingCount,
			DiscordDisableText:         *discordDisableText,
			DiscordDisableBotStatus:    *discordDisableBotStatus,
			Version:                    version,
		},
		Connected:    false,
		DiscordUsers: make(map[string]bridge.DiscordUser),
		MumbleUsers:  make(map[string]bool),
	}

	bridge.PromApplicationStartTime.SetToCurrentTime()

	// MUMBLE SETUP
	Bridge.BridgeConfig.MumbleConfig = gumble.NewConfig()
	Bridge.BridgeConfig.MumbleConfig.Username = *mumbleUsername
	Bridge.BridgeConfig.MumbleConfig.Password = *mumblePassword
	Bridge.BridgeConfig.MumbleConfig.AudioInterval = time.Millisecond * 10

	Bridge.MumbleListener = &bridge.MumbleListener{
		Bridge: Bridge,
	}

	Bridge.BridgeConfig.MumbleConfig.Attach(gumbleutil.Listener{
		Connect:    Bridge.MumbleListener.MumbleConnect,
		UserChange: Bridge.MumbleListener.MumbleUserChange,
		// ChannelChange: Bridge.MumbleListener.MumbleChannelChange,
	})

	// DISCORD SETUP

	//Connect to discord
	Bridge.DiscordSession, err = discordgo.New("Bot " + *discordToken)
	if err != nil {
		log.Println(err)
		return
	}

	Bridge.DiscordSession.LogLevel = *debug
	Bridge.DiscordSession.StateEnabled = true
	Bridge.DiscordSession.Identify.Intents = discordgo.MakeIntent(discordgo.IntentsAllWithoutPrivileged)
	Bridge.DiscordSession.ShouldReconnectOnError = true
	// register handlers
	Bridge.DiscordListener = &bridge.DiscordListener{
		Bridge: Bridge,
	}
	Bridge.DiscordSession.AddHandler(Bridge.DiscordListener.MessageCreate)
	Bridge.DiscordSession.AddHandler(Bridge.DiscordListener.GuildCreate)
	Bridge.DiscordSession.AddHandler(Bridge.DiscordListener.VoiceUpdate)

	// Open Discord websocket
	err = Bridge.DiscordSession.Open()
	if err != nil {
		log.Println(err)
		return
	}
	defer Bridge.DiscordSession.Close()

	log.Println("Discord Bot Connected")
	log.Printf("Discord bot looking for command !%v", *discordCommand)

	switch *mode {
	case "auto":
		log.Println("bridge starting in automatic mode")
		Bridge.AutoChanDie = make(chan bool)
		Bridge.Mode = bridge.BridgeModeAuto
		Bridge.DiscordChannelID = Bridge.BridgeConfig.CID
		go Bridge.AutoBridge()
	case "manual":
		log.Println("bridge starting in manual mode")
		Bridge.Mode = bridge.BridgeModeManual
	case "constant":
		log.Println("bridge starting in constant mode")
		Bridge.Mode = bridge.BridgeModeConstant
		Bridge.DiscordChannelID = Bridge.BridgeConfig.CID
		go func() {
			for {
				Bridge.StartBridge()
				log.Println("Bridge died")
				time.Sleep(5 * time.Second)
				log.Println("Restarting")
			}
		}()
	default:
		Bridge.DiscordSession.Close()
		log.Fatalln("invalid bridge mode set")
	}

	go Bridge.DiscordStatusUpdate()

	// Shutdown on OS signal
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	log.Println("OS Signal. Bot shutting down")

	time.AfterFunc(30*time.Second, func() {
		os.Exit(99)
	})

	// Wait or the bridge to exit cleanly
	Bridge.BridgeMutex.Lock()
	if Bridge.Connected {
		//TODO BridgeDie occasionally panics on send to closed channel
		Bridge.BridgeDie <- true
		Bridge.WaitExit.Wait()
	}
	Bridge.BridgeMutex.Unlock()
}
