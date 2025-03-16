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

	if err := godotenv.Load(); err != nil {
		log.Println("Failed to load .env file:", err)
	}

	mumbleAddr := flag.String("mumble-address", lookupEnvOrString("MUMBLE_ADDRESS", ""), "MUMBLE_ADDRESS, mumble server address, example example.com, required")
	mumblePort := flag.Int("mumble-port", lookupEnvOrInt("MUMBLE_PORT", 64738), "MUMBLE_PORT, mumble port")
	mumbleUsername := flag.String("mumble-username", lookupEnvOrString("MUMBLE_USERNAME", "Discord"), "MUMBLE_USERNAME, mumble username")
	mumblePassword := flag.String("mumble-password", lookupEnvOrString("MUMBLE_PASSWORD", ""), "MUMBLE_PASSWORD, mumble password, optional")
	mumbleInsecure := flag.Bool("mumble-insecure", lookupEnvOrBool("MUMBLE_INSECURE", false), " MUMBLE_INSECURE, mumble insecure, flag")
	mumbleCertificate := flag.String("mumble-certificate", lookupEnvOrString("MUMBLE_CERTIFICATE", ""), "MUMBLE_CERTIFICATE, client certificate to use when connecting to the Mumble server")
	mumbleChannel := flag.String("mumble-channel", lookupEnvOrString("MUMBLE_CHANNEL", ""), "MUMBLE_CHANNEL, mumble channel to start in, using '/' to separate nested channels, optional")
	mumbleSendBuffer := flag.Int("to-mumble-buffer", lookupEnvOrInt("TO_MUMBLE_BUFFER", 50), "TO_MUMBLE_BUFFER, Jitter buffer from Discord to Mumble to absorb timing issues related to network, OS and hardware quality, increments of 10ms")
	mumbleDisableText := flag.Bool("mumble-disable-text", lookupEnvOrBool("MUMBLE_DISABLE_TEXT", false), "MUMBLE_DISABLE_TEXT, disable sending text to mumble")
	mumbleBotFlag := flag.Bool("mumble-bot", lookupEnvOrBool("MUMBLE_BOT", false), "MUMBLE_BOT, exclude bot from mumble user count, optional, requires mumble v1.5 or later")
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

	// Optional CPU Profiling
	if *cpuprofile != "" {
		f, err := os.Create(*cpuprofile)
		if err != nil {
			log.Fatal("could not create CPU profile: ", err)
		}
		defer func() {
			if err := f.Close(); err != nil {
				log.Println("could not close CPU profile: ", err)
			}
		}()
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

	b := &bridge.BridgeState{
		BridgeConfig: &bridge.BridgeConfig{
			Command:                    *command,
			MumbleAddr:                 *mumbleAddr + ":" + strconv.Itoa(*mumblePort),
			MumbleInsecure:             *mumbleInsecure,
			MumbleCertificate:          *mumbleCertificate,
			MumbleChannel:              strings.Split(*mumbleChannel, "/"),
			MumbleStartStreamCount:     mumbleStartStreamCount,
			MumbleDisableText:          *mumbleDisableText,
			MumbleCommand:              mumbleCommand,
			MumbleBotFlag:              *mumbleBotFlag,
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

	if *promEnable {
		go bridge.StartPromServer(*promPort, b)
	}

	bridge.PromApplicationStartTime.SetToCurrentTime()

	// MUMBLE SETUP
	b.BridgeConfig.MumbleConfig = gumble.NewConfig()
	b.BridgeConfig.MumbleConfig.Username = *mumbleUsername
	b.BridgeConfig.MumbleConfig.Password = *mumblePassword
	b.BridgeConfig.MumbleConfig.AudioInterval = time.Millisecond * 10

	b.MumbleListener = &bridge.MumbleListener{
		Bridge: b,
	}

	b.BridgeConfig.MumbleConfig.Attach(gumbleutil.Listener{
		Connect:     b.MumbleListener.MumbleConnect,
		UserChange:  b.MumbleListener.MumbleUserChange,
		TextMessage: b.MumbleListener.MumbleTextMessage,
		// TODO - notify discord on channel change.
	})

	// DISCORD SETUP
	b.DiscordSession, err = discordgo.New("Bot " + *discordToken)
	if err != nil {
		log.Println(err)
		return
	}
	intents := discordgo.MakeIntent(discordgo.IntentsGuildVoiceStates |
		discordgo.IntentsGuildMessages |
		discordgo.IntentsGuildMessageReactions |
		discordgo.IntentsDirectMessages |
		discordgo.IntentsDirectMessageReactions |
		discordgo.IntentsMessageContent)
	b.DiscordSession.LogLevel = *debug
	b.DiscordSession.StateEnabled = true
	b.DiscordSession.Identify.Intents = intents
	b.DiscordSession.ShouldReconnectOnError = true
	// register handlers
	b.DiscordListener = &bridge.DiscordListener{
		Bridge: b,
	}
	b.DiscordSession.AddHandler(b.DiscordListener.MessageCreate)
	b.DiscordSession.AddHandler(b.DiscordListener.GuildCreate)
	b.DiscordSession.AddHandler(b.DiscordListener.VoiceUpdate)

	// Open Discord websocket
	maxRetries := 5
	for i := 0; i < maxRetries; i++ {
		err = b.DiscordSession.Open()
		if err == nil {
			break
		}
		log.Printf("Failed to connect to Discord (attempt %d/%d): %v", i+1, maxRetries, err)
		time.Sleep(5 * time.Second)
	}
	if err != nil {
		log.Fatalf("Could not connect to Discord after %d attempts: %v", maxRetries, err)
	}
	defer b.DiscordSession.Close()

	log.Println("Discord Bot Connected")
	log.Printf("Discord bot looking for command !%v", *command)

	switch *mode {
	case "auto":
		log.Println("Starting in automatic mode")
		b.AutoChanDie = make(chan bool)
		b.Mode = bridge.BridgeModeAuto
		b.DiscordChannelID = b.BridgeConfig.CID
		go b.AutoBridge()
	case "manual":
		log.Println("Starting in manual mode")
		b.Mode = bridge.BridgeModeManual
	case "constant":
		log.Println("Starting in constant mode")
		b.Mode = bridge.BridgeModeConstant
		b.DiscordChannelID = b.BridgeConfig.CID
		go func() {
			for {
				b.StartBridge()
				log.Println("Bridge died")
				time.Sleep(5 * time.Second)
				log.Println("Restarting")
			}
		}()
	default:
		log.Fatalln("invalid bridge mode set")
	}

	go b.DiscordStatusUpdate()

	// Shutdown on OS signal
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	log.Println("OS Signal. Bot shutting down")

	time.AfterFunc(30*time.Second, func() {
		os.Exit(99)
	})

	// Wait or the bridge to exit cleanly
	b.BridgeMutex.Lock()
	if b.Connected {
		// Prevent panic by checking if the channel is closed before sending
		select {
		case b.BridgeDie <- true:
		default:
		}
		b.WaitExit.Wait()
	}
	b.BridgeMutex.Unlock()
}
