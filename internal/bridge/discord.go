package bridge

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stieneee/gopus"
	"github.com/stieneee/gumble/gumble"
	"github.com/stieneee/mumble-discord-bridge/pkg/sleepct"
)

type fromDiscord struct {
	decoder       *gopus.Decoder
	pcm           chan []int16
	receiving     bool // is used to to track the assumption that we are streaming a continuos stream form discord
	streaming     bool // The buffer streaming is streaming out
	lastSequence  uint16
	lastTimeStamp uint32
}

// DiscordDuplex Handle discord voice stream
type DiscordDuplex struct {
	Bridge *BridgeState

	discordMutex            sync.Mutex
	fromDiscordMap          map[uint32]fromDiscord
	discordSendSleepTick    sleepct.SleepCT
	discordReceiveSleepTick sleepct.SleepCT
}

func NewDiscordDuplex(b *BridgeState) *DiscordDuplex {
	return &DiscordDuplex{
		Bridge:                  b,
		fromDiscordMap:          make(map[uint32]fromDiscord),
		discordSendSleepTick:    sleepct.SleepCT{},
		discordReceiveSleepTick: sleepct.SleepCT{},
	}
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
// received PCM data with Opus then send that to Discordgo
func (dd *DiscordDuplex) discordSendPCM(ctx context.Context, cancel context.CancelFunc, pcm <-chan []int16) {
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

	dd.discordSendSleepTick.Start(20 * time.Millisecond)

	lastReady := true
	var readyTimeout *time.Timer
	var speakingStart time.Time

	// Spy on the PCM channel to notify
	// TODO determine a method to notify a paused sleepct
	// pcm := make(chan []int16, 10)
	// go func() {
	// 	for {
	// 		t, ok := <-pcmIn
	// 		if !ok {
	// 			close(pcm)
	// 			return
	// 		} else {
	// 			dd.discordSendSleepTick.Notify()
	// 			pcm <- t
	// 		}
	// 	}
	// }()

	internalSend := func(opus []byte) {
		dd.Bridge.DiscordVoice.RLock()
		if !dd.Bridge.DiscordVoice.Ready || dd.Bridge.DiscordVoice.OpusSend == nil {
			if lastReady {
				OnError(fmt.Sprintf("Discordgo not ready for opus packets. %+v : %+v", dd.Bridge.DiscordVoice.Ready, dd.Bridge.DiscordVoice.OpusSend), nil)
				readyTimeout = time.AfterFunc(30*time.Second, func() {
					log.Println("Debug: Set ready timeout")
					cancel()
				})
				lastReady = false
			}
		} else if !lastReady {
			fmt.Println("Discordgo ready to send opus packets")
			lastReady = true
			readyTimeout.Stop()
		} else {
			select {
			case dd.Bridge.DiscordVoice.OpusSend <- opus:
			case <-ctx.Done():
			}

			promDiscordSentPackets.Inc()
		}
		dd.Bridge.DiscordVoice.RUnlock()
	}

	defer log.Println("Stopping Discord send PCM")

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		// if we are not streaming try to pause
		// promTimerDiscordSend.Observe(float64(dd.discordSendSleepTick.SleepNextTarget(ctx, !streaming)))
		promTimerDiscordSend.Observe(float64(dd.discordSendSleepTick.SleepNextTarget(ctx, false)))

		if (len(pcm) > 1 && streaming) || (len(pcm) > dd.Bridge.BridgeConfig.DiscordStartStreamingCount && !streaming) {
			if !streaming {
				speakingStart = time.Now()
				done := make(chan bool, 1)
				go func() {
					// This call will prevent discordSendPCM from exiting if the discord connection is lost
					if err := dd.Bridge.DiscordVoice.Speaking(true); err != nil {
						log.Println("Error setting speaking status to true:", err)
					}
					done <- true
				}()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
					fmt.Println("Discord speaking timeout :(")
					cancel()
					return
				case <-ctx.Done():
					return
				}
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
				// Multiple short cycle delays can result in a discord rate limiter being trigger due to of multiple JSON speaking/not-speaking state changes
				if time.Since(speakingStart).Milliseconds() < 50 {
					log.Println("Warning: Short Mumble to Discord speaking cycle. Consider increaseing the size of the to Discord jitter buffer.")
				}

				// Send silence as suggested by Discord Documentation.
				// We want to do this after alerting the user of possible short speaking cycles
				for i := 0; i < 5; i++ {
					internalSend(opusSilence)
					// promTimerDiscordSend.Observe(float64(dd.discordSendSleepTick.SleepNextTarget(ctx, true)))
					promTimerDiscordSend.Observe(float64(dd.discordSendSleepTick.SleepNextTarget(ctx, false)))

				}

				if err := dd.Bridge.DiscordVoice.Speaking(false); err != nil {
					log.Println("Error setting speaking status to false:", err)
				}
				streaming = false
			}
		}
	}
}

