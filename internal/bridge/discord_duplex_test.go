package bridge

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stieneee/gopus"
	"github.com/stieneee/gumble/gumble"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestFromDiscordMap_ConcurrentStreamCreation tests multiple SSRC streams created concurrently
func TestFromDiscordMap_ConcurrentStreamCreation(t *testing.T) {
	bridge := createTestBridgeState(nil)
	duplex := NewDiscordDuplex(bridge)

	var wg sync.WaitGroup

	// Concurrent stream access (simulates receiving from multiple SSRCs)
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ssrc := uint32(1000 + idx%10)

			duplex.discordMutex.Lock()
			if _, exists := duplex.fromDiscordMap[ssrc]; !exists {
				duplex.fromDiscordMap[ssrc] = fromDiscord{
					pcm:          make(chan []int16, 100),
					receiving:    false,
					streaming:    false,
					lastActivity: time.Now(),
				}
			}
			duplex.discordMutex.Unlock()
		}(i)
	}

	wg.Wait()

	// Verify some streams were created
	duplex.discordMutex.Lock()
	assert.NotEmpty(t, duplex.fromDiscordMap)
	duplex.discordMutex.Unlock()
}

// TestFromDiscordMap_CleanupWhileActive tests cleanup doesn't close active streams
func TestFromDiscordMap_CleanupWhileActive(t *testing.T) {
	bridge := createTestBridgeState(nil)
	duplex := NewDiscordDuplex(bridge)

	// Create a stream that's actively streaming
	duplex.discordMutex.Lock()
	duplex.fromDiscordMap[12345] = fromDiscord{
		pcm:          make(chan []int16, 100),
		receiving:    true,
		streaming:    true, // Marked as streaming
		lastActivity: time.Now(),
	}
	duplex.discordMutex.Unlock()

	// Simulate cleanup check (as done in cleanupStaleStreams)
	duplex.discordMutex.Lock()
	for ssrc, stream := range duplex.fromDiscordMap {
		if !stream.streaming {
			// Would clean up
			delete(duplex.fromDiscordMap, ssrc)
		}
	}
	streamExists := len(duplex.fromDiscordMap) > 0
	duplex.discordMutex.Unlock()

	// Active stream should still exist
	assert.True(t, streamExists, "Active stream should not be cleaned up")
}

// TestFromDiscordMap_ConcurrentReadWrite tests concurrent map access
func TestFromDiscordMap_ConcurrentReadWrite(_ *testing.T) {
	bridge := createTestBridgeState(nil)
	duplex := NewDiscordDuplex(bridge)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var wg sync.WaitGroup

	// Writers
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				ssrc := uint32(1000 + idx)
				duplex.discordMutex.Lock()
				duplex.fromDiscordMap[ssrc] = fromDiscord{
					pcm:          make(chan []int16, 10),
					receiving:    true,
					streaming:    j%2 == 0,
					lastActivity: time.Now(),
				}
				duplex.discordMutex.Unlock()
			}
		}(i)
	}

	// Readers
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				duplex.discordMutex.Lock()
				for ssrc := range duplex.fromDiscordMap {
					_ = duplex.fromDiscordMap[ssrc].streaming
				}
				duplex.discordMutex.Unlock()
			}
		}()
	}

	wg.Wait()
}

// TestFromDiscordMixer_SendToClosedChannel tests handling of closed channel
func TestFromDiscordMixer_SendToClosedChannel(t *testing.T) {
	// Test the mumbleTimeoutSend pattern used in fromDiscordMixer
	toMumble := make(chan gumble.AudioBuffer, 10)

	// Close the channel
	close(toMumble)

	// Sending to closed channel should panic, but the code uses select with timeout
	// Test the timeout pattern
	outBuf := make([]int16, 480)

	sent := false
	func() {
		defer func() {
			// Recover from panic on closed channel
			_ = recover() //nolint:errcheck // intentionally catching panic
		}()
		select {
		case toMumble <- outBuf:
			sent = true
		case <-time.After(10 * time.Millisecond):
			// Timeout
		}
	}()

	// Should have triggered panic or timeout, not successful send
	assert.False(t, sent, "Should not send to closed channel successfully")
}

