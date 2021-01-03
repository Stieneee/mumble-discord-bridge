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
	}
}

func pingMumble(host, port string, c chan int) {
	m, _ := time.ParseDuration("30s")
	curr := 0
	for {
		time.Sleep(3 * time.Second)
		resp, err := gumble.Ping(host+":"+port, -1, m)
		curr = resp.ConnectedUsers
		if err != nil {
			panic(err)
		}
		if Bridge.Connected {
			curr = curr - 1
		}
		if curr != Bridge.MumbleUserCount {
			Bridge.MumbleUserCount = curr
			c <- Bridge.MumbleUserCount
		}
	}
}

func discordStatusUpdate(dg *discordgo.Session, c chan int) {
	status := ""
	curr := 0
	for {
		curr = <-c
		if curr == 0 {
			status = ""
		} else {
			status = fmt.Sprintf("%v users in Mumble\n", curr)
		}
		dg.UpdateListeningStatus(status)
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
