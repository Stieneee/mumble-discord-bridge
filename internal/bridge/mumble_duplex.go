package bridge

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/stieneee/gumble/gumble"
	_ "github.com/stieneee/gumble/opus" // Register opus codec
	"github.com/stieneee/mumble-discord-bridge/pkg/logger"
	"github.com/stieneee/mumble-discord-bridge/pkg/sleepct"
)

// Constants for Mumble audio handling
const (
	mumbleStreamBufferSize  = 100 // Buffer size for individual audio streams
	mumbleAudioChunkSize    = 480 // Samples per 10ms audio chunk
	mumbleMixerInterval     = 10 * time.Millisecond
	mumbleMaxDroppedPackets = 250 // Maximum dropped packets before warning reset
)

// MumbleDuplex - listener and outgoing
type MumbleDuplex struct {
	mutex           sync.Mutex
	streams         []chan gumble.AudioBuffer
	mumbleSleepTick sleepct.SleepCT
	logger          logger.Logger
	bridge          *BridgeState // Reference to bridge for connection manager access
	// Track stream cleanup for reconnections - using map for O(1) removal
	streamCleanupCallbacks map[chan gumble.AudioBuffer]func() // Protected by mutex
}

// NewMumbleDuplex creates a new Mumble audio duplex handler.
func NewMumbleDuplex(log logger.Logger, bridge *BridgeState) *MumbleDuplex {
	return &MumbleDuplex{
		streams:                make([]chan gumble.AudioBuffer, 0),
		mumbleSleepTick:        sleepct.SleepCT{},
		logger:                 log,
		bridge:                 bridge,
		streamCleanupCallbacks: make(map[chan gumble.AudioBuffer]func()),
	}
}

// CleanupStreams forcibly closes all active audio streams (used during reconnections)
func (m *MumbleDuplex) CleanupStreams() {
	m.mutex.Lock()
	defer m.mutex.Unlock()

	m.logger.Info("MUMBLE_STREAM", fmt.Sprintf("Cleaning up %d active audio streams", len(m.streams)))

	// Call all cleanup callbacks to close streams gracefully
	for _, callback := range m.streamCleanupCallbacks {
		func() {
			defer func() {
				if r := recover(); r != nil {
					m.logger.Warn("MUMBLE_STREAM", fmt.Sprintf("Panic during stream cleanup: %v", r))
				}
			}()
			callback()
		}()
	}

	// Clear all streams and callbacks
	m.streams = make([]chan gumble.AudioBuffer, 0)
	m.streamCleanupCallbacks = make(map[chan gumble.AudioBuffer]func())

	promMumbleArraySize.Set(0)
	m.logger.Info("MUMBLE_STREAM", "Audio stream cleanup completed")
}

// OnAudioStream - Spawn routines to handle incoming packets with improved cleanup
func (m *MumbleDuplex) OnAudioStream(e *gumble.AudioStreamEvent) {
	stream := make(chan gumble.AudioBuffer, mumbleStreamBufferSize)
	streamClosed := false
	streamMutex := sync.Mutex{}

	m.mutex.Lock()
	m.streams = append(m.streams, stream)
	// Add cleanup callback for this stream using map for O(1) access
	m.streamCleanupCallbacks[stream] = func() {
		streamMutex.Lock()
		defer streamMutex.Unlock()
		if !streamClosed {
			close(stream)
			streamClosed = true
			m.logger.Debug("MUMBLE_STREAM", fmt.Sprintf("Forcibly closed stream for user: %s during cleanup", e.User.Name))
		}
	}
	m.mutex.Unlock()

	promMumbleArraySize.Set(float64(len(m.streams)))

	go func() {
		name := e.User.Name
		m.logger.Info("MUMBLE_STREAM", fmt.Sprintf("New mumble audio stream: %s", name))
		defer func() {
			if r := recover(); r != nil {
				m.logger.Error("MUMBLE_STREAM", fmt.Sprintf("Panic in audio stream for %s: %v", name, r))
			}
		}()

		for p := range e.C {
			// Hold lock once per packet instead of per chunk to reduce overhead
			streamMutex.Lock()
			if streamClosed {
				streamMutex.Unlock()

				break
			}

			// Process all audio chunks while holding lock
			for i := 0; i < len(p.AudioBuffer)/mumbleAudioChunkSize; i++ {
				start := mumbleAudioChunkSize * i
				end := mumbleAudioChunkSize * (i + 1)
				// Non-blocking send to avoid hanging on closed stream
				select {
				case stream <- p.AudioBuffer[start:end]:
				default:
					// Stream buffer full, drop packet
					m.logger.Debug("MUMBLE_STREAM", fmt.Sprintf("Stream buffer full for %s, dropping packet", name))
				}
			}
			streamMutex.Unlock()

			promReceivedMumblePackets.Inc()
			m.mumbleSleepTick.Notify()
		}

		m.logger.Info("MUMBLE_STREAM", fmt.Sprintf("Mumble audio stream ended: %s", name))

		// Cleanup stream from arrays
		m.mutex.Lock()
		defer m.mutex.Unlock()

		// Close stream safely
		streamMutex.Lock()
		if !streamClosed {
			close(stream)
			streamClosed = true
		}
		streamMutex.Unlock()

		// Remove stream from array
		for i := 0; i < len(m.streams); i++ {
			if m.streams[i] == stream {
				m.streams = append(m.streams[:i], m.streams[i+1:]...)

				break
			}
		}

		// Remove cleanup callback using map for O(1) removal
		delete(m.streamCleanupCallbacks, stream)

		promMumbleArraySize.Set(float64(len(m.streams)))
	}()
}

