package main

import (
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
		log.Println("new mumble audio stream", e.User.Name)
		for {
			select {
			case p := <-e.C:
				// log.Println("audio packet", p.Sender.Name, len(p.AudioBuffer))

				// 480 per 10ms
				for i := 0; i < len(p.AudioBuffer)/480; i++ {
					localMumbleArray <- p.AudioBuffer[480*i : 480*(i+1)]
				}
			}
		}
	}()
	return
}

func (m MumbleDuplex) fromMumbleMixer(toDiscord chan []int16) {
	ticker := time.NewTicker(10 * time.Millisecond)
	sendAudio := false

	for {
		<-ticker.C

		mutex.Lock()

		sendAudio = false
		internalMixerArr := make([]gumble.AudioBuffer, 0)

		// Work through each channel
		for i := 0; i < len(fromMumbleArr); i++ {
			if len(fromMumbleArr[i]) > 0 {
				sendAudio = true
				if mumbleStreamingArr[i] == false {
					mumbleStreamingArr[i] = true
					// log.Println("mumble starting", i)
				}

				x1 := (<-fromMumbleArr[i])
				internalMixerArr = append(internalMixerArr, x1)
			} else {
				if mumbleStreamingArr[i] == true {
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

		if sendAudio {
			toDiscord <- outBuf
		}
	}
}

func (m MumbleDuplex) sendToMumble(mumble *gumble.Client, toMumble chan gumble.AudioBuffer) {
	streaming := false
	ticker := time.NewTicker(10 * time.Millisecond)

	send := mumble.AudioOutgoing()

	for {
		<-ticker.C

		if len(toMumble) > 0 {
			streaming = true

			pcm := <-toMumble

			// gain up
			for i := range pcm {
				pcm[i] = int16((float64(pcm[i]) * 8))
			}

			send <- pcm
		} else {
			if streaming {
				streaming = false
			}

		}
	}
}
