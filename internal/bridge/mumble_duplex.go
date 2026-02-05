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

// toMumbleSender sends audio packets from Discord to Mumble's audio channel
// Uses blocking read (packets are already paced by fromDiscordMixer) and timeout send to prevent blocking
func (m *MumbleDuplex) toMumbleSender(ctx context.Context, internalChan <-chan gumble.AudioBuffer) {
	const sendTimeout = 20 * time.Millisecond // Timeout for send to gumble

	var mumbleOutgoing chan<- gumble.AudioBuffer
	var lastConnectionState bool

	sendTimer := time.NewTimer(sendTimeout)
	sendTimer.Stop()

	m.logger.Debug("MUMBLE_FORWARDER", "Starting Mumble audio forwarder")

	for {
		select {
		case <-ctx.Done():
			m.logger.Debug("MUMBLE_FORWARDER", "Stopping Mumble audio forwarder")
			sendTimer.Stop()

			return

		case packet := <-internalChan:
			// Update buffer metric
			promMumbleBufferedPackets.Set(float64(len(internalChan)))

			// Check connection state
			currentConnectionState := m.bridge.MumbleConnectionManager != nil && m.bridge.MumbleConnectionManager.IsConnected()

			if currentConnectionState != lastConnectionState {
				if currentConnectionState {
					// Connected - get new channel
					m.logger.Debug("MUMBLE_FORWARDER", "Mumble connected, getting audio channel")
					mumbleOutgoing = m.bridge.MumbleConnectionManager.GetAudioOutgoing()
				} else {
					// Disconnected
					m.logger.Debug("MUMBLE_FORWARDER", "Mumble disconnected")
					mumbleOutgoing = nil
				}
				lastConnectionState = currentConnectionState
			}

			// If not connected, sink packet
			if !currentConnectionState {
				promPacketsSunk.WithLabelValues("mumble", "inbound").Inc()

				continue
			}

			if mumbleOutgoing == nil {
				promPacketsSunk.WithLabelValues("mumble", "inbound").Inc()

				continue
			}

			// Try to send with timeout
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
				bufferSize := len(internalChan)
				if bufferSize > 50 { // Only log when significantly backed up
					m.logger.Debug("MUMBLE_FORWARDER",
						fmt.Sprintf("Send timeout, dropping packet (buffer: %d)", bufferSize))
				}
			}
		}
	}
}
