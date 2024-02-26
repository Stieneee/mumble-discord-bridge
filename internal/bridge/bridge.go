package bridge

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stieneee/gumble/gumble"
)

type DiscordUser struct {
	username string
	seen     bool
	dm       *discordgo.Channel
}

type BridgeMode int

// define a String method for the BridgeMode type
func (b BridgeMode) String() string {
	return [...]string{"auto", "manual", "constant"}[b]
}

const (
	BridgeModeAuto BridgeMode = iota
	BridgeModeManual
	BridgeModeConstant
)

type BridgeConfig struct {
	// The command prefix for the bot
	Command string

	// The mumble server configuration
	MumbleConfig *gumble.Config

	// The mumble server address
	MumbleAddr string

	// The mumble server certificate
	MumbleInsecure bool

	// The mumble server certificate
	MumbleCertificate string

	// The mumble channel to join - it has been parse to an array of strings representing the channel path
	MumbleChannel []string

	// The mumble voice stream count
	MumbleStartStreamCount int

	// Disable text messages to mumble
	MumbleDisableText bool

	// Responed to mumble commands
	MumbleCommand bool

	// The discord server ID
	GID string

	// The discord voice channel ID
	CID string

	// The discord voice stream count
	DiscordStartStreamingCount int

	// The discord text mode, channel, user, disabled
	DiscordTextMode string

	// Disable text messages to discord
	DiscordDisableBotStatus bool

	// Respond to discord commands
	DiscordCommand bool

	// Bridge messages between mumble and discord
	ChatBridge bool

	// The version of the bridge
	Version string
}

// BridgeState manages dynamic information about the bridge during runtime
type BridgeState struct {
	// The configuration data for this bridge
	BridgeConfig *BridgeConfig

	// External requests to kill the bridge
	BridgeDie chan bool

	// Lock to only allow one bridge session at a time
	lock sync.Mutex

	// Wait for bridge to exit cleanly
	WaitExit *sync.WaitGroup

	// Bridge State Mutex
	BridgeMutex sync.Mutex

	// Bridge connection
	Connected bool

	// The bridge mode constant, auto, manual. Default is constant.
	Mode BridgeMode

	// Discord session. This is created and outside the bridge state
	DiscordSession *discordgo.Session

	// Discord voice connection. Empty if not connected.
	DiscordVoice *discordgo.VoiceConnection

	// Mumble client. Empty if not connected.
	MumbleClient *gumble.Client

	// Map of Discord users tracked by this bridge.
	DiscordUsers      map[string]DiscordUser
	DiscordUsersMutex sync.Mutex

	// Map of Mumble users tracked by this bridge
	MumbleUsers      map[string]bool
	MumbleUsersMutex sync.Mutex

	// Total Number of Mumble users
	MumbleUserCount int

	// Kill the auto connect routine
	AutoChanDie chan bool

	// Discord Duplex and Event Listener
	DiscordStream   *DiscordDuplex
	DiscordListener *DiscordListener

	// Mumble Duplex and Event Listener
	MumbleStream   *MumbleDuplex
	MumbleListener *MumbleListener

	// Discord Voice channel to join
	DiscordChannelID string

	// Start time of the bridge
	StartTime time.Time
}