// ReceivePCM will receive on the the Discordgo OpusRecv channel and decode
// the opus audio into PCM then send it on the provided channel.
func (dd *DiscordDuplex) discordReceivePCM(ctx context.Context, cancel context.CancelFunc) {
	var err error

	lastReady := true
	var readyTimeout *time.Timer

	var zeros [480]int16
	for i := 0; i < 480; i++ {
		zeros[i] = 0
	}

	for {
		dd.Bridge.DiscordVoice.RLock()
		if !dd.Bridge.DiscordVoice.Ready || dd.Bridge.DiscordVoice.OpusRecv == nil {
			if lastReady {
				OnError(fmt.Sprintf("Discordgo not to receive opus packets. %+v : %+v", dd.Bridge.DiscordVoice.Ready, dd.Bridge.DiscordVoice.OpusSend), nil)
				readyTimeout = time.AfterFunc(30*time.Second, func() {
					log.Println("Debug: Set ready timeout")
					cancel()
				})
				lastReady = false
			}
			dd.Bridge.DiscordVoice.RUnlock()
			continue
		} else if !lastReady {
			fmt.Println("Discordgo ready to receive packets")
			lastReady = true
			readyTimeout.Stop()
		}
		dd.Bridge.DiscordVoice.RUnlock()

		var ok bool
		var p *discordgo.Packet

		select {
		case <-ctx.Done():
			log.Println("Stopping Discord receive PCM")
			return
		case p, ok = <-dd.Bridge.DiscordVoice.OpusRecv:
		}

		if !ok {
			log.Println("Opus not ok")
			continue
		}

		dd.discordMutex.Lock()

		_, ok = dd.fromDiscordMap[p.SSRC]
		if !ok {
			newStream := fromDiscord{}
			newStream.pcm = make(chan []int16, 100)
			newStream.receiving = false
			newStream.streaming = false
			newStream.decoder, err = gopus.NewDecoder(48000, 1) // Decode into mono
			if err != nil {
				OnError("error creating opus decoder", err)
				dd.discordMutex.Unlock()
				continue
			}

			dd.fromDiscordMap[p.SSRC] = newStream
		}

		s := dd.fromDiscordMap[p.SSRC]

		deltaT := int(p.Timestamp - s.lastTimeStamp)
		if p.Sequence-s.lastSequence != 1 {
			s.decoder.ResetState()
		}

		// oldReceiving := s.receiving

		if !s.receiving || deltaT < 1 || deltaT > 960*10 {
			// First packet assume deltaT
			// fmt.Println("replacing", deltaT, 960)
			deltaT = 960
			s.receiving = true
		}

		s.lastTimeStamp = p.Timestamp
		s.lastSequence = p.Sequence

		dd.fromDiscordMap[p.SSRC] = s
		dd.discordMutex.Unlock()

		p.PCM, err = s.decoder.Decode(p.Opus, deltaT, false)
		if err != nil {
			OnError("Error decoding opus data", err)
			continue
		}

		// fmt.Println(p.SSRC, p.Type, deltaT, p.Sequence, p.Sequence-s.lastSequence, oldReceiving, s.streaming, len(p.Opus), len(p.PCM))

		promDiscordReceivedPackets.Inc()

		// Push data into pcm channel in 10ms chunks of mono pcm data
		dd.discordMutex.Lock()
		for l := 0; l < len(p.PCM); l = l + 480 {
			var next []int16
			u := l + 480

			next = p.PCM[l:u]

			select {
			case dd.fromDiscordMap[p.SSRC].pcm <- next:
			default:
				log.Println("From Discord buffer full. Dropping packet")
			}
		}
		dd.discordMutex.Unlock()

		dd.discordReceiveSleepTick.Notify()
	}
}

