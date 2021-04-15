package main

import (
	"context"
	"log"
	"sync"
	"time"

	"layeh.com/gumble/gumble"
	_ "layeh.com/gumble/opus"
)

var mutex sync.Mutex
var fromMumbleArr []chan gumble.AudioBuffer
var mumbleStreamingArr []bool

// MumbleDuplex - listenera and outgoing
type MumbleDuplex struct{}

// OnAudioStream - Spawn routines to handle incoming packets
func (m MumbleDuplex) OnAudioStream(e *gumble.AudioStreamEvent) {

	// hold a reference ot the channel in the closure
	localMumbleArray := make(chan gumble.AudioBuffer, 100)

	mutex.Lock()
	fromMumbleArr = append(fromMumbleArr, localMumbleArray)
	mumbleStreamingArr = append(mumbleStreamingArr, false)
	mutex.Unlock()

	go func() {
		name := e.User.Name
		log.Println("new mumble audio stream", name)
		for p := range e.C {
			// log.Println("audio packet", p.Sender.Name, len(p.AudioBuffer))

			// 480 per 10ms
			for i := 0; i < len(p.AudioBuffer)/480; i++ {
				localMumbleArray <- p.AudioBuffer[480*i : 480*(i+1)]
			}
		}
		log.Println("mumble audio stream ended", name)
	}()
}

func (m MumbleDuplex) fromMumbleMixer(ctx context.Context, wg *sync.WaitGroup, toDiscord chan []int16) {
	ticker := NewTickerCT(10 * time.Millisecond)
	sendAudio := false
	bufferWarning := false

	wg.Add(1)

	for {
		select {
		case <-ctx.Done():
			wg.Done()
			return
		default:
		}

		<-ticker.C

		mutex.Lock()

		sendAudio = false
		internalMixerArr := make([]gumble.AudioBuffer, 0)

		// Work through each channel
		for i := 0; i < len(fromMumbleArr); i++ {
			if len(fromMumbleArr[i]) > 0 {
				sendAudio = true
				if !mumbleStreamingArr[i] {
					mumbleStreamingArr[i] = true
					// log.Println("mumble starting", i)
				}

				x1 := (<-fromMumbleArr[i])
				internalMixerArr = append(internalMixerArr, x1)
			} else {
				if mumbleStreamingArr[i] {
					mumbleStreamingArr[i] = false
					// log.Println("mumble stopping", i)
				}
			}
		}

		mutex.Unlock()

		outBuf := make([]int16, 480)

		for i := 0; i < len(outBuf); i++ {
			for j := 0; j < len(internalMixerArr); j++ {
				outBuf[i] += (internalMixerArr[j])[i]
			}
		}

		if len(toDiscord) > 20 {
			if !bufferWarning {
				log.Println("Warning: toDiscord buffer size")
				bufferWarning = true
			}
		} else {
			if bufferWarning {
				log.Println("Resolved: toDiscord buffer size")
				bufferWarning = false
			}
		}

		if sendAudio {
			select {
			case toDiscord <- outBuf:
			default:
				log.Println("Error: toDiscord buffer full. Dropping packet")
			}
		}
	}
}