// startBridge established the voice connection
func (b *BridgeState) StartBridge() {
	b.lock.Lock()
	defer b.lock.Unlock()

	b.BridgeDie = make(chan bool)
	defer close(b.BridgeDie)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wg := sync.WaitGroup{}
	b.WaitExit = &wg

	var err error

	promBridgeStarts.Inc()
	promBridgeStartTime.SetToCurrentTime()
	b.StartTime = time.Now()

	// DISCORD Connect Voice
	log.Println("Attempting to join Discord voice channel")
	if b.DiscordChannelID == "" {
		log.Println("Tried to start bridge but no Discord channel specified")
		return
	}
	b.DiscordVoice, err = b.DiscordSession.ChannelVoiceJoin(b.BridgeConfig.GID, b.DiscordChannelID, false, false)

	if err != nil {
		log.Println(err)
		b.DiscordVoice.Disconnect()
		return
	}
	defer b.DiscordVoice.Disconnect()
	defer b.DiscordVoice.Speaking(false)
	log.Println("Discord Voice Connected")

	// MUMBLE Connect

	b.MumbleStream = NewMumbleDuplex()
	det := b.BridgeConfig.MumbleConfig.AudioListeners.Attach(b.MumbleStream)
	defer det.Detach()

	var tlsConfig tls.Config
	if b.BridgeConfig.MumbleInsecure {
		tlsConfig.InsecureSkipVerify = true
	}

	if b.BridgeConfig.MumbleCertificate != "" {
		keyFile := b.BridgeConfig.MumbleCertificate
		if certificate, err := tls.LoadX509KeyPair(keyFile, keyFile); err != nil {
			fmt.Fprintf(os.Stderr, "%s: %s\n", os.Args[0], err)
			os.Exit(1)
		} else {
			tlsConfig.Certificates = append(tlsConfig.Certificates, certificate)
		}
	}

	log.Println("Attempting to join Mumble")
	b.MumbleClient, err = gumble.DialWithDialer(new(net.Dialer), b.BridgeConfig.MumbleAddr, b.BridgeConfig.MumbleConfig, &tlsConfig)

	if err != nil {
		log.Println(err)
		b.DiscordVoice.Disconnect()
		return
	}
	defer b.MumbleClient.Disconnect()
	log.Println("Mumble Connected")

	// Shared Channels
	// Shared channels pass PCM information in 10ms chunks [480]int16
	// These channels are internal and are not added to the bridge state.
	var toMumble = b.MumbleClient.AudioOutgoing()
	var toDiscord = make(chan []int16, 100)

	defer close(toDiscord)
	defer close(toMumble)

	// From Discord
	b.DiscordStream = NewDiscordDuplex(b)

	// Start Passing Between

	// From Mumble
	wg.Add(1)
	go func() {
		defer wg.Done()
		b.MumbleStream.fromMumbleMixer(ctx, cancel, toDiscord)
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		b.DiscordStream.discordReceivePCM(ctx, cancel)
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		b.DiscordStream.fromDiscordMixer(ctx, toMumble)
	}()

	// To Discord
	wg.Add(1)
	go func() {
		defer wg.Done()
		b.DiscordStream.discordSendPCM(ctx, cancel, toDiscord)
	}()

	// Monitor
	wg.Add(1)
	go func() {
		defer wg.Done()
		ticker := time.NewTicker(500 * time.Millisecond)
		for {
			select {
			case <-ticker.C:
				if b.MumbleClient == nil || b.MumbleClient.State() != 2 {
					if b.MumbleClient != nil {
						log.Println("Lost mumble connection " + strconv.Itoa(int(b.MumbleClient.State())))
					} else {
						log.Println("Lost mumble connection due to bridge dieing")
					}
					cancel()
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	b.BridgeMutex.Lock()
	b.Connected = true
	b.BridgeMutex.Unlock()

	// Hold until cancelled or external die request
	select {
	case <-ctx.Done():
		log.Println("Bridge internal context cancel")
	case <-b.BridgeDie:
		log.Println("Bridge die request received")
		cancel()
	}

	b.BridgeMutex.Lock()
	b.Connected = false
	b.BridgeMutex.Unlock()

	wg.Wait()
	log.Println("Terminating Bridge")
	b.MumbleUsersMutex.Lock()
	b.MumbleUsers = make(map[string]bool)
	b.MumbleUsersMutex.Unlock()
	b.DiscordUsers = make(map[string]DiscordUser)
}

func (b *BridgeState) DiscordStatusUpdate() {
	m, _ := time.ParseDuration("30s")
	for {
		time.Sleep(3 * time.Second)
		resp, err := gumble.Ping(b.BridgeConfig.MumbleAddr, -1, m)
		status := ""

		if err != nil {
			log.Printf("error pinging mumble server %v\n", err)
			if !b.BridgeConfig.DiscordDisableBotStatus {
				b.DiscordSession.UpdateListeningStatus("an error pinging mumble")
			}
		} else {

			promMumblePing.Set(float64(resp.Ping.Milliseconds()))

			b.MumbleUsersMutex.Lock()
			b.BridgeMutex.Lock()
			b.MumbleUserCount = resp.ConnectedUsers
			if b.Connected {
				b.MumbleUserCount = b.MumbleUserCount - 1
			}
			if b.MumbleUserCount == 0 {
				status = "No users in Mumble"
			} else {
				if len(b.MumbleUsers) > 0 {
					status = fmt.Sprintf("%v/%v users in Mumble\n", len(b.MumbleUsers), b.MumbleUserCount)
				} else {
					status = fmt.Sprintf("%v users in Mumble\n", b.MumbleUserCount)
				}
			}
			b.BridgeMutex.Unlock()
			b.MumbleUsersMutex.Unlock()
			if !b.BridgeConfig.DiscordDisableBotStatus {
				b.DiscordSession.UpdateListeningStatus(status)
			}
		}

		// report discord heartbeat
		discordHeartBeat := b.DiscordSession.LastHeartbeatAck.Sub(b.DiscordSession.LastHeartbeatSent).Milliseconds()
		if discordHeartBeat > 0 {
			promDiscordHeartBeat.Set(float64(discordHeartBeat))
		}

	}
}

// AutoBridge starts a goroutine to check the number of users in discord and mumble
// when there is at least one user on both, starts up the bridge
// when there are no users on either side, kills the bridge
func (b *BridgeState) AutoBridge() {
	log.Println("Beginning auto mode")
	ticker := time.NewTicker(3 * time.Second)

	for {
		select {
		case <-ticker.C:
		case <-b.AutoChanDie:
			log.Println("Ending automode")
			return
		}

		b.MumbleUsersMutex.Lock()
		b.DiscordUsersMutex.Lock()
		b.BridgeMutex.Lock()

		if !b.Connected && b.MumbleUserCount > 0 && len(b.DiscordUsers) > 0 {
			log.Println("Users detected in mumble and discord, bridging")
			go b.StartBridge()
		}
		if b.Connected && b.MumbleUserCount == 0 && len(b.DiscordUsers) <= 1 {
			log.Println("No one online, killing bridge")
			b.BridgeDie <- true
		}

		b.BridgeMutex.Unlock()
		b.MumbleUsersMutex.Unlock()
		b.DiscordUsersMutex.Unlock()
	}
}

// This function sends messages based on the bridge configuration
func (b *BridgeState) discordSendMessage(msg string) {
	switch b.BridgeConfig.DiscordTextMode {
	case "disabled":
		return
	case "channel":
		_, err := b.DiscordSession.ChannelMessageSend(b.DiscordChannelID, msg)
		if err != nil {
			log.Println(err)
		}
		return
	case "user":
		b.DiscordUsersMutex.Lock()
		for id := range b.DiscordUsers {
			du := b.DiscordUsers[id]
			if du.dm != nil {
				b.DiscordSession.ChannelMessageSend(du.dm.ID, msg)
			}
		}
		b.DiscordUsersMutex.Unlock()
		return
	default:
		log.Println("Invalid DiscordTextMode")
	}
}