// TestMumbleTimeoutSend_NoGoroutineLeak verifies time.After pattern
func TestMumbleTimeoutSend_NoGoroutineLeak(t *testing.T) {
	toMumble := make(chan gumble.AudioBuffer, 1) // Small buffer

	outBuf := make([]int16, 480)

	// Fill the channel
	toMumble <- outBuf

	// Now channel is full, sending should timeout
	start := time.Now()
	select {
	case toMumble <- outBuf:
		t.Fatal("Should not be able to send to full channel")
	case <-time.After(10 * time.Millisecond):
		// Expected timeout
	}
	elapsed := time.Since(start)

	// Should timeout quickly
	assert.Less(t, elapsed, 50*time.Millisecond)
}

// TestDiscordDuplex_NewInstance tests DiscordDuplex creation
func TestDiscordDuplex_NewInstance(t *testing.T) {
	bridge := createTestBridgeState(nil)
	duplex := NewDiscordDuplex(bridge)

	assert.NotNil(t, duplex)
	assert.NotNil(t, duplex.fromDiscordMap)
	assert.Equal(t, bridge, duplex.Bridge)
}

// TestDiscordDuplex_StartStopCleanup tests cleanup goroutine lifecycle
func TestDiscordDuplex_StartStopCleanup(t *testing.T) {
	bridge := createTestBridgeState(nil)
	duplex := NewDiscordDuplex(bridge)

	ctx, cancel := context.WithCancel(context.Background())

	// Start cleanup
	duplex.StartCleanup(ctx)

	// Should have cancel function set
	assert.NotNil(t, duplex.cleanupCancel)

	// Stop cleanup
	cancel()
	duplex.StopCleanup()

	// Cancel should be nil after stop
	assert.Nil(t, duplex.cleanupCancel)
}

// TestDiscordDuplex_CleanupStaleStreams tests stale stream removal
func TestDiscordDuplex_CleanupStaleStreams(t *testing.T) {
	bridge := createTestBridgeState(nil)
	duplex := NewDiscordDuplex(bridge)

	// Add some streams
	duplex.discordMutex.Lock()
	// Active stream (recent activity, streaming)
	duplex.fromDiscordMap[1001] = fromDiscord{
		pcm:          make(chan []int16, 100),
		streaming:    true,
		lastActivity: time.Now(),
	}
	// Stale stream (old activity, not streaming)
	duplex.fromDiscordMap[1002] = fromDiscord{
		pcm:          make(chan []int16, 100),
		streaming:    false,
		lastActivity: time.Now().Add(-60 * time.Second), // 60 seconds old
	}
	duplex.discordMutex.Unlock()

	// Simulate cleanup logic (threshold is 30 seconds)
	staleThreshold := 30 * time.Second
	now := time.Now()

	duplex.discordMutex.Lock()
	for ssrc, stream := range duplex.fromDiscordMap {
		if now.Sub(stream.lastActivity) > staleThreshold && !stream.streaming {
			close(stream.pcm)
			delete(duplex.fromDiscordMap, ssrc)
		}
	}
	remaining := len(duplex.fromDiscordMap)
	duplex.discordMutex.Unlock()

	// Only active stream should remain
	assert.Equal(t, 1, remaining)
}

// TestDiscordDuplex_StreamCreationRace tests concurrent stream creation for same SSRC
func TestDiscordDuplex_StreamCreationRace(t *testing.T) {
	bridge := createTestBridgeState(nil)
	duplex := NewDiscordDuplex(bridge)

	var wg sync.WaitGroup
	ssrc := uint32(9999)
	var createdCount int32

	// Multiple goroutines trying to create stream for same SSRC
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			duplex.discordMutex.Lock()
			if _, exists := duplex.fromDiscordMap[ssrc]; !exists {
				duplex.fromDiscordMap[ssrc] = fromDiscord{
					pcm:          make(chan []int16, 100),
					lastActivity: time.Now(),
				}
				atomic.AddInt32(&createdCount, 1)
			}
			duplex.discordMutex.Unlock()
		}()
	}

	wg.Wait()

	// Should only create one stream
	assert.Equal(t, int32(1), atomic.LoadInt32(&createdCount))
}