func (m *MumbleDuplex) fromMumbleMixer(ctx context.Context, toDiscord chan []int16) {
	m.mumbleSleepTick.Start(mumbleMixerInterval)

	sendAudio := false

	droppingPackets := false
	droppingPacketCount := 0

	for {
		select {
		case <-ctx.Done():
			m.logger.Info("MUMBLE_MIXER", "Stopping From Mumble Mixer")

			return
		default:
		}

		promTimerMumbleMixer.Observe(float64(m.mumbleSleepTick.SleepNextTarget(ctx, false)))

		m.mutex.Lock()

		sendAudio = false
		internalMixerArr := make([]gumble.AudioBuffer, 0)
		streamingCount := 0

		// Work through each stream
		for i := range m.streams {
			if len(m.streams[i]) > 0 {
				sendAudio = true
				streamingCount++
				audioData := <-m.streams[i]
				internalMixerArr = append(internalMixerArr, audioData)
			}
		}

		m.mutex.Unlock()

		promMumbleStreaming.Set(float64(streamingCount))

		if sendAudio {
			outBuf := make([]int16, mumbleAudioChunkSize)

			// Mix audio from all active streams
			for i := range outBuf {
				for _, audioData := range internalMixerArr {
					outBuf[i] += audioData[i]
				}
			}

			// Always try to send to Discord - let Discord side handle its own connection state
			promToDiscordBufferSize.Set(float64(len(toDiscord)))
			select {
			case toDiscord <- outBuf:
				if droppingPackets {
					m.logger.Info("MUMBLE_MIXER", fmt.Sprintf("Discord buffer ok, total packets dropped: %d", droppingPacketCount))
					droppingPackets = false
				}
			default:
				if !droppingPackets {
					m.logger.Warn("MUMBLE_MIXER", "toDiscord buffer full. Dropping packets")
					droppingPackets = true
					droppingPacketCount = 0
				}
				droppingPacketCount++
				promToDiscordDropped.Inc()
				// Don't cancel the entire bridge for Discord buffer issues in managed mode
				if droppingPacketCount > mumbleMaxDroppedPackets {
					m.logger.Warn("MUMBLE_MIXER", "Discord buffer overflowing, packets will be sunk")
					droppingPacketCount = 0 // Reset to avoid spam
				}
			}
		}
	}
}

// toMumbleSender sends audio packets from Discord to Mumble's audio channel.
// Detects Mumble reconnections by comparing the cached audio channel pointer against
// MumbleConnectionManager.GetAudioOutgoing(). The pointer changes when a new gumble
// client is created, so even if Mumble reconnects while this goroutine is blocked
// waiting for Discord packets, the stale channel is detected on the next packet.
func (m *MumbleDuplex) toMumbleSender(ctx context.Context, internalChan <-chan gumble.AudioBuffer) {
	const sendTimeout = 20 * time.Millisecond

	var mumbleOutgoing chan<- gumble.AudioBuffer

	sendTimer := time.NewTimer(sendTimeout)
	sendTimer.Stop()

	// closeOldChannel safely closes a stale gumble AudioOutgoing channel,
	// allowing gumble's goroutine to exit cleanly.
	closeOldChannel := func(ch chan<- gumble.AudioBuffer) {
		if ch == nil {
			return
		}
		defer func() {
			if r := recover(); r != nil {
				m.logger.Warn("MUMBLE_FORWARDER", fmt.Sprintf("Panic closing old audio channel: %v", r))
			}
		}()
		close(ch)
	}

	for {
		select {
		case <-ctx.Done():
			sendTimer.Stop()
			closeOldChannel(mumbleOutgoing)

			return

		case packet := <-internalChan:
			promMumbleBufferedPackets.Set(float64(len(internalChan)))

			// Get current audio channel from connection manager.
			// The pointer is cached per-client, so a different pointer means reconnection.
			var currentOutgoing chan<- gumble.AudioBuffer
			if m.bridge.MumbleConnectionManager != nil {
				currentOutgoing = m.bridge.MumbleConnectionManager.GetAudioOutgoing()
			}

			// Detect channel change: reconnection, disconnect, or first connect
			if currentOutgoing != mumbleOutgoing {
				if mumbleOutgoing != nil && currentOutgoing != nil {
					closeOldChannel(mumbleOutgoing)
					m.logger.Info("MUMBLE_FORWARDER", "Mumble reconnected, refreshed audio channel")
				} else if currentOutgoing == nil {
					closeOldChannel(mumbleOutgoing)
					m.logger.Info("MUMBLE_FORWARDER", "Mumble disconnected")
				} else {
					m.logger.Info("MUMBLE_FORWARDER", "Mumble connected, got audio channel")
				}
				mumbleOutgoing = currentOutgoing
			}

			if mumbleOutgoing == nil {
				promPacketsSunk.WithLabelValues("mumble", "inbound").Inc()

				continue
			}

			sendTimer.Reset(sendTimeout)
			select {
			case mumbleOutgoing <- packet:
				if !sendTimer.Stop() {
					<-sendTimer.C
				}
				promSentMumblePackets.Inc()
			case <-sendTimer.C:
				promMumbleSendTimeouts.Inc()
				promToMumbleDropped.Inc()
			}
		}
	}
}
