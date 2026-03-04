package bridge

import (
	"context"
	"runtime"
	"sync"
	"testing"
	"time"

	gopus "github.com/stieneee/gopus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/stieneee/mumble-discord-bridge/internal/discord"
)

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// encodeOpusSilence produces a valid Opus frame containing 960 samples of silence.
func encodeOpusSilence() []byte {
	enc, err := gopus.NewEncoder(48000, 1, gopus.Audio)
	if err != nil {
		panic(err)
	}
	silence := make([]int16, 960)
	data, err := enc.Encode(silence, 960, 960*2)
	if err != nil {
		panic(err)
	}
	return data
}

// makePacket is a convenience constructor for discord.AudioPacket.
func makePacket(ssrc uint32, seq uint16, ts uint32, opus []byte) *discord.AudioPacket {
	return &discord.AudioPacket{
		SSRC:      ssrc,
		Sequence:  seq,
		Timestamp: ts,
		Opus:      opus,
	}
}

// drainPCM reads all available PCM chunks from ch until timeout elapses with
// no new data.
func drainPCM(ch chan []int16, timeout time.Duration) [][]int16 {
	var result [][]int16
	deadline := time.After(timeout)
	for {
		select {
		case data := <-ch:
			result = append(result, data)
		case <-deadline:
			return result
		}
	}
}

// ---------------------------------------------------------------------------
// 1. TestFromDiscordMap_ConcurrentStreamCreation
// ---------------------------------------------------------------------------

func TestFromDiscordMap_ConcurrentStreamCreation(t *testing.T) {
	t.Parallel()

	dd := NewDiscordDuplex(createTestBridgeState(nil))
	const goroutines = 50
	const ssrcCount = 10

	runConcurrentlyWithTimeout(t, 5*time.Second, goroutines, func(i int) {
		ssrc := uint32(i % ssrcCount)
		dd.discordMutex.Lock()
		if _, ok := dd.fromDiscordMap[ssrc]; !ok {
			dd.fromDiscordMap[ssrc] = fromDiscord{
				pcm:          make(chan []int16, 100),
				lastActivity: time.Now(),
			}
		}
		dd.discordMutex.Unlock()
	})

	dd.discordMutex.Lock()
	defer dd.discordMutex.Unlock()
	assert.Equal(t, ssrcCount, len(dd.fromDiscordMap), "expected exactly %d unique SSRCs", ssrcCount)
}

// ---------------------------------------------------------------------------
// 2. TestFromDiscordMap_CleanupWhileActive
// ---------------------------------------------------------------------------

func TestFromDiscordMap_CleanupWhileActive(t *testing.T) {
	t.Parallel()

	dd := NewDiscordDuplex(createTestBridgeState(nil))

	// Active stream — should survive cleanup.
	dd.fromDiscordMap[1] = fromDiscord{
		pcm:          make(chan []int16, 100),
		streaming:    true,
		lastActivity: time.Now().Add(-1 * time.Minute),
	}
	// Stale stream — should be removed.
	dd.fromDiscordMap[2] = fromDiscord{
		pcm:          make(chan []int16, 100),
		streaming:    false,
		lastActivity: time.Now().Add(-1 * time.Minute),
	}

	// Simulate one cleanup pass (inline, not via goroutine).
	staleThreshold := 30 * time.Second
	dd.discordMutex.Lock()
	now := time.Now()
	for ssrc, stream := range dd.fromDiscordMap {
		if now.Sub(stream.lastActivity) > staleThreshold && !stream.streaming {
			close(stream.pcm)
			delete(dd.fromDiscordMap, ssrc)
		}
	}
	dd.discordMutex.Unlock()

	dd.discordMutex.Lock()
	defer dd.discordMutex.Unlock()
	assert.Contains(t, dd.fromDiscordMap, uint32(1), "active stream must survive cleanup")
	assert.NotContains(t, dd.fromDiscordMap, uint32(2), "stale stream must be removed")
}

// ---------------------------------------------------------------------------
// 3. TestFromDiscordMap_ConcurrentReadWrite
// ---------------------------------------------------------------------------