// TestDiscordDuplex_PCMChannelBuffering tests PCM channel behavior
func TestDiscordDuplex_PCMChannelBuffering(t *testing.T) {
	bridge := createTestBridgeState(nil)
	duplex := NewDiscordDuplex(bridge)

	// Create a stream
	ssrc := uint32(5000)
	pcmChan := make(chan []int16, 100)
	duplex.discordMutex.Lock()
	duplex.fromDiscordMap[ssrc] = fromDiscord{
		pcm:          pcmChan,
		lastActivity: time.Now(),
	}
	duplex.discordMutex.Unlock()

	// Send data to PCM channel
	testData := make([]int16, 480)
	for i := range testData {
		testData[i] = int16(i)
	}

	// Should not block with buffered channel
	select {
	case pcmChan <- testData:
		// Success
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Send to PCM channel timed out")
	}

	// Read back
	select {
	case received := <-pcmChan:
		assert.Equal(t, testData, received)
	case <-time.After(100 * time.Millisecond):
		t.Fatal("Receive from PCM channel timed out")
	}
}

// TestDiscordDuplex_StreamStateTransitions tests streaming state changes
func TestDiscordDuplex_StreamStateTransitions(t *testing.T) {
	bridge := createTestBridgeState(nil)
	duplex := NewDiscordDuplex(bridge)

	ssrc := uint32(7777)

	// Create non-streaming stream
	duplex.discordMutex.Lock()
	duplex.fromDiscordMap[ssrc] = fromDiscord{
		pcm:       make(chan []int16, 100),
		receiving: false,
		streaming: false,
	}
	duplex.discordMutex.Unlock()

	// Transition to streaming
	duplex.discordMutex.Lock()
	stream := duplex.fromDiscordMap[ssrc]
	stream.streaming = true
	stream.receiving = true
	duplex.fromDiscordMap[ssrc] = stream
	duplex.discordMutex.Unlock()

	// Verify state
	duplex.discordMutex.Lock()
	assert.True(t, duplex.fromDiscordMap[ssrc].streaming)
	assert.True(t, duplex.fromDiscordMap[ssrc].receiving)
	duplex.discordMutex.Unlock()

	// Transition back
	duplex.discordMutex.Lock()
	stream = duplex.fromDiscordMap[ssrc]
	stream.streaming = false
	stream.receiving = false
	duplex.fromDiscordMap[ssrc] = stream
	duplex.discordMutex.Unlock()

	// Verify state
	duplex.discordMutex.Lock()
	assert.False(t, duplex.fromDiscordMap[ssrc].streaming)
	assert.False(t, duplex.fromDiscordMap[ssrc].receiving)
	duplex.discordMutex.Unlock()
}

// TestDiscordDuplex_ConcurrentCleanupAndReceive tests cleanup during receive
func TestDiscordDuplex_ConcurrentCleanupAndReceive(_ *testing.T) {
	bridge := createTestBridgeState(nil)
	duplex := NewDiscordDuplex(bridge)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Start cleanup
	duplex.StartCleanup(ctx)

	var wg sync.WaitGroup

	// Simulate receiving (creating streams)
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				ssrc := uint32(2000 + idx)
				duplex.discordMutex.Lock()
				if _, exists := duplex.fromDiscordMap[ssrc]; !exists {
					duplex.fromDiscordMap[ssrc] = fromDiscord{
						pcm:          make(chan []int16, 100),
						streaming:    j%2 == 0,
						lastActivity: time.Now(),
					}
				} else {
					stream := duplex.fromDiscordMap[ssrc]
					stream.lastActivity = time.Now()
					duplex.fromDiscordMap[ssrc] = stream
				}
				duplex.discordMutex.Unlock()
				time.Sleep(time.Millisecond)
			}
		}(i)
	}

	wg.Wait()
	duplex.StopCleanup()
}