func (dd *DiscordDuplex) fromDiscordMixer(ctx context.Context, toMumble chan<- gumble.AudioBuffer) {
	mumbleSilence := gumble.AudioBuffer{}
	for i := 3; i < 480; i++ {
		mumbleSilence = append(mumbleSilence, 0x00)
	}
	var speakingStart time.Time

	dd.discordReceiveSleepTick.Start(10 * time.Millisecond)

	sendAudio := false
	toMumbleStreaming := false

	for {
		select {
		case <-ctx.Done():
			log.Println("Stopping from Discord mixer")
			return
		default:
		}

		// if didn't send audio try to pause
		// promTimerDiscordMixer.Observe(float64(dd.discordReceiveSleepTick.SleepNextTarget(ctx, !sendAudio)))
		// TODO Additional pause testing
		promTimerDiscordMixer.Observe(float64(dd.discordReceiveSleepTick.SleepNextTarget(ctx, false)))

		dd.discordMutex.Lock()

		sendAudio = false
		internalMixerArr := make([][]int16, 0)
		streamingCount := 0

		// Work through each channel
		for i := range dd.fromDiscordMap {
			bufferLength := len(dd.fromDiscordMap[i].pcm)
			isStreaming := dd.fromDiscordMap[i].streaming
			if (bufferLength > 0 && isStreaming) || (bufferLength > dd.Bridge.BridgeConfig.MumbleStartStreamCount && !isStreaming) {
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

				streamingCount++
				x1 := (<-dd.fromDiscordMap[i].pcm)
				internalMixerArr = append(internalMixerArr, x1)
			} else {
				if dd.fromDiscordMap[i].streaming {
					x := dd.fromDiscordMap[i]
					x.streaming = false
					x.receiving = false // toggle this here is not optimal but there is no better location atm.
					dd.fromDiscordMap[i] = x
				}
			}
		}

		promDiscordArraySize.Set(float64(len(dd.fromDiscordMap)))
		promDiscordStreaming.Set(float64(streamingCount))

		dd.discordMutex.Unlock()

		mumbleTimeoutSend := func(outBuf []int16) {
			timeout := make(chan bool, 1)
			go func() {
				time.Sleep(10 * time.Millisecond)
				timeout <- true
			}()

			select {
			case toMumble <- outBuf:
				promSentMumblePackets.Inc()
			case <-timeout:
				log.Println("To Mumble timeout. Dropping packet")
				promToMumbleDropped.Inc()
			}
		}

		if sendAudio {
			// Regular send mixed audio
			outBuf := make([]int16, 480)

			for j := 0; j < len(internalMixerArr); j++ {
				for i := 0; i < len(internalMixerArr[j]); i++ {
					outBuf[i] += (internalMixerArr[j])[i]
				}
			}

			mumbleTimeoutSend(outBuf)
		} else if !sendAudio && toMumbleStreaming {
			// Send opus silence to mumble
			// See note above about jitter buffer warning
			if time.Since(speakingStart).Milliseconds() < 50 {
				log.Println("Warning: Short Discord to Mumble speaking cycle. Consider increaseing the size of the to Mumble jitter buffer.", time.Since(speakingStart).Milliseconds())
			}

			for i := 0; i < 5; i++ {
				mumbleTimeoutSend(mumbleSilence)
				promTimerDiscordMixer.Observe(float64(dd.discordReceiveSleepTick.SleepNextTarget(ctx, false)))
			}

			toMumbleStreaming = false
		}
	}
}
