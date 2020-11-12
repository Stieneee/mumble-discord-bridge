package main

import (
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"layeh.com/gopus"
	"layeh.com/gumble/gumble"
	_ "layeh.com/gumble/opus"
)

type fromDiscord struct {
	decoder   *gopus.Decoder
	pcm       chan []int16
	streaming bool
}

var discordMutex sync.Mutex
var discordMixerMutex sync.Mutex
var fromDiscordMap = make(map[uint32]fromDiscord)

// OnError gets called by dgvoice when an error is encountered.
// By default logs to STDERR
var OnError = func(str string, err error) {
	prefix := "dgVoice: " + str

	if err != nil {
		log.Println(prefix + ": " + err.Error())
	} else {
		log.Println(prefix)
	}
}

// SendPCM will receive on the provied channel encode
// received PCM data into Opus then send that to Discordgo
func discordSendPCM(v *discordgo.VoiceConnection, pcm <-chan []int16, die chan bool) {
	const channels int = 1
	const frameRate int = 48000              // audio sampling rate
	const frameSize int = 960                // uint16 size of each audio frame
	const maxBytes int = (frameSize * 2) * 2 // max size of opus data

	streaming := false

	opusEncoder, err := gopus.NewEncoder(frameRate, channels, gopus.Audio)
	if err != nil {
		OnError("NewEncoder Error", err)
		panic(err)
	}

	ticker := time.NewTicker(20 * time.Millisecond)

	lastReady := true
	var readyTimeout *time.Timer

	for {
		<-ticker.C

		if len(pcm) > 1 {
			if !streaming {
				v.Speaking(true)
				streaming = true
			}

			r1 := <-pcm
			r2 := <-pcm

			// try encoding pcm frame with Opus
			opus, err := opusEncoder.Encode(append(r1, r2...), frameSize, maxBytes)
			if err != nil {
				OnError("Encoding Error", err)
				continue
			}

			if v.Ready == false || v.OpusSend == nil {
				if lastReady == true {
					OnError(fmt.Sprintf("Discordgo not ready for opus packets. %+v : %+v", v.Ready, v.OpusSend), nil)
					readyTimeout = time.AfterFunc(30*time.Second, func() {
						die <- true
					})
					lastReady = false
				}
				continue
			} else if lastReady == false {
				fmt.Println("Discordgo ready to send opus packets")
				lastReady = true
				readyTimeout.Stop()
			}

			v.OpusSend <- opus
		} else {
			if streaming {
				v.Speaking(false)
				streaming = false
			}
		}
	}
}

// ReceivePCM will receive on the the Discordgo OpusRecv channel and decode
// the opus audio into PCM then send it on the provided channel.
func discordReceivePCM(v *discordgo.VoiceConnection, die chan bool) {
	var err error

	lastReady := true
	var readyTimeout *time.Timer

	for {
		if v.Ready == false || v.OpusRecv == nil {
			if lastReady == true {
				OnError(fmt.Sprintf("Discordgo not to receive opus packets. %+v : %+v", v.Ready, v.OpusSend), nil)
				readyTimeout = time.AfterFunc(30*time.Second, func() {
					die <- true
				})
				lastReady = false
			}
			continue
		} else if lastReady == false {
			fmt.Println("Discordgo ready to receive packets")
			lastReady = true
			readyTimeout.Stop()
		}

		p, ok := <-v.OpusRecv
		if !ok {
			log.Println("Opus not ok")
			continue
		}

		discordMutex.Lock()
		_, ok = fromDiscordMap[p.SSRC]
		discordMutex.Unlock()
		if !ok {
			newStream := fromDiscord{}
			newStream.pcm = make(chan []int16, 100)
			newStream.streaming = false
			newStream.decoder, err = gopus.NewDecoder(48000, 1)
			if err != nil {
				OnError("error creating opus decoder", err)
				continue
			}
			discordMutex.Lock()
			fromDiscordMap[p.SSRC] = newStream
			discordMutex.Unlock()
		}

		discordMutex.Lock()
		p.PCM, err = fromDiscordMap[p.SSRC].decoder.Decode(p.Opus, 960, false)
		discordMutex.Unlock()
		if err != nil {
			OnError("Error decoding opus data", err)
			continue
		}

		discordMutex.Lock()
		fromDiscordMap[p.SSRC].pcm <- p.PCM[0:480]
		fromDiscordMap[p.SSRC].pcm <- p.PCM[480:960]
		discordMutex.Unlock()
	}
}

func fromDiscordMixer(toMumble chan<- gumble.AudioBuffer) {
	ticker := time.NewTicker(10 * time.Millisecond)
	sendAudio := false

	for {
		<-ticker.C
		discordMutex.Lock()

		sendAudio = false
		internalMixerArr := make([][]int16, 0)

		// Work through each channel
		for i := range fromDiscordMap {
			if len(fromDiscordMap[i].pcm) > 0 {
				sendAudio = true
				if fromDiscordMap[i].streaming == false {
					x := fromDiscordMap[i]
					x.streaming = true
					fromDiscordMap[i] = x
				}

				x1 := (<-fromDiscordMap[i].pcm)
				internalMixerArr = append(internalMixerArr, x1)
			} else {
				if fromDiscordMap[i].streaming == true {
					x := fromDiscordMap[i]
					x.streaming = false
					fromDiscordMap[i] = x
				}
			}
		}

		discordMutex.Unlock()

		outBuf := make([]int16, 480)

		for i := 0; i < len(outBuf); i++ {
			for j := 0; j < len(internalMixerArr); j++ {
				outBuf[i] += (internalMixerArr[j])[i]
			}
		}

		if sendAudio {
			select {
			case toMumble <- outBuf:
			default:
				log.Println("toMumble buffer full. Dropping packet")
			}

		}
	}
}
