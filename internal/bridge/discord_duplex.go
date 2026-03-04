package bridge

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/stieneee/gopus"
	"github.com/stieneee/gumble/gumble"
	"github.com/stieneee/mumble-discord-bridge/internal/discord"
	"github.com/stieneee/mumble-discord-bridge/pkg/sleepct"
)

// Constants for Discord audio handling
const (
	silenceFrameCount       = 3   // Number of silence frames to send after speaking
	shortSpeakingThreshold  = 50  // Milliseconds - threshold for short speaking cycle warning
	noDataGraceTicks        = 3   // Ticks (30ms) to wait for more audio before declaring end-of-speech
	connectionCheckInterval = 100 // Milliseconds - sleep time when connection not ready
	maxPLCPackets           = 10  // Maximum PLC frames to generate (prevents runaway on major discontinuity)
	opusFrameSize           = 960 // Standard opus frame size (20ms at 48kHz)
	pcmChunkSize            = 480 // PCM chunk size for 10ms at 48kHz sample rate
)

// sequenceGap calculates the number of lost packets between two sequence numbers,
// handling uint16 wrap-around correctly.
func sequenceGap(current, last uint16) int {
	gap := int(current) - int(last)
	if gap < 0 {
		gap += 65536 // Handle wrap-around
	}

	return gap - 1 // gap of 1 means 0 lost packets
}

type fromDiscord struct {
	decoder       *gopus.Decoder
	pcm           chan []int16
	receiving     bool // is used to to track the assumption that we are streaming a continuous stream form discord
	streaming     bool // The buffer streaming is streaming out
	lastSequence  uint16
	lastTimeStamp uint32
	lastActivity  time.Time // Track last activity for cleanup
}

// DiscordDuplex Handle discord voice stream
type DiscordDuplex struct {
	Bridge *BridgeState

	discordMutex            sync.Mutex
	fromDiscordMap          map[uint32]fromDiscord
	discordSendSleepTick    sleepct.SleepCT
	discordReceiveSleepTick sleepct.SleepCT
	cleanupCancel           context.CancelFunc // Cancel function for cleanup goroutine
}

// NewDiscordDuplex creates a new Discord audio duplex handler.
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
	// OnError function still uses log for compatibility
	// but individual bridge instances will use their own logger
	prefix := "dgVoice: " + str

	if err != nil {
		log.Println(prefix + ": " + err.Error())
	} else {
		log.Println(prefix)
	}
}