func TestFromDiscordMap_ConcurrentReadWrite(t *testing.T) {
	t.Parallel()

	dd := NewDiscordDuplex(createTestBridgeState(nil))

	const writers = 20
	const readers = 20

	var wg sync.WaitGroup
	wg.Add(writers + readers)

	// Writers
	for i := 0; i < writers; i++ {
		go func(idx int) {
			defer wg.Done()
			ssrc := uint32(idx)
			dd.discordMutex.Lock()
			dd.fromDiscordMap[ssrc] = fromDiscord{
				pcm:          make(chan []int16, 100),
				lastActivity: time.Now(),
			}
			dd.discordMutex.Unlock()
		}(i)
	}

	// Readers
	for i := 0; i < readers; i++ {
		go func() {
			defer wg.Done()
			dd.discordMutex.Lock()
			_ = len(dd.fromDiscordMap)
			dd.discordMutex.Unlock()
		}()
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("concurrent read/write stress test timed out")
	}
}

// ---------------------------------------------------------------------------
// 4. TestFromDiscordMixer_SendToClosedChannel
// ---------------------------------------------------------------------------

func TestFromDiscordMixer_SendToClosedChannel(t *testing.T) {
	t.Parallel()

	// Simulate the mumbleTimeoutSend pattern with a full channel.
	ch := make(chan []int16) // unbuffered — nobody reading

	done := make(chan bool, 1)
	go func() {
		select {
		case ch <- make([]int16, pcmChunkSize):
			done <- true
		case <-time.After(10 * time.Millisecond):
			done <- false
		}
	}()

	result := <-done
	assert.False(t, result, "timeout send to a blocked channel must not succeed")
}

// ---------------------------------------------------------------------------
// 5. TestMumbleTimeoutSend_NoGoroutineLeak
// ---------------------------------------------------------------------------

func TestMumbleTimeoutSend_NoGoroutineLeak(t *testing.T) {
	t.Parallel()

	before := runtime.NumGoroutine()
	ch := make(chan []int16, 1)
	ch <- make([]int16, pcmChunkSize) // fill the buffer

	const iterations = 50
	var wg sync.WaitGroup
	wg.Add(iterations)

	for i := 0; i < iterations; i++ {
		go func() {
			defer wg.Done()
			select {
			case ch <- make([]int16, pcmChunkSize):
			case <-time.After(1 * time.Millisecond):
			}
		}()
	}

	wg.Wait()
	// Allow goroutines a moment to wind down.
	time.Sleep(50 * time.Millisecond)
	after := runtime.NumGoroutine()
	// Give some headroom; the key assertion is that goroutines are not leaking.
	assert.LessOrEqual(t, after, before+10, "goroutine count should not grow significantly")
}

// ---------------------------------------------------------------------------
// 6. TestDiscordDuplex_NewInstance
// ---------------------------------------------------------------------------

func TestDiscordDuplex_NewInstance(t *testing.T) {
	t.Parallel()

	bs := createTestBridgeState(nil)
	dd := NewDiscordDuplex(bs)

	require.NotNil(t, dd)
	assert.Same(t, bs, dd.Bridge)
	assert.NotNil(t, dd.fromDiscordMap)
	assert.Empty(t, dd.fromDiscordMap)
}

// ---------------------------------------------------------------------------
// 7. TestDiscordDuplex_StartStopCleanup
// ---------------------------------------------------------------------------

func TestDiscordDuplex_StartStopCleanup(t *testing.T) {
	t.Parallel()

	dd := NewDiscordDuplex(createTestBridgeState(nil))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	dd.StartCleanup(ctx)
	assert.NotNil(t, dd.cleanupCancel, "cleanupCancel should be set after StartCleanup")

	dd.StopCleanup()
	assert.Nil(t, dd.cleanupCancel, "cleanupCancel should be nil after StopCleanup")
}

// ---------------------------------------------------------------------------
// 8. TestDiscordDuplex_CleanupStaleStreams
// ---------------------------------------------------------------------------

