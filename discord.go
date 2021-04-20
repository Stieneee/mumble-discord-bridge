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

	discordMutex   sync.Mutex
	fromDiscordMap map[uint32]fromDiscord
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

	// Generate Opus Silence Frame
	opusSilence := []byte{0xf8, 0xff, 0xfe}
	for i := 3; i < frameSize; i++ {
		opusSilence = append(opusSilence, 0x00)
	}

	// ticker := NewTickerCT(20 * time.Millisecond)
	sleepTick := SleepCT{
		d: 20 * time.Millisecond,
		t: time.Now(),
	}

	lastReady := true
	var readyTimeout *time.Timer
	var speakingStart time.Time

	wg.Add(1)

	internalSend := func(opus []byte) {
		dd.Bridge.DiscordVoice.RWMutex.RLock()
		if !dd.Bridge.DiscordVoice.Ready || dd.Bridge.DiscordVoice.OpusSend == nil {
			if lastReady {
				OnError(fmt.Sprintf("Discordgo not ready for opus packets. %+v : %+v", dd.Bridge.DiscordVoice.Ready, dd.Bridge.DiscordVoice.OpusSend), nil)
				readyTimeout = time.AfterFunc(30*time.Second, func() {
					log.Println("set ready timeout")
					cancel()
				})
				lastReady = false
			}
		} else if !lastReady {
			fmt.Println("Discordgo ready to send opus packets")
			lastReady = true
			readyTimeout.Stop()
		} else {
			dd.Bridge.DiscordVoice.OpusSend <- opus
		}
		dd.Bridge.DiscordVoice.RWMutex.RUnlock()
	}

	for {
		select {
		case <-ctx.Done():
			wg.Done()
			return
		default:
		}

		sleepTick.SleepNextTarget()

		if (len(pcm) > 1 && streaming) || (len(pcm) > dd.Bridge.BridgeConfig.DiscordStartStreamingCount && !streaming) {
			if !streaming {
				speakingStart = time.Now()
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

			internalSend(opus)

		} else {
			if streaming {
				// Check to see if there is a short speaking cycle.
				// It is possible that short speaking cycle is the result of a short input to mumble (Not a problem). ie a quick tap of push to talk button.
				// Or when timing delays are introduced via network, hardware or kernel delays (Problem).
				// The problem delays result in choppy or stuttering sounds, especially when the silence frames are introduced into the opus frames below.
				// Multiple short cycle delays can result in a Discrod rate limiter being trigger due to of multiple JSON speaking/not-speaking state changes
				if time.Since(speakingStart).Milliseconds() < 100 {
					log.Println("Warning: Short Mumble to Discord speaking cycle. Consider increaseing the size of the to Discord jitter buffer.")
				}

				// Send silence as suggested by Discord Documentation.
				// We want to do this after alerting the user of possible short speaking cycles
				for i := 0; i < 5; i++ {
					internalSend(opusSilence)
					sleepTick.SleepNextTarget()
				}

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
		dd.Bridge.DiscordVoice.RWMutex.RLock()
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
		dd.Bridge.DiscordVoice.RWMutex.RUnlock()

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
	mumbleSilence := gumble.AudioBuffer{}
	for i := 3; i < 480; i++ {
		mumbleSilence = append(mumbleSilence, 0x00)
	}
	var speakingStart time.Time
	sleepTick := SleepCT{
		d: 10 * time.Millisecond,
		t: time.Now(),
	}
	sendAudio := false
	toMumbleStreaming := false
	wg.Add(1)

	for {
		select {
		case <-ctx.Done():
			wg.Done()
			return
		default:
		}

		sleepTick.SleepNextTarget()

		dd.discordMutex.Lock()

		sendAudio = false
		internalMixerArr := make([][]int16, 0)

		// Work through each channel
		for i := range dd.fromDiscordMap {
			bufferLength := len(dd.fromDiscordMap[i].pcm)
			isStreaming := dd.fromDiscordMap[i].streaming
			if (bufferLength > 0 && isStreaming) || (bufferLength > dd.Bridge.BridgeConfig.mumbleStartStreamCount && !isStreaming) {
				if !toMumbleStreaming {
					speakingStart = time.Now()
					toMumbleStreaming = true
				}
				sendAudio = true

				if !isStreaming {
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

		mumbleTimeoutSend := func(outBuf []int16) {
			timeout := make(chan bool, 1)
			go func() {
				time.Sleep(10 * time.Millisecond)
				timeout <- true
			}()

			select {
			case toMumble <- outBuf:
			case <-timeout:
				log.Println("toMumble timeout. Dropping packet")
			}
		}

		if sendAudio {
			// Regular send mixed audio
			outBuf := make([]int16, 480)

			for i := 0; i < len(outBuf); i++ {
				for j := 0; j < len(internalMixerArr); j++ {
					outBuf[i] += (internalMixerArr[j])[i]
				}
			}

			mumbleTimeoutSend(outBuf)
		} else if !sendAudio && toMumbleStreaming {
			// Send opus silence to mumble
			// See note above about jitter buffer warning
			if time.Since(speakingStart).Milliseconds() < 100 {
				log.Println("Warning: Short Discord to Mumble speaking cycle. Consider increaseing the size of the to Mumble jitter buffer.")
			}

			for i := 0; i < 5; i++ {
				mumbleTimeoutSend(mumbleSilence)
				sleepTick.SleepNextTarget()
			}

			toMumbleStreaming = false
		}
	}
}
