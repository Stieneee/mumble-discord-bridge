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
	mumblePort := flag.Int("mumble-port", lookupEnvOrInt("MUMBLE_PORT", 64738), "MUMBLE_PORT, mumble port")
	mumbleUsername := flag.String("mumble-username", lookupEnvOrString("MUMBLE_USERNAME", "Discord"), "MUMBLE_USERNAME, mumble username")
	mumblePassword := flag.String("mumble-password", lookupEnvOrString("MUMBLE_PASSWORD", ""), "MUMBLE_PASSWORD, mumble password, optional")
	mumbleInsecure := flag.Bool("mumble-insecure", lookupEnvOrBool("MUMBLE_INSECURE", false), " MUMBLE_INSECURE, mumble insecure, flag")
	mumbleCertificate := flag.String("mumble-certificate", lookupEnvOrString("MUMBLE_CERTIFICATE", ""), "MUMBLE_CERTIFICATE, client certificate to use when connecting to the Mumble server")
	mumbleChannel := flag.String("mumble-channel", lookupEnvOrString("MUMBLE_CHANNEL", ""), "MUMBLE_CHANNEL, mumble channel to start in, using '/' to separate nested channels, optional")
	mumbleSendBuffer := flag.Int("to-mumble-buffer", lookupEnvOrInt("TO_MUMBLE_BUFFER", 50), "TO_MUMBLE_BUFFER, Jitter buffer from Discord to Mumble to absorb timing issues related to network, OS and hardware quality, increments of 10ms")
	mumbleDisableText := flag.Bool("mumble-disable-text", lookupEnvOrBool("MUMBLE_DISABLE_TEXT", false), "MUMBLE_DISABLE_TEXT, disable sending text to mumble")
	discordToken := flag.String("discord-token", lookupEnvOrString("DISCORD_TOKEN", ""), "DISCORD_TOKEN, discord bot token, required")
	discordGID := flag.String("discord-gid", lookupEnvOrString("DISCORD_GID", ""), "DISCORD_GID, discord gid, required")
	discordCID := flag.String("discord-cid", lookupEnvOrString("DISCORD_CID", ""), "DISCORD_CID, discord cid, required")
	discordSendBuffer := flag.Int("to-discord-buffer", lookupEnvOrInt("TO_DISCORD_BUFFER", 50), "TO_DISCORD_BUFFER, Jitter buffer from Mumble to Discord to absorb timing issues related to network, OS and hardware quality, increments of 10ms")
	discordTextMode := flag.String("discord-text-mode", lookupEnvOrString("DISCORD_TEXT_MODE", "channel"), "DISCORD_TEXT_MODE, [channel, user, disabled] determine where discord text messages are sent")
	discordDisableBotStatus := flag.Bool("discord-disable-bot-status", lookupEnvOrBool("DISCORD_DISABLE_BOT_STATUS", false), "DISCORD_DISABLE_BOT_STATUS, disable updating bot status")
	chatBridge := flag.Bool("chat-bridge", lookupEnvOrBool("CHAT_BRIDGE", false), "CHAT_BRIDGE, enable chat bridge")
	command := flag.String("command", lookupEnvOrString("COMMAND", "mumble-discord"), "COMMAND, command phrase '!mumble-discord help' to control the bridge via text channels")
	commandMode := flag.String("command-mode", lookupEnvOrString("COMMAND_MODE", "both"), "COMMAND_MODE, [both, mumble, discord, none] determine which side of the bridge will respond to commands")
	mode := flag.String("mode", lookupEnvOrString("MODE", "constant"), "MODE, [constant, manual, auto] determine which mode the bridge starts in")
	nice := flag.Bool("nice", lookupEnvOrBool("NICE", false), "NICE, whether the bridge should automatically try to 'nice' itself")
	debug := flag.Int("debug-level", lookupEnvOrInt("DEBUG", 1), "DEBUG_LEVEL, Discord debug level, optional")
	promEnable := flag.Bool("prometheus-enable", lookupEnvOrBool("PROMETHEUS_ENABLE", false), "PROMETHEUS_ENABLE, Enable prometheus metrics")
	promPort := flag.Int("prometheus-port", lookupEnvOrInt("PROMETHEUS_PORT", 9559), "PROMETHEUS_PORT, Prometheus metrics port, optional")

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
	if *discordTextMode != "channel" && *discordTextMode != "user" && *discordTextMode != "disabled" {
		log.Fatalln("invalid discord text mode set")
	}
	if *commandMode != "both" && *commandMode != "mumble" && *commandMode != "discord" && *commandMode != "none" {
		log.Fatalln("invalid command mode set")
	}
	if *commandMode != "none" {
		if *command == "" {
			log.Fatalln("missing command")
		}
	}
	if *mode != "constant" && *mode != "manual" && *mode != "auto" {
		log.Fatalln("invalid bridge mode set")
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

	// check if chat bridge is enabled
	if !*mumbleDisableText && *discordTextMode == "channel" && *chatBridge {
		log.Println("chat bridge is enabled")
	} else {
		*chatBridge = false
		log.Println("chat bridge is disabled")
	}

	var discordStartStreamingCount int = int(math.Round(float64(*discordSendBuffer) / 10.0))
	log.Println("To Discord Jitter Buffer: ", discordStartStreamingCount*10, " ms")

	var mumbleStartStreamCount int = int(math.Round(float64(*mumbleSendBuffer) / 10.0))
	log.Println("To Mumble Jitter Buffer: ", mumbleStartStreamCount*10, " ms")

	// create a command flag for each command mode
	var discordCommand bool
	var mumbleCommand bool

	switch *commandMode {
	case "both":
		discordCommand = true
		mumbleCommand = true
	case "mumble":
		discordCommand = false
		mumbleCommand = true
	case "discord":
		discordCommand = true
		mumbleCommand = false
	case "none":
		discordCommand = false
		mumbleCommand = false
	}

	// BRIDGE SETUP

	Bridge := &bridge.BridgeState{
		BridgeConfig: &bridge.BridgeConfig{
			// MumbleConfig:   config,
			Command:                    *command,
			MumbleAddr:                 *mumbleAddr + ":" + strconv.Itoa(*mumblePort),
			MumbleInsecure:             *mumbleInsecure,
			MumbleCertificate:          *mumbleCertificate,
			MumbleChannel:              strings.Split(*mumbleChannel, "/"),
			MumbleStartStreamCount:     mumbleStartStreamCount,
			MumbleDisableText:          *mumbleDisableText,
			MumbleCommand:              mumbleCommand,
			GID:                        *discordGID,
			CID:                        *discordCID,
			DiscordStartStreamingCount: discordStartStreamingCount,
			DiscordTextMode:            *discordTextMode,
			DiscordDisableBotStatus:    *discordDisableBotStatus,
			DiscordCommand:             discordCommand,
			ChatBridge:                 *chatBridge,
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
		Connect:     Bridge.MumbleListener.MumbleConnect,
		UserChange:  Bridge.MumbleListener.MumbleUserChange,
		TextMessage: Bridge.MumbleListener.MumbleTextMessage,
		// TODO - notify discord on channel change.
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
	log.Printf("Discord bot looking for command !%v", *command)

	switch *mode {
	case "auto":
		log.Println("Starting in automatic mode")
		Bridge.AutoChanDie = make(chan bool)
		Bridge.Mode = bridge.BridgeModeAuto
		Bridge.DiscordChannelID = Bridge.BridgeConfig.CID
		go Bridge.AutoBridge()
	case "manual":
		log.Println("Starting in manual mode")
		Bridge.Mode = bridge.BridgeModeManual
	case "constant":
		log.Println("Starting in constant mode")
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
