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
				die <- true
				m.Close <- true
			}
		}
	}()

	select {
	case sig := <-c:
		log.Printf("\nGot %s signal. Terminating Mumble-Bridge\n", sig)
	case <-die:
		log.Println("\nGot internal die request. Terminating Mumble-Bridge")
		close(toMumble)
		dgv.Disconnect()
		log.Println("Closing mumble threads")
		det.Detach()
	}
}

func pingMumble(host, port string, c chan int) {
	m, _ := time.ParseDuration("30s")
	curr := 0
	log.Println("Started mumble ping loop")
	for {
		time.Sleep(3 * time.Second)
		resp, err := gumble.Ping(host+":"+port, -1, m)
		if err != nil {
			panic(err)
		}
		if resp.ConnectedUsers-1 != curr {
			curr = resp.ConnectedUsers - 1
			log.Printf("Now %v users in mumble\n", curr)
			if curr > 0 {
				c <- curr
			}
		}
	}
	log.Println("Mumble ping loop broken")
}

func discordStatusUpdate(dg *discordgo.Session, c chan int) {
	status := ""
	curr := 0
	log.Println("Started discord control loop")
	for {
		curr = <-c
		log.Println("Updating discord status")
		if curr == 0 {
			status = ""
		} else {
			status = fmt.Sprintf("%v users in Mumble\n", curr)
		}
		dg.UpdateListeningStatus(status)
	}
	log.Println("Discord control loop broken")
}
