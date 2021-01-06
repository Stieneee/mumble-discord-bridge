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
	"layeh.com/gumble/gumbleutil"
)

type BridgeState struct {
	ActiveConn       chan bool
	Connected        bool
	Client           *gumble.Client
	DiscordUsers     map[string]bool
	MumbleUsers      map[string]bool
	MumbleUserCount  int
	DiscordUserCount int
	AutoChan         chan bool
}

func startBridge(discord *discordgo.Session, discordGID string, discordCID string, l *Listener, die chan bool) {
	dgv, err := discord.ChannelVoiceJoin(discordGID, discordCID, false, false)
	if err != nil {
		log.Println(err)
		return
	}
	defer dgv.Speaking(false)
	defer dgv.Close()
	discord.ShouldReconnectOnError = true

	// MUMBLE Setup

	m := MumbleDuplex{
		Close: make(chan bool),
	}

	var tlsConfig tls.Config
	if l.BridgeConf.MumbleInsecure {
		tlsConfig.InsecureSkipVerify = true
	}
	l.BridgeConf.Config.Attach(gumbleutil.Listener{
		Connect:    l.mumbleConnect,
		UserChange: l.mumbleUserChange,
	})
	mumble, err := gumble.DialWithDialer(new(net.Dialer), l.BridgeConf.MumbleAddr, l.BridgeConf.Config, &tlsConfig)

	if err != nil {
		log.Println(err)
		return
	}
	defer mumble.Disconnect()
	l.Bridge.Client = mumble
	// Shared Channels
	// Shared channels pass PCM information in 10ms chunks [480]int16
	var toMumble = mumble.AudioOutgoing()
	var toDiscord = make(chan []int16, 100)

	log.Println("Mumble Connected")

	// Start Passing Between
	// Mumble
	go m.fromMumbleMixer(toDiscord, die)
	det := l.BridgeConf.Config.AudioListeners.Attach(m)

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

	l.Bridge.Connected = true

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
		l.Bridge.Connected = false
		l.Bridge.Client = nil
		l.Bridge.MumbleUserCount = 0
		l.Bridge.MumbleUsers = make(map[string]bool)
		l.Bridge.DiscordUserCount = 0
		l.Bridge.DiscordUsers = make(map[string]bool)
	}
}

func discordStatusUpdate(dg *discordgo.Session, host, port string, l *Listener) {
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
			if l.Bridge.Connected {
				curr = curr - 1
			}
			if curr != l.Bridge.MumbleUserCount {
				l.Bridge.MumbleUserCount = curr
			}
			if curr == 0 {
				status = ""
			} else {
				if len(l.Bridge.MumbleUsers) > 0 {
					status = fmt.Sprintf("%v/%v users in Mumble\n", len(l.Bridge.MumbleUsers), curr)
				} else {
					status = fmt.Sprintf("%v users in Mumble\n", curr)
				}
			}
			dg.UpdateListeningStatus(status)
		}
	}
}

func AutoBridge(s *discordgo.Session, l *Listener) {
	log.Println("beginning auto mode")
	for {
		select {
		default:
		case <-l.Bridge.AutoChan:
			log.Println("ending automode")
			return
		}
		time.Sleep(3 * time.Second)
		if !l.Bridge.Connected && l.Bridge.MumbleUserCount > 0 && l.Bridge.DiscordUserCount > 0 {
			log.Println("users detected in mumble and discord, bridging")
			die := make(chan bool)
			l.Bridge.ActiveConn = die
			go startBridge(s, l.BridgeConf.GID, l.BridgeConf.CID, l, die)
		}
		if l.Bridge.Connected && l.Bridge.MumbleUserCount == 0 && l.Bridge.DiscordUserCount <= 1 {
			log.Println("no one online, killing bridge")
			l.Bridge.ActiveConn <- true
			MumbleReset()
			DiscordReset()
		}
	}
}