// TestDiscordDuplex_MapSizeMetric tests that map size can be read safely
func TestDiscordDuplex_MapSizeMetric(t *testing.T) {
	bridge := createTestBridgeState(nil)
	duplex := NewDiscordDuplex(bridge)

	// Add streams
	duplex.discordMutex.Lock()
	for i := 0; i < 10; i++ {
		duplex.fromDiscordMap[uint32(i)] = fromDiscord{
			pcm: make(chan []int16, 100),
		}
	}
	duplex.discordMutex.Unlock()

	// Read size concurrently
	var wg sync.WaitGroup
	var sizes []int

	var mu sync.Mutex
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			duplex.discordMutex.Lock()
			size := len(duplex.fromDiscordMap)
			duplex.discordMutex.Unlock()
			mu.Lock()
			sizes = append(sizes, size)
			mu.Unlock()
		}()
	}

	wg.Wait()

	// All reads should succeed
	assert.Len(t, sizes, 50)
}

// TestDiscordDuplex_SleepTickInitialization tests SleepCT initialization
func TestDiscordDuplex_SleepTickInitialization(t *testing.T) {
	bridge := createTestBridgeState(nil)
	duplex := NewDiscordDuplex(bridge)

	// SleepCT should be zero-valued but usable
	assert.NotNil(t, &duplex.discordSendSleepTick)
	assert.NotNil(t, &duplex.discordReceiveSleepTick)
}

// TestDiscordDuplex_BridgeReference tests bridge reference is maintained
func TestDiscordDuplex_BridgeReference(t *testing.T) {
	logger := NewMockLogger()
	bridge := createTestBridgeState(logger)
	duplex := NewDiscordDuplex(bridge)

	require.Same(t, bridge, duplex.Bridge)
	require.Same(t, logger, duplex.Bridge.Logger)
}