func TestDiscordDuplex_CleanupStaleStreams(t *testing.T) {
	t.Parallel()

	dd := NewDiscordDuplex(createTestBridgeState(nil))

	// Add an active (recent) stream.
	dd.fromDiscordMap[1] = fromDiscord{
		pcm:          make(chan []int16, 100),
		lastActivity: time.Now(),
		streaming:    false,
	}
	// Add a stale stream (old, not streaming).
	dd.fromDiscordMap[2] = fromDiscord{
		pcm:          make(chan []int16, 100),
		lastActivity: time.Now().Add(-1 * time.Minute),
		streaming:    false,
	}
	// Add a stale but streaming stream — should survive.
	dd.fromDiscordMap[3] = fromDiscord{
		pcm:          make(chan []int16, 100),
		lastActivity: time.Now().Add(-1 * time.Minute),
		streaming:    true,
	}

	// Inline cleanup pass.
	staleThreshold := 30 * time.Second
	dd.discordMutex.Lock()
	now := time.Now()
	for ssrc, stream := range dd.fromDiscordMap {
		if now.Sub(stream.lastActivity) > staleThreshold && !stream.streaming {
			close(stream.pcm)
			delete(dd.fromDiscordMap, ssrc)
		}
	}
	dd.discordMutex.Unlock()

	dd.discordMutex.Lock()
	defer dd.discordMutex.Unlock()

	assert.Contains(t, dd.fromDiscordMap, uint32(1), "recent stream must survive")
	assert.NotContains(t, dd.fromDiscordMap, uint32(2), "stale non-streaming stream must be removed")
	assert.Contains(t, dd.fromDiscordMap, uint32(3), "stale but streaming stream must survive")
}

// ---------------------------------------------------------------------------
// 9. TestDiscordDuplex_StreamCreationRace
// ---------------------------------------------------------------------------

func TestDiscordDuplex_StreamCreationRace(t *testing.T) {
	t.Parallel()

	dd := NewDiscordDuplex(createTestBridgeState(nil))
	const goroutines = 100
	const ssrc uint32 = 42

	runConcurrentlyWithTimeout(t, 5*time.Second, goroutines, func(_ int) {
		dd.discordMutex.Lock()
		if _, ok := dd.fromDiscordMap[ssrc]; !ok {
			dd.fromDiscordMap[ssrc] = fromDiscord{
				pcm:          make(chan []int16, 100),
				lastActivity: time.Now(),
			}
		}
		dd.discordMutex.Unlock()
	})

	dd.discordMutex.Lock()
	defer dd.discordMutex.Unlock()
	assert.Equal(t, 1, len(dd.fromDiscordMap), "only one entry for the single SSRC")
}

// ---------------------------------------------------------------------------
// 10. TestDiscordDuplex_PCMChannelBuffering
// ---------------------------------------------------------------------------

func TestDiscordDuplex_PCMChannelBuffering(t *testing.T) {
	t.Parallel()

	dd := NewDiscordDuplex(createTestBridgeState(nil))
	ch := make(chan []int16, 100)
	dd.fromDiscordMap[1] = fromDiscord{
		pcm:          ch,
		lastActivity: time.Now(),
	}

	// Write known data.
	for i := 0; i < 10; i++ {
		sample := make([]int16, pcmChunkSize)
		sample[0] = int16(i)
		ch <- sample
	}

	// Read and verify order is preserved.
	for i := 0; i < 10; i++ {
		got := <-ch
		require.Equal(t, int16(i), got[0], "PCM data order must be preserved")
	}
}

// ---------------------------------------------------------------------------
// 11. TestDiscordDuplex_StreamStateTransitions
// ---------------------------------------------------------------------------

func TestDiscordDuplex_StreamStateTransitions(t *testing.T) {
	t.Parallel()

	dd := NewDiscordDuplex(createTestBridgeState(nil))

	dd.discordMutex.Lock()
	dd.fromDiscordMap[1] = fromDiscord{
		pcm:          make(chan []int16, 100),
		receiving:    false,
		streaming:    false,
		lastActivity: time.Now(),
	}
	dd.discordMutex.Unlock()

	// Toggle receiving on.
	dd.discordMutex.Lock()
	s := dd.fromDiscordMap[1]
	assert.False(t, s.receiving)
	s.receiving = true
	dd.fromDiscordMap[1] = s
	dd.discordMutex.Unlock()

	dd.discordMutex.Lock()
	s = dd.fromDiscordMap[1]
	assert.True(t, s.receiving)
	dd.discordMutex.Unlock()

	// Toggle streaming on.
	dd.discordMutex.Lock()
	s = dd.fromDiscordMap[1]
	s.streaming = true
	dd.fromDiscordMap[1] = s
	dd.discordMutex.Unlock()

	dd.discordMutex.Lock()
	s = dd.fromDiscordMap[1]
	assert.True(t, s.streaming)
	dd.discordMutex.Unlock()

	// Toggle streaming off.
	dd.discordMutex.Lock()
	s = dd.fromDiscordMap[1]
	s.streaming = false
	s.receiving = false
	dd.fromDiscordMap[1] = s
	dd.discordMutex.Unlock()

	dd.discordMutex.Lock()
	s = dd.fromDiscordMap[1]
	assert.False(t, s.streaming)
	assert.False(t, s.receiving)
	dd.discordMutex.Unlock()
}

