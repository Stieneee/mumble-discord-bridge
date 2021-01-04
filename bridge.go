package main

import (
	"crypto/tls"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"time"

	"github.com/bwmarrin/discordgo"
	"layeh.com/gumble/gumble"
)

type BridgeState struct {
	ActiveConn       chan bool
	Connected        bool
	Client           *gumble.Client
	CurrentChannel   *gumble.Channel
	DiscordUsers     map[string]bool
	MumbleUserCount  int
	DiscordUserCount int
	AutoChan         chan bool
}

func startBridge(discord *discordgo.Session, discordGID string, discordCID string, config *gumble.Config, mumbleAddr string, mumbleInsecure bool, die chan bool) {
	dgv, err := discord.ChannelVoiceJoin(discordGID, discordCID, false, false)
	if err != nil {
		log.Println(err)
		return
	}
	defer dgv.Speaking(false)
	defer dgv.Close()
	Bridge.Connected = true
	discord.ShouldReconnectOnError = true

	// MUMBLE Setup

	m := MumbleDuplex{
		Close: make(chan bool),
	}

	var tlsConfig tls.Config
	if mumbleInsecure {
		tlsConfig.InsecureSkipVerify = true
	}

	mumble, err := gumble.DialWithDialer(new(net.Dialer), mumbleAddr, config, &tlsConfig)

	if err != nil {
		log.Println(err)
		return
	}
	defer mumble.Disconnect()
	Bridge.Client = mumble
	if BridgeConf.MumbleChannel != "" {
		//join specified channel
		startingChannel := mumble.Channels.Find(BridgeConf.MumbleChannel)
		mumble.Self.Move(startingChannel)
		Bridge.CurrentChannel = startingChannel
	}

	// Shared Channels
	// Shared channels pass PCM information in 10ms chunks [480]int16
	var toMumble = mumble.AudioOutgoing()
	var toDiscord = make(chan []int16, 100)

	log.Println("Mumble Connected")

	// Start Passing Between
	// Mumble
	go m.fromMumbleMixer(toDiscord, die)
	det := config.AudioListeners.Attach(m)
	//Discord
	go discordReceivePCM(dgv, die)
	go fromDiscordMixer(toMumble, die)
	go discordSendPCM(dgv, toDiscord, die)
	c := make(chan os.Signal)
	signal.Notify(c, os.Interrupt)

	go func() {
		ticker := time.NewTicker(500 * time.Millisecond)
		for {
			<-ticker.C
			if mumble.State() != 2 {
				log.Println("Lost mumble connection " + strconv.Itoa(int(mumble.State())))
				select {
				default:
					close(die)
				case <-die:
					//die is already closed
				}

				select {
				default:
					close(m.Close)
				case <-m.Close:
					//m.Close is already closed
				}
				return
			}
		}
	}()

	//Setup initial discord state
	g, err := discord.State.Guild(discordGID)
	Bridge.DiscordUsers = make(map[string]bool)
	if err != nil {
		log.Println("error finding guild")
		panic(err)
	}
	for _, vs := range g.VoiceStates {
		if vs.ChannelID == discordCID {
			Bridge.DiscordUserCount = Bridge.DiscordUserCount + 1
			u, err := discord.User(vs.UserID)
			if err != nil {
				log.Println("error looking up username")
				Bridge.DiscordUsers[u.Username] = true
				Bridge.CurrentChannel.Send(fmt.Sprintf("%v has joined Discord channel\n", u.Username), false)
			}
		}
	}

	select {
	case sig := <-c:
		log.Printf("\nGot %s signal. Terminating Mumble-Bridge\n", sig)
	case <-die:
		log.Println("\nGot internal die request. Terminating Mumble-Bridge")
		dgv.Disconnect()
		det.Detach()
		close(die)
		close(m.Close)
		close(toMumble)
		Bridge.Connected = false
		Bridge.Client = nil
		Bridge.MumbleUserCount = 0
		Bridge.DiscordUserCount = 0
		Bridge.DiscordUsers = nil
	}
}

func discordStatusUpdate(dg *discordgo.Session, host, port string) {
	status := ""
	curr := 0
	m, _ := time.ParseDuration("30s")
	for {
		time.Sleep(3 * time.Second)
		resp, err := gumble.Ping(host+":"+port, -1, m)

		if err != nil {
			log.Printf("error pinging mumble server %v\n", err)
			dg.UpdateListeningStatus("an error pinging mumble")
		} else {
			curr = resp.ConnectedUsers
			if Bridge.Connected {
				curr = curr - 1
			}
			if curr != Bridge.MumbleUserCount {
				Bridge.MumbleUserCount = curr
			}
			if curr == 0 {
				status = ""
			} else {
				status = fmt.Sprintf("%v users in Mumble\n", curr)
			}
			dg.UpdateListeningStatus(status)
		}
	}
}

func AutoBridge(s *discordgo.Session) {
	log.Println("beginning auto mode")
	for {
		select {
		default:
		case <-Bridge.AutoChan:
			log.Println("ending automode")
			return
		}
		time.Sleep(3 * time.Second)
		if !Bridge.Connected && Bridge.MumbleUserCount > 0 && Bridge.DiscordUserCount > 0 {
			log.Println("users detected in mumble and discord, bridging")
			die := make(chan bool)
			Bridge.ActiveConn = die
			go startBridge(s, BridgeConf.GID, BridgeConf.CID, BridgeConf.Config, BridgeConf.MumbleAddr, BridgeConf.MumbleInsecure, die)
		}
		if Bridge.Connected && Bridge.MumbleUserCount == 0 && Bridge.DiscordUserCount <= 1 {
			log.Println("no one online, killing bridge")
			Bridge.ActiveConn <- true
			MumbleReset()
			DiscordReset()
		}
	}
}