// toDiscordSender mixes Mumble audio inline, encodes with Opus, and sends to Discord.
// Runs at 10ms intervals (matching Mumble's chunk size), accumulates 2 chunks (20ms)
// before encoding an Opus frame.
func (dd *DiscordDuplex) toDiscordSender(ctx context.Context) {
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

	dd.discordSendSleepTick.Start(10 * time.Millisecond)

	lastReady := true
	var speakingStart time.Time
	var pendingChunk []int16 // First of 2 chunks waiting for second
	var noDataTicks int      // Consecutive ticks with no audio data

	// Diagnostic: track speaking/silence cycles and RTP timestamp drift
	var lastSilenceStart time.Time
	senderStart := time.Now()
	var totalPacketsSent int64

	internalSend := func(opus []byte) {
		connManager := dd.Bridge.DiscordVoiceConnectionManager
		if connManager == nil {
			return
		}
		voiceConn := connManager.GetVoiceConnection()
		if voiceConn == nil {
			if lastReady {
				dd.Bridge.Logger.Debug("DISCORD_SEND", "Discord connection not ready, sinking packet")
				lastReady = false
			}
			promPacketsSunk.WithLabelValues("discord", "outbound").Inc()

			return
		}

		// Connection is ready, update lastReady state if needed
		if !lastReady {
			dd.Bridge.Logger.Info("DISCORD_SEND", "Discord ready to send opus packets")
			lastReady = true
		}

		err := voiceConn.SendOpus(opus)
		if err != nil {
			dd.Bridge.Logger.Debug("DISCORD_SEND", fmt.Sprintf("Error sending opus: %v", err))

			return
		}

		totalPacketsSent++
		promDiscordSentPackets.Inc()
		promRtpTimestampDrift.Set(time.Since(senderStart).Seconds() - float64(totalPacketsSent)*0.02)
	}

	setSpeaking := func(speaking bool) {
		connManager := dd.Bridge.DiscordVoiceConnectionManager
		if connManager == nil {
			return
		}
		voiceConn := connManager.GetVoiceConnection()
		if voiceConn == nil {
			if speaking {
				dd.Bridge.Logger.Debug("DISCORD_SEND", "Discord connection not available for speaking status")
			}

			return
		}
		func() {
			defer func() {
				if r := recover(); r != nil {
					dd.Bridge.Logger.Error("DISCORD_SEND", fmt.Sprintf("Panic setting speaking status: %v", r))
				}
			}()
			if err := voiceConn.SetSpeaking(ctx, speaking); err != nil {
				dd.Bridge.Logger.Error("DISCORD_SEND", fmt.Sprintf("Error setting speaking status to %v: %v", speaking, err))
			}
		}()
	}

	defer dd.Bridge.Logger.Info("DISCORD_SEND", "Stopping Discord send PCM")

	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		promTimerDiscordSend.Observe(float64(dd.discordSendSleepTick.SleepNextTarget(ctx, false)))

		// Mix one 10ms chunk directly from Mumble streams
		chunk, streamingCount := dd.Bridge.MumbleStream.MixOneChunk()
		promMumbleStreaming.Set(float64(streamingCount))

		if chunk != nil {
			noDataTicks = 0

			if pendingChunk == nil {
				// Save first chunk, wait for second on next 10ms tick.
				// This ensures consistent 20ms send intervals which keeps
				// Discord's adaptive jitter buffer from growing.
				pendingChunk = chunk

				continue
			}

			// Have pending + new chunk (20ms) — encode and send
			opus, err := opusEncoder.Encode(append(pendingChunk, chunk...), frameSize, maxBytes)
			pendingChunk = nil

			if err != nil {
				OnError("Encoding Error", err)

				continue
			}

			if !streaming {
				if !lastSilenceStart.IsZero() {
					promSilenceGapMs.Observe(float64(time.Since(lastSilenceStart).Milliseconds()))
				}
				promSpeakingTransitions.Inc()
				speakingStart = time.Now()
				setSpeaking(true)
				streaming = true
			}

			internalSend(opus)
		} else {
			if pendingChunk != nil {
				// Don't discard the pending chunk — pad with silence to complete
				// a 20ms frame. Preserves audio at speech boundaries.
				silencePad := make([]int16, len(pendingChunk))
				opus, err := opusEncoder.Encode(append(pendingChunk, silencePad...), frameSize, maxBytes)
				pendingChunk = nil

				if err != nil {
					OnError("Encoding Error", err)
				} else {
					if !streaming {
						if !lastSilenceStart.IsZero() {
							promSilenceGapMs.Observe(float64(time.Since(lastSilenceStart).Milliseconds()))
						}
						promSpeakingTransitions.Inc()
						speakingStart = time.Now()
						setSpeaking(true)
						streaming = true
					}

					internalSend(opus)
				}

				continue
			}

			noDataTicks++

			if streaming && noDataTicks >= noDataGraceTicks {
				if time.Since(speakingStart).Milliseconds() < shortSpeakingThreshold {
					dd.Bridge.Logger.Warn("DISCORD_SEND", "Short Mumble to Discord speaking cycle.")
				}

				// Send silence frames as required by Discord documentation.
				// Each frame is 20ms of Opus silence but SleepCT ticks at 10ms,
				// so sleep twice per frame to maintain correct 20ms send cadence.
				for range silenceFrameCount {
					internalSend(opusSilence)
					promTimerDiscordSend.Observe(float64(dd.discordSendSleepTick.SleepNextTarget(ctx, false)))
					promTimerDiscordSend.Observe(float64(dd.discordSendSleepTick.SleepNextTarget(ctx, false)))
				}

				setSpeaking(false)
				streaming = false
				lastSilenceStart = time.Now()
				noDataTicks = 0
			}
		}
	}
}