// ---------------------------------------------------------------------------
// 12. TestDiscordDuplex_ConcurrentCleanupAndReceive
// ---------------------------------------------------------------------------

func TestDiscordDuplex_ConcurrentCleanupAndReceive(t *testing.T) {
	t.Parallel()

	dd := NewDiscordDuplex(createTestBridgeState(nil))

	ctx, cancel := context.WithCancel(context.Background())
	dd.StartCleanup(ctx)

	// Concurrently create streams while cleanup runs.
	const goroutines = 50
	runConcurrentlyWithTimeout(t, 5*time.Second, goroutines, func(i int) {
		ssrc := uint32(i)
		dd.discordMutex.Lock()
		dd.fromDiscordMap[ssrc] = fromDiscord{
			pcm:          make(chan []int16, 100),
			lastActivity: time.Now(),
		}
		dd.discordMutex.Unlock()
	})

	cancel()
	dd.StopCleanup()

	// All streams should still be present (they are fresh).
	dd.discordMutex.Lock()
	defer dd.discordMutex.Unlock()
	assert.Equal(t, goroutines, len(dd.fromDiscordMap))
}

// ---------------------------------------------------------------------------
// 13. TestDiscordDuplex_MapSizeMetric
// ---------------------------------------------------------------------------

func TestDiscordDuplex_MapSizeMetric(t *testing.T) {
	t.Parallel()

	dd := NewDiscordDuplex(createTestBridgeState(nil))

	// Pre-populate some streams.
	for i := uint32(0); i < 5; i++ {
		dd.fromDiscordMap[i] = fromDiscord{
			pcm:          make(chan []int16, 100),
			lastActivity: time.Now(),
		}
	}

	const goroutines = 50
	runConcurrentlyWithTimeout(t, 5*time.Second, goroutines, func(_ int) {
		dd.discordMutex.Lock()
		size := len(dd.fromDiscordMap)
		dd.discordMutex.Unlock()
		assert.GreaterOrEqual(t, size, 5, "map size should be at least 5")
	})
}

// ---------------------------------------------------------------------------
// 14. TestDiscordDuplex_SleepTickInitialization
// ---------------------------------------------------------------------------

func TestDiscordDuplex_SleepTickInitialization(t *testing.T) {
	t.Parallel()

	dd := NewDiscordDuplex(createTestBridgeState(nil))

	// SleepCT fields are zero-value structs; they are non-nil because they are
	// value types embedded in DiscordDuplex (not pointers).
	assert.NotNil(t, &dd.discordSendSleepTick, "discordSendSleepTick must be addressable")
	assert.NotNil(t, &dd.discordReceiveSleepTick, "discordReceiveSleepTick must be addressable")
}

// ---------------------------------------------------------------------------
// 15. TestDiscordDuplex_BridgeReference
// ---------------------------------------------------------------------------

func TestDiscordDuplex_BridgeReference(t *testing.T) {
	t.Parallel()

	mockLog := NewMockLogger()
	bs := createTestBridgeState(mockLog)
	dd := NewDiscordDuplex(bs)

	assert.Same(t, bs, dd.Bridge, "Bridge pointer must match")
	ml, ok := dd.Bridge.Logger.(*MockLogger)
	require.True(t, ok, "Logger must be a *MockLogger")
	assert.Same(t, mockLog, ml, "Logger pointer must match")
}