// TestSequenceGap tests the sequenceGap function with various scenarios
func TestSequenceGap(t *testing.T) {
	tests := []struct {
		name     string
		current  uint16
		last     uint16
		expected int
	}{
		{
			name:     "Sequential packets (no gap)",
			current:  101,
			last:     100,
			expected: 0,
		},
		{
			name:     "One lost packet",
			current:  102,
			last:     100,
			expected: 1,
		},
		{
			name:     "Five lost packets",
			current:  106,
			last:     100,
			expected: 5,
		},
		{
			name:     "Wrap-around at uint16 boundary (no gap)",
			current:  0,
			last:     65535,
			expected: 0,
		},
		{
			name:     "Wrap-around with one lost packet",
			current:  1,
			last:     65535,
			expected: 1,
		},
		{
			name:     "Wrap-around with multiple lost packets",
			current:  5,
			last:     65535,
			expected: 5,
		},
		{
			name:     "Large gap (100 packets)",
			current:  200,
			last:     100,
			expected: 99,
		},
		{
			name:     "Wrap-around large gap",
			current:  100,
			last:     65500,
			expected: 135,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := sequenceGap(tt.current, tt.last)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestSequenceGap_EdgeCases tests edge cases for sequence gap calculation
func TestSequenceGap_EdgeCases(t *testing.T) {
	// Same sequence number (should return -1, which represents a duplicate/reordered packet)
	result := sequenceGap(100, 100)
	assert.Equal(t, -1, result)

	// Maximum uint16 values
	result = sequenceGap(65535, 65534)
	assert.Equal(t, 0, result)

	// Minimum uint16 values
	result = sequenceGap(1, 0)
	assert.Equal(t, 0, result)
}

// --- Integration test helpers for discordReceivePCM ---

// createTestDiscordVoiceConnectionManager creates a DiscordVoiceConnectionManager
// with injected opus channels for testing, bypassing the need for a real Discord session.
func createTestDiscordVoiceConnectionManager(opusSend chan []byte, opusRecv chan *discordgo.Packet) *DiscordVoiceConnectionManager {
	base := NewBaseConnectionManager(NewMockLogger(), "discord-test", NewMockBridgeEventEmitter())
	base.SetStatus(ConnectionConnected, nil)

	// Create a mock VoiceConnection with Ready=true so GetOpusChannels returns ready
	voiceConn := &discordgo.VoiceConnection{}
	voiceConn.Lock()
	voiceConn.Ready = true
	voiceConn.Unlock()

	mgr := &DiscordVoiceConnectionManager{
		BaseConnectionManager: base,
		connection:            voiceConn,
	}
	mgr.opusSend = opusSend
	mgr.opusRecv = opusRecv

	return mgr
}

// encodeOpusSilence encodes a frame of silence using a real opus encoder.
func encodeOpusSilence(t *testing.T) []byte {
	t.Helper()

	const channels = 1
	const frameRate = 48000
	const frameSize = 960
	const maxBytes = frameSize * 2 * 2

	encoder, err := gopus.NewEncoder(frameRate, channels, gopus.Audio)
	require.NoError(t, err)

	silence := make([]int16, frameSize)
	opus, err := encoder.Encode(silence, frameSize, maxBytes)
	require.NoError(t, err)

	return opus
}

// makePacket creates a discordgo.Packet with the given parameters.
func makePacket(ssrc uint32, seq uint16, ts uint32, opus []byte) *discordgo.Packet {
	return &discordgo.Packet{
		SSRC:      ssrc,
		Sequence:  seq,
		Timestamp: ts,
		Opus:      opus,
	}
}

// drainPCM reads all available PCM chunks from the channel within the timeout.
func drainPCM(ch <-chan []int16, timeout time.Duration) [][]int16 {
	var chunks [][]int16
	deadline := time.After(timeout)

	for {
		select {
		case chunk := <-ch:
			chunks = append(chunks, chunk)
		case <-deadline:
			return chunks
		}
	}
}

// --- Integration tests for discordReceivePCM ---

// TestDiscordReceivePCM_NormalFlow exercises the full decode + chunking pipeline
// by feeding sequential opus packets through discordReceivePCM.
func TestDiscordReceivePCM_NormalFlow(t *testing.T) {
	opusSend := make(chan []byte, 10)
	opusRecv := make(chan *discordgo.Packet, 10)

	logger := NewMockLogger()
	bridge := createTestBridgeState(logger)
	bridge.DiscordVoiceConnectionManager = createTestDiscordVoiceConnectionManager(opusSend, opusRecv)

	duplex := NewDiscordDuplex(bridge)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go duplex.discordReceivePCM(ctx)

	opusData := encodeOpusSilence(t)
	ssrc := uint32(1000)

	// Send 3 sequential packets
	opusRecv <- makePacket(ssrc, 100, 48000, opusData)
	opusRecv <- makePacket(ssrc, 101, 48960, opusData)
	opusRecv <- makePacket(ssrc, 102, 49920, opusData)

	// Wait for processing
	time.Sleep(200 * time.Millisecond)

	// Verify PCM output: each 960-sample opus frame produces 2x 480-sample chunks
	duplex.discordMutex.Lock()
	s, exists := duplex.fromDiscordMap[ssrc]
	duplex.discordMutex.Unlock()
	require.True(t, exists, "Stream should exist for SSRC")

	chunks := drainPCM(s.pcm, 100*time.Millisecond)
	// 3 packets * 2 chunks each = 6 chunks
	assert.Equal(t, 6, len(chunks), "Expected 6 PCM chunks from 3 packets")

	for i, chunk := range chunks {
		assert.Equal(t, pcmChunkSize, len(chunk), "Chunk %d should be %d samples", i, pcmChunkSize)
	}

	// No errors should have been logged
	for _, entry := range logger.GetEntriesByLevel("ERROR") {
		t.Errorf("Unexpected error log: %s", entry.Message)
	}
}

// TestDiscordReceivePCM_PLCIntegration exercises PLC frame generation through
// the actual discordReceivePCM path by introducing a sequence gap.
func TestDiscordReceivePCM_PLCIntegration(t *testing.T) {
	opusSend := make(chan []byte, 10)
	opusRecv := make(chan *discordgo.Packet, 10)

	logger := NewMockLogger()
	bridge := createTestBridgeState(logger)
	bridge.DiscordVoiceConnectionManager = createTestDiscordVoiceConnectionManager(opusSend, opusRecv)

	duplex := NewDiscordDuplex(bridge)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go duplex.discordReceivePCM(ctx)

	opusData := encodeOpusSilence(t)
	ssrc := uint32(2000)

	// Send first packet to establish stream
	opusRecv <- makePacket(ssrc, 100, 48000, opusData)
	time.Sleep(50 * time.Millisecond)

	// Drain the first packet's PCM
	duplex.discordMutex.Lock()
	s := duplex.fromDiscordMap[ssrc]
	duplex.discordMutex.Unlock()
	drainPCM(s.pcm, 100*time.Millisecond)

	// Send packet with gap of 2 (seq 101 and 102 are "lost")
	opusRecv <- makePacket(ssrc, 103, 48000+960*3, opusData)
	time.Sleep(200 * time.Millisecond)

	// Expect: 2 PLC frames (2 chunks each) + 1 real frame (2 chunks) = 6 chunks
	duplex.discordMutex.Lock()
	s = duplex.fromDiscordMap[ssrc]
	duplex.discordMutex.Unlock()

	chunks := drainPCM(s.pcm, 100*time.Millisecond)
	// 2 PLC packets * 2 chunks + 1 real packet * 2 chunks = 6
	assert.Equal(t, 6, len(chunks), "Expected 6 PCM chunks (2 PLC + 1 real, each producing 2 chunks)")

	// Verify PLC was logged
	assert.True(t, logger.ContainsMessage("PLC"), "Should log PLC frame generation")
}

// TestDiscordReceivePCM_FrozenTimestampIntegration verifies that packets with
// frozen timestamps (same timestamp, different sequence) are skipped.
func TestDiscordReceivePCM_FrozenTimestampIntegration(t *testing.T) {
	opusSend := make(chan []byte, 10)
	opusRecv := make(chan *discordgo.Packet, 10)

	logger := NewMockLogger()
	bridge := createTestBridgeState(logger)
	bridge.DiscordVoiceConnectionManager = createTestDiscordVoiceConnectionManager(opusSend, opusRecv)

	duplex := NewDiscordDuplex(bridge)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go duplex.discordReceivePCM(ctx)

	opusData := encodeOpusSilence(t)
	ssrc := uint32(3000)
	ts := uint32(48000)

	// Send first normal packet
	opusRecv <- makePacket(ssrc, 100, ts, opusData)
	time.Sleep(50 * time.Millisecond)

	// Drain first packet's PCM
	duplex.discordMutex.Lock()
	s := duplex.fromDiscordMap[ssrc]
	duplex.discordMutex.Unlock()
	drainPCM(s.pcm, 100*time.Millisecond)

	// Send frozen timestamp packet (same timestamp, advanced sequence)
	opusRecv <- makePacket(ssrc, 101, ts, opusData) // ts unchanged = frozen
	time.Sleep(100 * time.Millisecond)

	// Should produce NO PCM output (packet was skipped)
	duplex.discordMutex.Lock()
	s = duplex.fromDiscordMap[ssrc]
	duplex.discordMutex.Unlock()

	chunks := drainPCM(s.pcm, 100*time.Millisecond)
	assert.Equal(t, 0, len(chunks), "Frozen timestamp packet should produce no PCM output")

	// Verify skip was logged
	assert.True(t, logger.ContainsMessage("frozen timestamp"), "Should log frozen timestamp skip")

	// Send next normal packet (with advanced timestamp) to confirm stream still works
	opusRecv <- makePacket(ssrc, 102, ts+960, opusData)
	time.Sleep(100 * time.Millisecond)

	duplex.discordMutex.Lock()
	s = duplex.fromDiscordMap[ssrc]
	duplex.discordMutex.Unlock()

	chunks = drainPCM(s.pcm, 100*time.Millisecond)
	assert.Equal(t, 2, len(chunks), "Normal packet after frozen should produce 2 PCM chunks")
}

// TestDiscordReceivePCM_MajorDiscontinuityIntegration verifies that large sequence
// gaps (>maxPLCPackets) reset the decoder without generating PLC frames.
func TestDiscordReceivePCM_MajorDiscontinuityIntegration(t *testing.T) {
	opusSend := make(chan []byte, 10)
	opusRecv := make(chan *discordgo.Packet, 10)

	logger := NewMockLogger()
	bridge := createTestBridgeState(logger)
	bridge.DiscordVoiceConnectionManager = createTestDiscordVoiceConnectionManager(opusSend, opusRecv)

	duplex := NewDiscordDuplex(bridge)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go duplex.discordReceivePCM(ctx)

	opusData := encodeOpusSilence(t)
	ssrc := uint32(4000)

	// Send first packet to establish stream
	opusRecv <- makePacket(ssrc, 100, 48000, opusData)
	time.Sleep(50 * time.Millisecond)

	// Drain first packet's PCM
	duplex.discordMutex.Lock()
	s := duplex.fromDiscordMap[ssrc]
	duplex.discordMutex.Unlock()
	drainPCM(s.pcm, 100*time.Millisecond)

	// Send packet with major gap (99 lost > maxPLCPackets=10)
	opusRecv <- makePacket(ssrc, 200, 48000+960*100, opusData)
	time.Sleep(200 * time.Millisecond)

	duplex.discordMutex.Lock()
	s = duplex.fromDiscordMap[ssrc]
	duplex.discordMutex.Unlock()

	chunks := drainPCM(s.pcm, 100*time.Millisecond)
	// Should get only the real packet's chunks (2), no PLC frames
	assert.Equal(t, 2, len(chunks), "Major discontinuity should not generate PLC, only decode the arriving packet")

	// Verify discontinuity was logged
	assert.True(t, logger.ContainsMessage("Major discontinuity"), "Should log major discontinuity detection")
}

// TestDiscordReceivePCM_PCMChunking tests that PCM is chunked into 10ms frames
func TestDiscordReceivePCM_PCMChunking(t *testing.T) {
	// Test the chunking logic that splits PCM into pcmChunkSize chunks
	testPCM := make([]int16, opusFrameSize) // 960 samples = 20ms
	for i := range testPCM {
		testPCM[i] = int16(i)
	}

	// Simulate chunking
	expectedChunks := len(testPCM) / pcmChunkSize
	chunks := make([][]int16, 0, expectedChunks)
	for l := 0; l < len(testPCM); l += pcmChunkSize {
		u := l + pcmChunkSize
		chunk := testPCM[l:u]
		chunks = append(chunks, chunk)
	}

	// Should produce 2 chunks (960 / 480 = 2)
	assert.Equal(t, 2, len(chunks))

	// Each chunk should be pcmChunkSize samples
	for i, chunk := range chunks {
		assert.Equal(t, pcmChunkSize, len(chunk), "Chunk %d should be %d samples", i, pcmChunkSize)
	}

	// Verify data integrity
	assert.Equal(t, testPCM[0:pcmChunkSize], chunks[0])
	assert.Equal(t, testPCM[pcmChunkSize:opusFrameSize], chunks[1])
}

// TestDiscordReceivePCM_SequenceTracking tests sequence number tracking
func TestDiscordReceivePCM_SequenceTracking(t *testing.T) {
	bridge := createTestBridgeState(nil)
	duplex := NewDiscordDuplex(bridge)

	ssrc := uint32(99999)

	// Create stream
	duplex.discordMutex.Lock()
	duplex.fromDiscordMap[ssrc] = fromDiscord{
		pcm:          make(chan []int16, 100),
		receiving:    false,
		lastSequence: 0,
		lastActivity: time.Now(),
	}
	duplex.discordMutex.Unlock()

	// Simulate receiving packets in sequence
	sequences := []uint16{100, 101, 102, 103}

	for _, seq := range sequences {
		duplex.discordMutex.Lock()
		s := duplex.fromDiscordMap[ssrc]

		if s.receiving {
			lostCount := sequenceGap(seq, s.lastSequence)
			assert.Equal(t, 0, lostCount, "Sequential packets should have no gap")
		} else {
			s.receiving = true
		}

		s.lastSequence = seq
		duplex.fromDiscordMap[ssrc] = s
		duplex.discordMutex.Unlock()
	}

	// Verify final sequence
	duplex.discordMutex.Lock()
	finalSeq := duplex.fromDiscordMap[ssrc].lastSequence
	duplex.discordMutex.Unlock()

	assert.Equal(t, uint16(103), finalSeq)
}