// discordReceivePCM receives opus packets from Discord, decodes to PCM.
func (dd *DiscordDuplex) discordReceivePCM(ctx context.Context) {
	lastReady := true

	for {
		connManager := dd.Bridge.DiscordVoiceConnectionManager
		if connManager == nil {
			time.Sleep(connectionCheckInterval * time.Millisecond)

			continue
		}

		voiceConn := connManager.GetVoiceConnection()
		if voiceConn == nil {
			if lastReady {
				dd.Bridge.Logger.Debug("DISCORD_RECEIVE", "Discord connection not ready for receiving")
				lastReady = false
			}
			time.Sleep(connectionCheckInterval * time.Millisecond)

			continue
		}

		// Connection is ready for receiving
		if !lastReady {
			dd.Bridge.Logger.Info("DISCORD_RECEIVE", "Discord ready to receive packets")
			lastReady = true
		}

		// Check context before blocking on receive
		select {
		case <-ctx.Done():
			dd.Bridge.Logger.Info("DISCORD_RECEIVE", "Stopping Discord receive PCM")

			return
		default:
		}

		// ReceiveOpus blocks until a packet arrives
		p, recvErr := voiceConn.ReceiveOpus()
		if recvErr != nil {
			dd.Bridge.Logger.Debug("DISCORD_RECEIVE", fmt.Sprintf("Receive error: %v", recvErr))

			continue
		}

		if p == nil {
			// Connection closing
			continue
		}

		dd.processReceivedPacket(p)
	}
}

