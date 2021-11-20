package bridge

import (
	"context"
	"log"
	"strconv"
	"sync"
	"time"

	"github.com/stieneee/gumble/gumble"
	_ "github.com/stieneee/gumble/opus"
	"github.com/stieneee/mumble-discord-bridge/pkg/sleepct"
)

// MumbleDuplex - listener and outgoing
type MumbleDuplex struct {
	mutex              sync.Mutex
	fromMumbleArr      []chan gumble.AudioBuffer
	mumbleStreamingArr []bool
	mumbleSleepTick    sleepct.SleepCT
}

func NewMumbleDuplex() *MumbleDuplex {
	return &MumbleDuplex{
		fromMumbleArr:      make([]chan gumble.AudioBuffer, 0),
		mumbleStreamingArr: make([]bool, 0),
		mumbleSleepTick:    sleepct.SleepCT{},
	}
}

// OnAudioStream - Spawn routines to handle incoming packets
func (m *MumbleDuplex) OnAudioStream(e *gumble.AudioStreamEvent) {

	// hold a reference ot the channel in the closure
	streamChan := make(chan gumble.AudioBuffer, 100)

	m.mutex.Lock()
	m.fromMumbleArr = append(m.fromMumbleArr, streamChan)
	m.mumbleStreamingArr = append(m.mumbleStreamingArr, false)
	m.mutex.Unlock()

	promMumbleArraySize.Set(float64(len(m.fromMumbleArr)))

	go func() {
		name := e.User.Name
		log.Println("New mumble audio stream", name)
		for p := range e.C {
			// log.Println("audio packet", p.Sender.Name, len(p.AudioBuffer))

			// 480 per 10ms
			for i := 0; i < len(p.AudioBuffer)/480; i++ {
				streamChan <- p.AudioBuffer[480*i : 480*(i+1)]
			}
			promReceivedMumblePackets.Inc()
			m.mumbleSleepTick.Notify()
		}
		log.Println("Mumble audio stream ended", name)
	}()
}

func (m *MumbleDuplex) fromMumbleMixer(ctx context.Context, cancel context.CancelFunc, toDiscord chan []int16) {
	m.mumbleSleepTick.Start(10 * time.Millisecond)

	sendAudio := false

	droppingPackets := false
	droppingPacketCount := 0

	for {
		select {
		case <-ctx.Done():
			log.Println("Stopping From Mumble Mixer")
			return
		default:
		}

		promTimerMumbleMixer.Observe(float64(m.mumbleSleepTick.SleepNextTarget(ctx, false)))

		m.mutex.Lock()

		sendAudio = false
		internalMixerArr := make([]gumble.AudioBuffer, 0)
		streamingCount := 0

		// Work through each channel
		for i := 0; i < len(m.fromMumbleArr); i++ {
			if len(m.fromMumbleArr[i]) > 0 {
				sendAudio = true
				if !m.mumbleStreamingArr[i] {
					m.mumbleStreamingArr[i] = true
					streamingCount++
					// log.Println("Mumble starting", i)
				}

				x1 := (<-m.fromMumbleArr[i])
				internalMixerArr = append(internalMixerArr, x1)
			} else {
				if m.mumbleStreamingArr[i] {
					m.mumbleStreamingArr[i] = false
					// log.Println("Mumble stopping", i)
				}
			}
		}

		m.mutex.Unlock()

		promMumbleStreaming.Set(float64(streamingCount))

		if sendAudio {

			outBuf := make([]int16, 480)

			for i := 0; i < len(outBuf); i++ {
				for j := 0; j < len(internalMixerArr); j++ {
					outBuf[i] += (internalMixerArr[j])[i]
				}
			}

			promToDiscordBufferSize.Set(float64(len(toDiscord)))
			select {
			case toDiscord <- outBuf:
				{
					if droppingPackets {
						log.Println("Discord buffer ok, total packets dropped " + strconv.Itoa(droppingPacketCount))
						droppingPackets = false
					}
				}
			default:
				if !droppingPackets {
					log.Println("Error: toDiscord buffer full. Dropping packets")
					droppingPackets = true
					droppingPacketCount = 0
				}
				droppingPacketCount++
				promToDiscordDropped.Inc()
				if droppingPacketCount > 250 {
					log.Println("Discord Timeout")
					cancel()
				}
			}
		}
	}
}
