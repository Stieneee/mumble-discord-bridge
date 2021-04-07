package main

import (
	"context"
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

// DiscordDuplex Handle discord voice stream
type DiscordDuplex struct {
	Bridge *BridgeState

	discordMutex      sync.Mutex
	discordMixerMutex sync.Mutex
	fromDiscordMap    map[uint32]fromDiscord
}

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
func (dd *DiscordDuplex) discordSendPCM(ctx context.Context, wg *sync.WaitGroup, cancel context.CancelFunc, pcm <-chan []int16) {
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

	wg.Add(1)

	for {
		select {
		case <-ctx.Done():
			wg.Done()
			return
		default:
		}
		<-ticker.C
		if len(pcm) > 1 {
			if !streaming {
				dd.Bridge.DiscordVoice.Speaking(true)
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

			if !dd.Bridge.DiscordVoice.Ready || dd.Bridge.DiscordVoice.OpusSend == nil {
				if lastReady {
					OnError(fmt.Sprintf("Discordgo not ready for opus packets. %+v : %+v", dd.Bridge.DiscordVoice.Ready, dd.Bridge.DiscordVoice.OpusSend), nil)
					readyTimeout = time.AfterFunc(30*time.Second, func() {
						log.Println("set ready timeout")
						cancel()
					})
					lastReady = false
				}
				continue
			} else if !lastReady {
				fmt.Println("Discordgo ready to send opus packets")
				lastReady = true
				readyTimeout.Stop()
			}
			dd.Bridge.DiscordVoice.OpusSend <- opus
		} else {
			if streaming {
				dd.Bridge.DiscordVoice.Speaking(false)
				streaming = false
			}
		}
	}
}

// ReceivePCM will receive on the the Discordgo OpusRecv channel and decode
// the opus audio into PCM then send it on the provided channel.
func (dd *DiscordDuplex) discordReceivePCM(ctx context.Context, wg *sync.WaitGroup, cancel context.CancelFunc) {
	var err error

	lastReady := true
	var readyTimeout *time.Timer

	wg.Add(1)

	for {
		if !dd.Bridge.DiscordVoice.Ready || dd.Bridge.DiscordVoice.OpusRecv == nil {
			if lastReady {
				OnError(fmt.Sprintf("Discordgo not to receive opus packets. %+v : %+v", dd.Bridge.DiscordVoice.Ready, dd.Bridge.DiscordVoice.OpusSend), nil)
				readyTimeout = time.AfterFunc(30*time.Second, func() {
					log.Println("set ready timeout")
					cancel()
				})
				lastReady = false
			}
			continue
		} else if !lastReady {
			fmt.Println("Discordgo ready to receive packets")
			lastReady = true
			readyTimeout.Stop()
		}

		var ok bool
		var p *discordgo.Packet

		select {
		case <-ctx.Done():
			wg.Done()
			return
		case p, ok = <-dd.Bridge.DiscordVoice.OpusRecv:
		}

		if !ok {
			log.Println("Opus not ok")
			continue
		}

		dd.discordMutex.Lock()
		_, ok = dd.fromDiscordMap[p.SSRC]
		dd.discordMutex.Unlock()
		if !ok {
			newStream := fromDiscord{}
			newStream.pcm = make(chan []int16, 100)
			newStream.streaming = false
			newStream.decoder, err = gopus.NewDecoder(48000, 1)
			if err != nil {
				OnError("error creating opus decoder", err)
				continue
			}
			dd.discordMutex.Lock()
			dd.fromDiscordMap[p.SSRC] = newStream
			dd.discordMutex.Unlock()
		}

		dd.discordMutex.Lock()
		p.PCM, err = dd.fromDiscordMap[p.SSRC].decoder.Decode(p.Opus, 960, false)
		dd.discordMutex.Unlock()
		if err != nil {
			OnError("Error decoding opus data", err)
			continue
		}
		if len(p.PCM) != 960 {
			log.Println("Opus size error")
			continue
		}

		dd.discordMutex.Lock()
		select {
		case dd.fromDiscordMap[p.SSRC].pcm <- p.PCM[0:480]:
		default:
			log.Println("fromDiscordMap buffer full. Dropping packet")
			dd.discordMutex.Unlock()
			continue
		}
		select {
		case dd.fromDiscordMap[p.SSRC].pcm <- p.PCM[480:960]:
		default:
			log.Println("fromDiscordMap buffer full. Dropping packet")
		}
		dd.discordMutex.Unlock()
	}
}

func (dd *DiscordDuplex) fromDiscordMixer(ctx context.Context, wg *sync.WaitGroup, toMumble chan<- gumble.AudioBuffer) {
	ticker := time.NewTicker(10 * time.Millisecond)
	sendAudio := false
	wg.Add(1)

	for {
		select {
		case <-ctx.Done():
			wg.Done()
			return
		case <-ticker.C:
		}

		dd.discordMutex.Lock()

		sendAudio = false
		internalMixerArr := make([][]int16, 0)

		// Work through each channel
		for i := range dd.fromDiscordMap {
			if len(dd.fromDiscordMap[i].pcm) > 0 {
				sendAudio = true
				if !dd.fromDiscordMap[i].streaming {
					x := dd.fromDiscordMap[i]
					x.streaming = true
					dd.fromDiscordMap[i] = x
				}

				x1 := (<-dd.fromDiscordMap[i].pcm)
				internalMixerArr = append(internalMixerArr, x1)
			} else {
				if dd.fromDiscordMap[i].streaming {
					x := dd.fromDiscordMap[i]
					x.streaming = false
					dd.fromDiscordMap[i] = x
				}
			}
		}

		dd.discordMutex.Unlock()

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
