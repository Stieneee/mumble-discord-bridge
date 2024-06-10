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
	mutex           sync.Mutex
	streams         []chan gumble.AudioBuffer
	mumbleSleepTick sleepct.SleepCT
}

func NewMumbleDuplex() *MumbleDuplex {
	return &MumbleDuplex{
		streams:         make([]chan gumble.AudioBuffer, 0),
		mumbleSleepTick: sleepct.SleepCT{},
	}
}

// OnAudioStream - Spawn routines to handle incoming packets
func (m *MumbleDuplex) OnAudioStream(e *gumble.AudioStreamEvent) {

	stream := make(chan gumble.AudioBuffer, 100)

	m.mutex.Lock()
	m.streams = append(m.streams, stream)
	m.mutex.Unlock()

	promMumbleArraySize.Set(float64(len(m.streams)))

	go func() {
		name := e.User.Name
		log.Println("New mumble audio stream", name)
		for p := range e.C {
			// log.Println("audio packet", p.Sender.Name, len(p.AudioBuffer))

			// 480 per 10ms
			for i := 0; i < len(p.AudioBuffer)/480; i++ {
				stream <- p.AudioBuffer[480*i : 480*(i+1)]
			}
			promReceivedMumblePackets.Inc()
			m.mumbleSleepTick.Notify()
		}
		log.Println("Mumble audio stream ended", name)
		//remove the MumbleStream from the array
		m.mutex.Lock()
		defer m.mutex.Unlock()
		for i := 0; i < len(m.streams); i++ {
			if m.streams[i] == stream {
				m.streams = append(m.streams[:i], m.streams[i+1:]...)
				break
			}
		}

		promMumbleArraySize.Set(float64(len(m.streams)))
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
		for i := 0; i < len(m.streams); i++ {
			if len(m.streams[i]) > 0 {
				sendAudio = true
				streamingCount++
				x1 := (<-m.streams[i])
				internalMixerArr = append(internalMixerArr, x1)
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