// processReceivedPacket handles a single received audio packet.
func (dd *DiscordDuplex) processReceivedPacket(p *discord.AudioPacket) {
	dd.discordMutex.Lock()

	_, ok := dd.fromDiscordMap[p.SSRC]
	if !ok {
		newStream := fromDiscord{}
		newStream.pcm = make(chan []int16, 100)
		newStream.receiving = false
		newStream.streaming = false
		newStream.lastActivity = time.Now()
		var err error
		newStream.decoder, err = gopus.NewDecoder(48000, 1) // Decode into mono
		if err != nil {
			OnError("error creating opus decoder", err)
			dd.discordMutex.Unlock()

			return
		}

		dd.fromDiscordMap[p.SSRC] = newStream
	}

	s := dd.fromDiscordMap[p.SSRC]
	s.lastActivity = time.Now() // Update activity timestamp

	// Skip non-audio packets: if timestamp is frozen but sequence advances,
	// these are not standard opus audio frames (possibly redundancy/metadata).
	// Real audio packets always advance the timestamp by the frame duration (960).
	if s.receiving && p.Timestamp == s.lastTimeStamp && p.Sequence != s.lastSequence {
		dd.Bridge.Logger.Debug("DISCORD_RECEIVE", fmt.Sprintf(
			"Skipping frozen timestamp packet: seq=%d lastSeq=%d ts=%d SSRC=%d",
			p.Sequence, s.lastSequence, p.Timestamp, p.SSRC))
		s.lastSequence = p.Sequence
		dd.fromDiscordMap[p.SSRC] = s
		dd.discordMutex.Unlock()

		return
	}

	// Handle packet loss with PLC (Packet Loss Concealment)
	if s.receiving {
		lostCount := sequenceGap(p.Sequence, s.lastSequence)

		if lostCount > 0 {
			dd.Bridge.Logger.Debug("DISCORD_RECEIVE", fmt.Sprintf(
				"Sequence gap detected: current=%d last=%d lostCount=%d SSRC=%d",
				p.Sequence, s.lastSequence, lostCount, p.SSRC))
		}

		if lostCount > 0 && lostCount <= maxPLCPackets {
			// Generate PLC frames for each lost packet
			dd.Bridge.Logger.Debug("DISCORD_RECEIVE", fmt.Sprintf(
				"Generating %d PLC frames for SSRC=%d", lostCount, p.SSRC))
			for i := 0; i < lostCount; i++ {
				plcPCM, plcErr := s.decoder.Decode(nil, opusFrameSize, false)
				if plcErr != nil {
					dd.Bridge.Logger.Debug("DISCORD_RECEIVE", "PLC decode error, resetting decoder")
					s.decoder.ResetState()
					s.receiving = false

					break
				}
				// Push PLC audio in 10ms chunks (same as normal packets)
				for l := 0; l < len(plcPCM); l += pcmChunkSize {
					select {
					case dd.fromDiscordMap[p.SSRC].pcm <- plcPCM[l : l+pcmChunkSize]:
					default:
						// Buffer full, skip remaining PLC frames
					}
				}
				promDiscordPLCPackets.Inc()
			}
		} else if lostCount > maxPLCPackets {
			// Major discontinuity (>200ms) - likely a new utterance, reset decoder
			dd.Bridge.Logger.Debug("DISCORD_RECEIVE", fmt.Sprintf(
				"Major discontinuity detected: lostCount=%d (>%d), resetting decoder for SSRC=%d",
				lostCount, maxPLCPackets, p.SSRC))
			s.decoder.ResetState()
			s.receiving = false
		}
	}

	// Handle first packet for this stream
	if !s.receiving {
		s.receiving = true
	}

	prevSeq := s.lastSequence
	prevTS := s.lastTimeStamp
	s.lastTimeStamp = p.Timestamp
	s.lastSequence = p.Sequence

	dd.fromDiscordMap[p.SSRC] = s
	dd.discordMutex.Unlock()

	// Always decode with standard frame size - opus packet header contains actual duration
	pcmData, err := s.decoder.Decode(p.Opus, opusFrameSize, false)
	if err != nil {
		dd.Bridge.Logger.Warn("DISCORD_RECEIVE", fmt.Sprintf(
			"Opus decode error: %v | SSRC=%d seq=%d ts=%d opusLen=%d receiving=%v prevSeq=%d prevTS=%d",
			err, p.SSRC, p.Sequence, p.Timestamp, len(p.Opus), s.receiving, prevSeq, prevTS))

		// Reset decoder to recover from corrupted state
		dd.discordMutex.Lock()
		if entry, exists := dd.fromDiscordMap[p.SSRC]; exists {
			entry.decoder.ResetState()
			entry.receiving = false
			dd.fromDiscordMap[p.SSRC] = entry
		}
		dd.discordMutex.Unlock()

		return
	}

	promDiscordReceivedPackets.Inc()

	// Push data into pcm channel in 10ms chunks of mono pcm data
	dd.discordMutex.Lock()
	for l := 0; l < len(pcmData); l += pcmChunkSize {
		var next []int16
		u := l + pcmChunkSize
		next = pcmData[l:u]

		select {
		case dd.fromDiscordMap[p.SSRC].pcm <- next:
		default:
			dd.Bridge.Logger.Debug("DISCORD_RECEIVE", "From Discord buffer full. Dropping packet")
		}
	}
	dd.discordMutex.Unlock()

	dd.discordReceiveSleepTick.Notify()
}

