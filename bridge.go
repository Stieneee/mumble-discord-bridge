package main

import (
	"context"
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"layeh.com/gumble/gumble"
)

type discordUser struct {
	username string
	seen     bool
	dm       *discordgo.Channel
}

//BridgeState manages dynamic information about the bridge during runtime
type BridgeState struct {
	// The configuration data for this bridge
	BridgeConfig *BridgeConfig

	// External requests to kill the bridge
	BridgeDie chan bool

	// Lock to only allow one bridge session at a time
	lock sync.Mutex

	// Wait for bridge to exit cleanly
	WaitExit *sync.WaitGroup

	// Bridge connection
	Connected bool

	// The bridge mode constant, auto, manual. Default is constant.
	Mode bridgeMode

	// Discord session. This is created and outside the bridge state
	DiscordSession *discordgo.Session

	// Discord voice connection. Empty if not connected.
	DiscordVoice *discordgo.VoiceConnection

	// Mumble client. Empty if not connected.
	MumbleClient *gumble.Client

	// Map of Discord users tracked by this bridge.
	DiscordUsers      map[string]discordUser
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
}

// startBridge established the voice connection
func (b *BridgeState) startBridge() {
	b.lock.Lock()
	defer b.lock.Unlock()

	b.BridgeDie = make(chan bool)
	defer close(b.BridgeDie)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	wg := sync.WaitGroup{}
	b.WaitExit = &wg

	var err error

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

	b.MumbleStream = &MumbleDuplex{}
	det := b.BridgeConfig.MumbleConfig.AudioListeners.Attach(b.MumbleStream)
	defer det.Detach()

	var tlsConfig tls.Config
	if b.BridgeConfig.MumbleInsecure {
		tlsConfig.InsecureSkipVerify = true
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

	// Start Passing Between

	// From Mumble
	go b.MumbleStream.fromMumbleMixer(ctx, &wg, toDiscord)

	// From Discord
	b.DiscordStream = &DiscordDuplex{
		Bridge:         b,
		fromDiscordMap: make(map[uint32]fromDiscord),
	}

	go b.DiscordStream.discordReceivePCM(ctx, &wg, cancel)
	go b.DiscordStream.fromDiscordMixer(ctx, &wg, toMumble)

	// To Discord
	go b.DiscordStream.discordSendPCM(ctx, &wg, cancel, toDiscord)

	// Monitor Mumble
	go func() {
		wg.Add(1)
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
				wg.Done()
				return
			}
		}
	}()

	b.Connected = true

	// Hold until cancelled or external die request
	select {
	case <-ctx.Done():
		log.Println("Bridge internal context cancel")
	case <-b.BridgeDie:
		log.Println("Bridge die request received")
		cancel()
	}

	b.Connected = false
	wg.Wait()
	log.Println("Terminating Bridge")
	b.MumbleUsersMutex.Lock()
	b.MumbleUsers = make(map[string]bool)
	b.MumbleUsersMutex.Unlock()
	b.DiscordUsers = make(map[string]discordUser)
}

func (b *BridgeState) discordStatusUpdate() {
	m, _ := time.ParseDuration("30s")
	for {
		time.Sleep(3 * time.Second)
		resp, err := gumble.Ping(b.BridgeConfig.MumbleAddr, -1, m)
		status := ""

		if err != nil {
			log.Printf("error pinging mumble server %v\n", err)
			b.DiscordSession.UpdateListeningStatus("an error pinging mumble")
		} else {
			b.MumbleUsersMutex.Lock()
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
			b.MumbleUsersMutex.Unlock()
			b.DiscordSession.UpdateListeningStatus(status)
		}
	}
}

// AutoBridge starts a goroutine to check the number of users in discord and mumble
// when there is at least one user on both, starts up the bridge
// when there are no users on either side, kills the bridge
func (b *BridgeState) AutoBridge() {
	log.Println("beginning auto mode")
	ticker := time.NewTicker(3 * time.Second)

	for {
		select {
		case <-ticker.C:
		case <-b.AutoChanDie:
			log.Println("ending automode")
			return
		}

		b.MumbleUsersMutex.Lock()
		b.DiscordUsersMutex.Lock()

		if !b.Connected && b.MumbleUserCount > 0 && len(b.DiscordUsers) > 0 {
			log.Println("users detected in mumble and discord, bridging")
			go b.startBridge()
		}
		if b.Connected && b.MumbleUserCount == 0 && len(b.DiscordUsers) <= 1 {
			log.Println("no one online, killing bridge")
			b.BridgeDie <- true
		}

		b.MumbleUsersMutex.Unlock()
		b.DiscordUsersMutex.Unlock()
	}
}

func (b *BridgeState) discordSendMessageAll(msg string) {
	if b.BridgeConfig.DiscordDisableText {
		return
	}

	b.DiscordUsersMutex.Lock()
	for id := range b.DiscordUsers {
		du := b.DiscordUsers[id]
		if du.dm != nil {
			b.DiscordSession.ChannelMessageSend(du.dm.ID, msg)
		}
	}
	b.DiscordUsersMutex.Unlock()
}