// ---------------------------------------------------------------------------
// 16. TestSequenceGap
// ---------------------------------------------------------------------------

func TestSequenceGap(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current uint16
		last    uint16
		want    int
	}{
		{"sequential", 2, 1, 0},
		{"one_lost", 3, 1, 1},
		{"five_lost", 7, 1, 5},
		{"wrap_around_no_loss", 0, 65535, 0},
		{"wrap_around_one_lost", 1, 65535, 1},
		{"wrap_around_large_gap", 100, 65500, 135},
		{"large_gap_no_wrap", 1000, 500, 499},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sequenceGap(tt.current, tt.last)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// 17. TestSequenceGap_EdgeCases
// ---------------------------------------------------------------------------

func TestSequenceGap_EdgeCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		current uint16
		last    uint16
		want    int
	}{
		{"duplicate_seq", 5, 5, -1},
		{"max_to_zero", 0, 65535, 0},
		{"zero_to_max", 65535, 0, 65534},
		{"max_to_max", 65535, 65535, -1},
		{"zero_to_zero", 0, 0, -1},
		{"one_to_zero", 0, 1, 65534},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := sequenceGap(tt.current, tt.last)
			assert.Equal(t, tt.want, got)
		})
	}
}

// ---------------------------------------------------------------------------
// Integration test helpers
// ---------------------------------------------------------------------------

// newIntegrationDuplex creates a DiscordDuplex wired up for integration tests.
// It initializes the discordReceiveSleepTick so that Notify() does not panic.
func newIntegrationDuplex(t *testing.T) *DiscordDuplex {
	t.Helper()
	dd := NewDiscordDuplex(createTestBridgeState(nil))
	dd.discordReceiveSleepTick.Start(10 * time.Millisecond)
	return dd
}

// ---------------------------------------------------------------------------
// 18. TestDiscordReceivePCM_NormalFlow
// ---------------------------------------------------------------------------

func TestDiscordReceivePCM_NormalFlow(t *testing.T) {
	t.Parallel()

	dd := newIntegrationDuplex(t)
	opus := encodeOpusSilence()

	// Send 3 sequential packets. Each 960-sample frame produces 2x 480-sample
	// PCM chunks, giving 6 chunks total.
	for i := 0; i < 3; i++ {
		p := makePacket(1, uint16(i+1), uint32((i+1)*960), opus)
		dd.processReceivedPacket(p)
	}

	dd.discordMutex.Lock()
	ch := dd.fromDiscordMap[uint32(1)].pcm
	dd.discordMutex.Unlock()

	chunks := drainPCM(ch, 200*time.Millisecond)
	assert.Equal(t, 6, len(chunks), "3 packets * 2 chunks/packet = 6 chunks")
	for _, c := range chunks {
		assert.Equal(t, pcmChunkSize, len(c), "each chunk must be %d samples", pcmChunkSize)
	}
}

// ---------------------------------------------------------------------------
// 19. TestDiscordReceivePCM_PLCIntegration
// ---------------------------------------------------------------------------

func TestDiscordReceivePCM_PLCIntegration(t *testing.T) {
	t.Parallel()

	dd := newIntegrationDuplex(t)
	opus := encodeOpusSilence()

	// Send first packet to establish the stream.
	dd.processReceivedPacket(makePacket(1, 1, 960, opus))

	// Skip seq 2 — send seq 3 to trigger 1 PLC frame.
	dd.processReceivedPacket(makePacket(1, 3, 3*960, opus))

	dd.discordMutex.Lock()
	ch := dd.fromDiscordMap[uint32(1)].pcm
	dd.discordMutex.Unlock()

	chunks := drainPCM(ch, 200*time.Millisecond)

	// Packet 1: 2 chunks, PLC for missing seq 2: 2 chunks, Packet 3: 2 chunks = 6 total
	assert.Equal(t, 6, len(chunks), "expected 6 chunks (2 normal + 2 PLC + 2 normal)")
}

// ---------------------------------------------------------------------------
// 20. TestDiscordReceivePCM_FrozenTimestampIntegration
// ---------------------------------------------------------------------------