func (dd *DiscordDuplex) fromDiscordMixer(ctx context.Context, toMumble chan<- gumble.AudioBuffer) {
	mumbleSilence := gumble.AudioBuffer{}
	for i := 3; i < pcmChunkSize; i++ {
		mumbleSilence = append(mumbleSilence, 0x00)
	}
	var speakingStart time.Time

	dd.discordReceiveSleepTick.Start(10 * time.Millisecond)

	sendAudio := false
	toMumbleStreaming := false

	for {
		select {
		case <-ctx.Done():
			dd.Bridge.Logger.Info("DISCORD_MIXER", "Stopping from Discord mixer")

			return
		default:
		}

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
			} else if dd.fromDiscordMap[i].streaming {
				x := dd.fromDiscordMap[i]
				x.streaming = false
				x.receiving = false // toggle this here is not optimal but there is no better location atm.
				dd.fromDiscordMap[i] = x
			}
		}

		promDiscordArraySize.Set(float64(len(dd.fromDiscordMap)))
		promDiscordStreaming.Set(float64(streamingCount))

		dd.discordMutex.Unlock()

		mumbleTimeoutSend := func(outBuf []int16) {
			select {
			case toMumble <- outBuf:
				promSentMumblePackets.Inc()
			case <-time.After(10 * time.Millisecond):
				dd.Bridge.Logger.Debug("DISCORD_MIXER", "To Mumble timeout. Dropping packet")
				promToMumbleDropped.Inc()
			}
		}

		if sendAudio {
			// Regular send mixed audio
			outBuf := make([]int16, pcmChunkSize)

			for j := 0; j < len(internalMixerArr); j++ {
				for i := 0; i < len(internalMixerArr[j]); i++ {
					outBuf[i] += (internalMixerArr[j])[i]
				}
			}

			mumbleTimeoutSend(outBuf)
		} else if !sendAudio && toMumbleStreaming {
			// Send opus silence to mumble
			// See note above about jitter buffer warning
			if time.Since(speakingStart).Milliseconds() < shortSpeakingThreshold {
				dd.Bridge.Logger.Warn("DISCORD_MIXER", fmt.Sprintf("Short Discord to Mumble speaking cycle. Consider increasing the size of the to Mumble jitter buffer. Duration: %d ms", time.Since(speakingStart).Milliseconds()))
			}

			for range silenceFrameCount {
				mumbleTimeoutSend(mumbleSilence)
				promTimerDiscordMixer.Observe(float64(dd.discordReceiveSleepTick.SleepNextTarget(ctx, false)))
			}

			toMumbleStreaming = false
		}
	}
}

// StartCleanup starts the background goroutine that removes stale streams from fromDiscordMap
func (dd *DiscordDuplex) StartCleanup(ctx context.Context) {
	cleanupCtx, cancel := context.WithCancel(ctx)
	dd.cleanupCancel = cancel
	go dd.cleanupStaleStreams(cleanupCtx)
}

// StopCleanup stops the cleanup goroutine
func (dd *DiscordDuplex) StopCleanup() {
	if dd.cleanupCancel != nil {
		dd.cleanupCancel()
		dd.cleanupCancel = nil
	}
}

// cleanupStaleStreams periodically removes entries from fromDiscordMap that have been inactive
func (dd *DiscordDuplex) cleanupStaleStreams(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	const staleThreshold = 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			dd.Bridge.Logger.Debug("DISCORD_CLEANUP", "Stopping stale stream cleanup")

			return
		case <-ticker.C:
			dd.discordMutex.Lock()
			now := time.Now()
			removed := 0
			for ssrc, stream := range dd.fromDiscordMap {
				if now.Sub(stream.lastActivity) > staleThreshold && !stream.streaming {
					// Close the PCM channel before removing
					close(stream.pcm)
					delete(dd.fromDiscordMap, ssrc)
					removed++
				}
			}
			mapSize := len(dd.fromDiscordMap)
			dd.discordMutex.Unlock()

			if removed > 0 {
				dd.Bridge.Logger.Debug("DISCORD_CLEANUP", fmt.Sprintf("Removed %d stale streams, %d remaining", removed, mapSize))
			}
		}
	}
}
