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
type MumbleDuplex struct {
	Close chan bool
}

func MumbleReset() {
	fromMumbleArr = []chan gumble.AudioBuffer{}
	mumbleStreamingArr = []bool{}
}

// OnAudioStream - Spawn routines to handle incoming packets
func (m MumbleDuplex) OnAudioStream(e *gumble.AudioStreamEvent) {

	// hold a reference ot the channel in the closure
	localMumbleArray := make(chan gumble.AudioBuffer, 100)

	mutex.Lock()
	fromMumbleArr = append(fromMumbleArr, localMumbleArray)
	mumbleStreamingArr = append(mumbleStreamingArr, false)
	mutex.Unlock()

	go func(die chan bool) {
		log.Println("new mumble audio stream", e.User.Name)
		for {
			select {
			default:
			case <-die:
				log.Println("Removing mumble audio stream")
				return
			case p := <-e.C:
				// log.Println("audio packet", p.Sender.Name, len(p.AudioBuffer))

				// 480 per 10ms
				for i := 0; i < len(p.AudioBuffer)/480; i++ {
					localMumbleArray <- p.AudioBuffer[480*i : 480*(i+1)]
				}
			}
		}
	}(m.Close)
	return
}

func (m MumbleDuplex) fromMumbleMixer(toDiscord chan []int16, die chan bool) {
	ticker := time.NewTicker(10 * time.Millisecond)
	sendAudio := false

	for {
		select {
		case <-die:
			log.Println("Killing fromMumbleMixer")
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
			select {
			case toDiscord <- outBuf:
			case <-die:
				log.Println("Killing fromMumbleMixer")
				return
			default:
				log.Println("toDiscord buffer full. Dropping packet")
			}
		}
	}
}
