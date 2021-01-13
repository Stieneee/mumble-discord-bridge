package main

import (
	"crypto/tls"
	"flag"
	"fmt"
	"log"
	"net"
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

var (
	// Build vars
	version string
	commit  string
	date    string
)

var (
	discordGID *string
	discordCID *string
)

func lookupEnvOrString(key string, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return defaultVal
}

func lookupEnvOrInt(key string, defaultVal int) int {
	if val, ok := os.LookupEnv(key); ok {
		v, err := strconv.Atoi(val)
		if err != nil {
			log.Fatalf("LookupEnvOrInt[%s]: %v", key, err)
		}
		return v
	}
	return defaultVal
}

func lookupEnvOrBool(key string, defaultVal bool) bool {
	if val, ok := os.LookupEnv(key); ok {
		v, err := strconv.ParseBool(val)
		if err != nil {
			log.Fatalf("LookupEnvOrInt[%s]: %v", key, err)
		}
		return v
	}
	return defaultVal
}

func getConfig(fs *flag.FlagSet) []string {
	cfg := make([]string, 0, 10)
	fs.VisitAll(func(f *flag.Flag) {
		cfg = append(cfg, fmt.Sprintf("%s:%q", f.Name, f.Value.String()))
	})

	return cfg
}

func main() {
	log.Println("Mumble-Discord-Bridge")
	log.Println("v" + version + " " + commit + " " + date)

	godotenv.Load()

	mumbleAddr := flag.String("mumble-address", lookupEnvOrString("MUMBLE_ADDRESS", ""), "MUMBLE_ADDRESS, mumble server address, example example.com")
	mumblePort := flag.Int("mumble-port", lookupEnvOrInt("MUMBLE_PORT", 64738), "MUMBLE_PORT mumble port")
	mumbleUsername := flag.String("mumble-username", lookupEnvOrString("MUMBLE_USERNAME", "discord"), "MUMBLE_USERNAME, mumble username")
	mumblePassword := flag.String("mumble-password", lookupEnvOrString("MUMBLE_PASSWORD", ""), "MUMBLE_PASSWORD, mumble password, optional")
	mumbleInsecure := flag.Bool("mumble-insecure", lookupEnvOrBool("MUMBLE_INSECURE", false), "mumble insecure,  env alt MUMBLE_INSECURE")

	discordToken := flag.String("discord-token", lookupEnvOrString("DISCORD_TOKEN", ""), "DISCORD_TOKEN, discord bot token")
	discordGID = flag.String("discord-gid", lookupEnvOrString("DISCORD_GID", ""), "DISCORD_GID, discord gid")
	discordCID = flag.String("discord-cid", lookupEnvOrString("DISCORD_CID", ""), "DISCORD_CID, discord cid")

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

	// Attempt to set Process Priority
	// This is allowed to fail

	err := syscall.Setpriority(syscall.PRIO_PROCESS, os.Getpid(), -5)
	if err != nil {
		log.Println("Unable to set process priority. ", err)
	}

	// DISCORD Setup

	discord, err := discordgo.New("Bot " + *discordToken)
	if err != nil {
		log.Println(err)
		return
	}

	// Open Websocket
	discord.ShouldReconnectOnError = true
	discord.LogLevel = 1
	discord.StateEnabled = true
	discord.Identify.Intents = discordgo.MakeIntent(discordgo.IntentsAllWithoutPrivileged)

	err = discord.Open()
	if err != nil {
		log.Println(err)
		return
	}
	defer discord.Close()

	log.Println("Discord Bot Connected")

	dgv, err := discord.ChannelVoiceJoin(*discordGID, *discordCID, false, false)
	if err != nil {
		log.Println(err)
		return
	}
	defer dgv.Speaking(false)
	defer dgv.Close()

	// MUMBLE Setup

	config := gumble.NewConfig()
	config.Username = *mumbleUsername
	config.Password = *mumblePassword
	config.AudioInterval = time.Millisecond * 10

	m := MumbleDuplex{}
	ml := MumbleEventListener{
		d: discord,
	}

	var tlsConfig tls.Config
	if *mumbleInsecure {
		tlsConfig.InsecureSkipVerify = true
	}

	mumble, err := gumble.DialWithDialer(new(net.Dialer), *mumbleAddr+":"+strconv.Itoa(*mumblePort), config, &tlsConfig)

	if err != nil {
		log.Println(err)
		return
	}
	defer mumble.Disconnect()

	// Shared Channels
	// Shared channels pass PCM information in 10ms chunks [480]int16
	var toMumble = mumble.AudioOutgoing()
	var toDiscord = make(chan []int16, 100)
	var die = make(chan bool)

	log.Println("Mumble Connected")

	// Initial User States

	// Start Passing Between
	// Mumble
	go m.fromMumbleMixer(toDiscord)
	config.AudioListeners.Attach(m)
	config.Listeners.Attach(ml)
	//Discord
	go discordReceivePCM(dgv, die)
	go fromDiscordMixer(toMumble)
	go discordSendPCM(dgv, toDiscord, die)
	go discordMemberWatcher(discord, mumble)

	// Wait for Exit Signal
	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt)

	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		for {
			<-ticker.C
			if mumble.State() != 2 {
				log.Println("Lost mumble connection " + strconv.Itoa(int(mumble.State())))
				die <- true
			}
		}
	}()

	select {
	case sig := <-c:
		log.Printf("\nGot %s signal. Terminating Mumble-Bridge\n", sig)
	case <-die:
		log.Println("\nGot internal die request. Terminating Mumble-Bridge")
	}
	time.AfterFunc(5*time.Second, func() {
		log.Println("Graceful disconnect timeout, Forcing!")
		os.Exit(9)
	})
}
