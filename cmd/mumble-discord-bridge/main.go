package main

import (
	"flag"
	"fmt"
	"log"
	"math"
	"os"
	"os/signal"
	"runtime/pprof"
	"syscall"

	"github.com/joho/godotenv"
	"github.com/stieneee/mumble-discord-bridge/internal/bridge"
	"github.com/stieneee/mumble-discord-bridge/pkg/bridgelib"
)

var (
	// Build vars
	version string
	commit  string
	date    string
)

const (
	// Command modes
	commandModeBoth    = "both"
	commandModeMumble  = "mumble"
	commandModeDiscord = "discord"
	commandModeNone    = "none"
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
	chatBridge := flag.Bool("chat-bridge", lookupEnvOrBool("CHAT_BRIDGE", false), "CHAT_BRIDGE, enable chat bridge")
	command := flag.String("command", lookupEnvOrString("COMMAND", "mumble-discord"), "COMMAND, command phrase '!mumble-discord help' to control the bridge via text channels")
	commandMode := flag.String("command-mode", lookupEnvOrString("COMMAND_MODE", "both"), "COMMAND_MODE, [both, mumble, discord, none] determine which side of the bridge will respond to commands")
	mode := flag.String("mode", lookupEnvOrString("MODE", "constant"), "MODE, [constant, manual, auto] determine which mode the bridge starts in")
	nice := flag.Bool("nice", lookupEnvOrBool("NICE", false), "NICE, whether the bridge should automatically try to 'nice' itself")
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
	if *commandMode != commandModeBoth && *commandMode != commandModeMumble && *commandMode != commandModeDiscord && *commandMode != commandModeNone {
		log.Fatalln("invalid command mode set")
	}
	if *commandMode != commandModeNone {
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
		if err := pprof.StartCPUProfile(f); err != nil {
			if closeErr := f.Close(); closeErr != nil {
				log.Printf("Error closing CPU profile file: %v", closeErr)
			}
			log.Fatal("could not start CPU profile: ", err)
		}
		defer func() {
			if err := f.Close(); err != nil {
				log.Println("could not close CPU profile: ", err)
			}
		}()
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
	log.Printf("Chat bridge settings - mumbleDisableText: %v, discordTextMode: %s, chatBridge flag: %v",
		*mumbleDisableText, *discordTextMode, *chatBridge)

	// Override chatBridge if conditions aren't met
	switch {
	case *mumbleDisableText:
		log.Println("Warning: Chat bridge disabled because mumbleDisableText is set to true")
		*chatBridge = false
	case *discordTextMode != "channel":
		log.Printf("Warning: Chat bridge disabled because discordTextMode is '%s' instead of 'channel'", *discordTextMode)
		*chatBridge = false
	}

	if !*chatBridge {
		log.Println("Warning: Chat bridge disabled because chatBridge flag is set to false")
	} else {
		log.Println("Chat bridge is ENABLED")
	}

	log.Printf("Final chat bridge status: %v", *chatBridge)

	discordStartStreamingCount := int(math.Round(float64(*discordSendBuffer) / 10.0))
	log.Println("To Discord Jitter Buffer: ", discordStartStreamingCount*10, " ms")

	mumbleStartStreamCount := int(math.Round(float64(*mumbleSendBuffer) / 10.0))
	log.Println("To Mumble Jitter Buffer: ", mumbleStartStreamCount*10, " ms")

	// create a command flag for each command mode
	var discordCommand bool
	var mumbleCommand bool

	switch *commandMode {
	case commandModeBoth:
		discordCommand = true
		mumbleCommand = true
	case commandModeMumble:
		discordCommand = false
		mumbleCommand = true
	case commandModeDiscord:
		discordCommand = true
		mumbleCommand = false
	case commandModeNone:
		discordCommand = false
		mumbleCommand = false
	}

	// BRIDGE SETUP

	// Create shared Discord client
	discordClient, err := bridgelib.NewSharedDiscordClient(*discordToken, nil)
	if err != nil {
		if *cpuprofile != "" {
			pprof.StopCPUProfile()
		}
		log.Fatalln("Failed to create Discord client:", err) //nolint:gocritic // exitAfterDefer: StopCPUProfile is called manually before exit
	}

	// Connect to Discord
	err = discordClient.Connect()
	if err != nil {
		if *cpuprofile != "" {
			pprof.StopCPUProfile()
		}
		log.Fatalln("Failed to connect to Discord:", err)
	}
	defer func() {
		if err := discordClient.Disconnect(); err != nil {
			log.Printf("Error disconnecting from Discord: %v", err)
		}
	}()

	// Create bridge configuration
	config := &bridgelib.BridgeConfig{
		Command:           *command,
		MumbleAddress:     *mumbleAddr,
		MumblePort:        *mumblePort,
		MumbleUsername:    *mumbleUsername,
		MumblePassword:    *mumblePassword,
		MumbleChannel:     *mumbleChannel,
		MumbleSendBuffer:  *mumbleSendBuffer,
		MumbleDisableText: *mumbleDisableText,
		MumbleCommand:     mumbleCommand,
		MumbleBotFlag:     *mumbleBotFlag,
		MumbleInsecure:    *mumbleInsecure,
		MumbleCertificate: *mumbleCertificate,
		DiscordGID:        *discordGID,
		DiscordCID:        *discordCID,
		DiscordSendBuffer: *discordSendBuffer,
		DiscordTextMode:   *discordTextMode,
		DiscordCommand:    discordCommand,
		ChatBridge:        *chatBridge,
		Mode:              *mode,
		Version:           version,
	}

	// Create and start bridge instance
	bridgeInstance, err := bridgelib.NewBridgeInstance("default", config, discordClient)
	if err != nil {
		if *cpuprofile != "" {
			pprof.StopCPUProfile()
		}
		log.Fatalln("Failed to create bridge instance:", err)
	}

	// Start metrics server if enabled
	if *promEnable {
		go bridge.StartPromServer(*promPort, bridgeInstance.State)
	}

	// Set prometheus start time metric
	bridge.PromApplicationStartTime.SetToCurrentTime()

	// Start the bridge
	if err := bridgeInstance.Start(); err != nil {
		if *cpuprofile != "" {
			pprof.StopCPUProfile()
		}
		log.Fatalln("Failed to start bridge:", err)
	}

	// Wait for termination signal
	sc := make(chan os.Signal, 1)
	signal.Notify(sc, syscall.SIGINT, syscall.SIGTERM, os.Interrupt)
	<-sc

	log.Println("OS Signal. Bot shutting down")

	if err := bridgeInstance.Stop(); err != nil {
		log.Println("Error stopping bridge:", err)
	}
}