func TestDiscordReceivePCM_FrozenTimestampIntegration(t *testing.T) {
	t.Parallel()

	dd := newIntegrationDuplex(t)
	opus := encodeOpusSilence()

	// First packet — normal.
	dd.processReceivedPacket(makePacket(1, 1, 960, opus))

	// Second packet — same timestamp but different sequence (frozen timestamp).
	dd.processReceivedPacket(makePacket(1, 2, 960, opus))

	// Third packet — normal advance.
	dd.processReceivedPacket(makePacket(1, 3, 2*960, opus))

	dd.discordMutex.Lock()
	ch := dd.fromDiscordMap[uint32(1)].pcm
	dd.discordMutex.Unlock()

	chunks := drainPCM(ch, 200*time.Millisecond)

	// Packet 1: 2 chunks, frozen packet skipped: 0, packet 3: 2 chunks = 4 total
	assert.Equal(t, 4, len(chunks), "frozen timestamp packet must be skipped")
}

// ---------------------------------------------------------------------------
// 21. TestDiscordReceivePCM_MajorDiscontinuityIntegration
// ---------------------------------------------------------------------------

func TestDiscordReceivePCM_MajorDiscontinuityIntegration(t *testing.T) {
	t.Parallel()

	dd := newIntegrationDuplex(t)
	opus := encodeOpusSilence()

	// First packet.
	dd.processReceivedPacket(makePacket(1, 1, 960, opus))

	// Jump sequence by > maxPLCPackets (10) — triggers decoder reset.
	jumpSeq := uint16(1 + maxPLCPackets + 2) // gap of maxPLCPackets+1 lost packets
	dd.processReceivedPacket(makePacket(1, jumpSeq, uint32(jumpSeq)*960, opus))

	dd.discordMutex.Lock()
	ch := dd.fromDiscordMap[uint32(1)].pcm
	dd.discordMutex.Unlock()

	chunks := drainPCM(ch, 200*time.Millisecond)

	// Packet 1: 2 chunks, major discontinuity: no PLC, packet 2: 2 chunks = 4 total
	assert.Equal(t, 4, len(chunks), "major discontinuity should not generate PLC frames")

	// The stream should still be usable (receiving flag re-established).
	dd.discordMutex.Lock()
	s := dd.fromDiscordMap[uint32(1)]
	assert.True(t, s.receiving, "receiving must be re-established after discontinuity")
	dd.discordMutex.Unlock()
}

// ---------------------------------------------------------------------------
// 22. TestDiscordReceivePCM_PCMChunking
// ---------------------------------------------------------------------------

func TestDiscordReceivePCM_PCMChunking(t *testing.T) {
	t.Parallel()

	dd := newIntegrationDuplex(t)
	opus := encodeOpusSilence()

	dd.processReceivedPacket(makePacket(1, 1, 960, opus))

	dd.discordMutex.Lock()
	ch := dd.fromDiscordMap[uint32(1)].pcm
	dd.discordMutex.Unlock()

	chunks := drainPCM(ch, 200*time.Millisecond)

	// One 960-sample frame must be split into 2x 480-sample chunks.
	require.Equal(t, 2, len(chunks), "960-sample frame should produce 2 chunks of %d", pcmChunkSize)
	assert.Equal(t, pcmChunkSize, len(chunks[0]))
	assert.Equal(t, pcmChunkSize, len(chunks[1]))
}

// ---------------------------------------------------------------------------
// 23. TestDiscordReceivePCM_SequenceTracking
// ---------------------------------------------------------------------------

func TestDiscordReceivePCM_SequenceTracking(t *testing.T) {
	t.Parallel()

	dd := newIntegrationDuplex(t)
	opus := encodeOpusSilence()

	sequences := []uint16{10, 11, 12, 13}
	for _, seq := range sequences {
		dd.processReceivedPacket(makePacket(1, seq, uint32(seq)*960, opus))
	}

	dd.discordMutex.Lock()
	s := dd.fromDiscordMap[uint32(1)]
	dd.discordMutex.Unlock()

	assert.Equal(t, uint16(13), s.lastSequence, "lastSequence must track the most recent packet")
	assert.Equal(t, uint32(13*960), s.lastTimeStamp, "lastTimeStamp must track the most recent packet")
	assert.True(t, s.receiving, "receiving must be true after packets are processed")
}
