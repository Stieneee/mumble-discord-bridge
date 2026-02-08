package bridge

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/bwmarrin/discordgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// createMockDiscordSession creates a mock Discord session for testing
func createMockDiscordSession() *discordgo.Session {
	session := &discordgo.Session{
		State:     discordgo.NewState(),
		DataReady: true,
	}
	session.State.User = &discordgo.User{
		ID:       "test-bot-id",
		Username: "TestBot",
	}
	return session
}

// TestDiscord_SessionNotReadyWaits tests that manager waits for session ready
func TestDiscord_SessionNotReadyWaits(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	session.Lock()
	session.DataReady = false
	session.Unlock()

	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.InitContext(ctx)

	time.Sleep(50 * time.Millisecond)

	status := manager.GetStatus()
	assert.True(t, status == ConnectionConnecting || status == ConnectionFailed || status == ConnectionDisconnected,
		"Expected connecting, failed, or disconnected, got %s", status)

	cancel()
	err := manager.Stop()
	require.NoError(t, err)
}

// TestDiscord_ConcurrentGetOpusChannels tests multiple readers during state change
func TestDiscord_ConcurrentGetOpusChannels(_ *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			send, recv, ready := manager.GetOpusChannels()
			_ = send
			_ = recv
			_ = ready
		}()
	}

	wg.Wait()
}

// TestDiscord_UpdateChannel tests thread-safe channel update
func TestDiscord_UpdateChannel(_ *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "initial-channel", logger, emitter)

	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			manager.UpdateChannel("channel-" + string(rune('0'+idx%10)))
		}(i)
	}

	wg.Wait()
}

// TestDiscord_StopIdempotent ensures multiple Stop calls are safe
func TestDiscord_StopIdempotent(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	ctx, cancel := context.WithCancel(context.Background())
	manager.InitContext(ctx)
	cancel()

	for i := 0; i < 5; i++ {
		err := manager.Stop()
		require.NoError(t, err, "Stop call %d failed", i)
	}
}

// TestDiscord_GetReadyConnection tests GetReadyConnection method
func TestDiscord_GetReadyConnection(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	// Before starting, should return nil
	conn := manager.GetReadyConnection()
	assert.Nil(t, conn)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.InitContext(ctx)

	_ = manager.GetReadyConnection()

	cancel()
	err := manager.Stop()
	require.NoError(t, err)
}

// TestDiscord_GetReadyConnection_ChecksReady verifies GetReadyConnection checks Ready flag
func TestDiscord_GetReadyConnection_ChecksReady(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	sendChan := make(chan []byte, 1)
	recvChan := make(chan *discordgo.Packet, 1)
	voiceConn := &discordgo.VoiceConnection{}

	// Set Ready=false
	voiceConn.Lock()
	voiceConn.Ready = false
	voiceConn.Unlock()

	manager.connMutex.Lock()
	manager.connection = voiceConn
	manager.opusSend = sendChan
	manager.opusRecv = recvChan
	manager.connMutex.Unlock()

	// Should return nil when Ready=false
	assert.Nil(t, manager.GetReadyConnection(), "Should return nil when Ready is false")

	// Set Ready=true
	voiceConn.Lock()
	voiceConn.Ready = true
	voiceConn.Unlock()

	// Should return connection when Ready=true
	assert.NotNil(t, manager.GetReadyConnection(), "Should return connection when Ready is true")
}

// TestDiscord_IsConnectionHealthy tests health check method
func TestDiscord_IsConnectionHealthy(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	healthy := manager.isConnectionHealthy()
	assert.False(t, healthy, "Should not be healthy without channels")
}

// TestDiscord_RapidStartStop tests multiple start/stop cycles
func TestDiscord_RapidStartStop(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()

	for i := 0; i < 10; i++ {
		manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

		ctx, cancel := context.WithCancel(context.Background())

		manager.InitContext(ctx)

		time.Sleep(10 * time.Millisecond)

		cancel()
		err := manager.Stop()
		require.NoError(t, err, "Failed to stop on cycle %d", i)
	}
}

// TestDiscord_ConcurrentStatusReads tests concurrent status access
func TestDiscord_ConcurrentStatusReads(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.InitContext(ctx)

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = manager.GetStatus()
				_ = manager.IsConnected()
			}
		}()
	}

	wg.Wait()

	cancel()
	err := manager.Stop()
	require.NoError(t, err)
}

// TestDiscord_EventHandlerRegistration tests voice event handler lifecycle
func TestDiscord_EventHandlerRegistration(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.InitContext(ctx)

	cancel()
	err := manager.Stop()
	require.NoError(t, err)
}

// TestDiscord_IsConnectionHealthy_WithNilConnection tests health check with nil connection
func TestDiscord_IsConnectionHealthy_WithNilConnection(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	healthy := manager.isConnectionHealthy()
	assert.False(t, healthy, "Should not be healthy with nil connection")
}

// TestDiscord_IsConnectionHealthy_WithNilChannels tests health check with nil opus channels
func TestDiscord_IsConnectionHealthy_WithNilChannels(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	manager.connMutex.Lock()
	manager.connection = &discordgo.VoiceConnection{}
	manager.opusSend = nil
	manager.opusRecv = nil
	manager.connMutex.Unlock()

	healthy := manager.isConnectionHealthy()
	assert.False(t, healthy, "Should not be healthy without opus channels")
}

// TestDiscord_IsConnectionHealthy_ChecksReadyFlag tests that health check verifies Ready flag
func TestDiscord_IsConnectionHealthy_ChecksReadyFlag(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	sendChan := make(chan []byte, 1)
	recvChan := make(chan *discordgo.Packet, 1)

	voiceConn := &discordgo.VoiceConnection{}
	voiceConn.Lock()
	voiceConn.Ready = false
	voiceConn.Unlock()

	manager.connMutex.Lock()
	manager.connection = voiceConn
	manager.opusSend = sendChan
	manager.opusRecv = recvChan
	manager.connMutex.Unlock()

	healthy := manager.isConnectionHealthy()
	assert.False(t, healthy, "Should not be healthy when Ready is false")

	voiceConn.Lock()
	voiceConn.Ready = true
	voiceConn.Unlock()

	healthy = manager.isConnectionHealthy()
	assert.True(t, healthy, "Should be healthy when Ready is true")
}

// TestDiscord_ConcurrentHealthChecks tests concurrent health check access
func TestDiscord_ConcurrentHealthChecks(t *testing.T) {
	t.Parallel()
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	sendChan := make(chan []byte, 1)
	recvChan := make(chan *discordgo.Packet, 1)
	voiceConn := &discordgo.VoiceConnection{}

	manager.connMutex.Lock()
	manager.connection = voiceConn
	manager.opusSend = sendChan
	manager.opusRecv = recvChan
	manager.connMutex.Unlock()

	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 50; j++ {
				_ = manager.isConnectionHealthy()
			}
		}()
	}

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				voiceConn.Lock()
				voiceConn.Ready = !voiceConn.Ready
				voiceConn.Unlock()
			}
		}()
	}

	wg.Wait()
}

// TestDiscord_DisconnectInternalClearsChannels tests that disconnect clears channels
func TestDiscord_DisconnectInternalClearsChannels(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	sendChan := make(chan []byte, 1)
	recvChan := make(chan *discordgo.Packet, 1)
	voiceConn := &discordgo.VoiceConnection{}

	manager.connMutex.Lock()
	manager.opusSend = sendChan
	manager.opusRecv = recvChan
	manager.connection = voiceConn
	manager.connMutex.Unlock()

	manager.disconnectInternal()

	manager.connMutex.RLock()
	assert.Nil(t, manager.opusSend, "OpusSend should be nil after disconnect")
	assert.Nil(t, manager.opusRecv, "OpusRecv should be nil after disconnect")
	assert.Nil(t, manager.connection, "Connection should be nil after disconnect")
	manager.connMutex.RUnlock()
}

// TestDiscord_WaitForSessionReady_LockedMutex tests that waitForSessionReady()
// does not block forever when the session mutex is write-locked (simulating a
// deadlocked session). It should time out normally via TryRLock.
func TestDiscord_WaitForSessionReady_LockedMutex(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()

	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.InitContext(ctx)

	session.Lock()
	defer session.Unlock()

	done := make(chan error, 1)
	go func() {
		done <- manager.waitForSessionReady(500 * time.Millisecond)
	}()

	select {
	case err := <-done:
		assert.Error(t, err, "should return timeout error")
		assert.Contains(t, err.Error(), "timeout")
	case <-time.After(3 * time.Second):
		t.Fatal("waitForSessionReady() blocked on locked mutex — TryRLock not working")
	}
}

// TestDiscord_SetStatusAfterStop ensures SetStatus is safe after stop
func TestDiscord_SetStatusAfterStop(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	manager.InitContext(ctx)

	err := manager.Stop()
	require.NoError(t, err)

	assert.NotPanics(t, func() {
		manager.SetStatus(ConnectionConnected, nil)
	})
}

// TestDiscord_EventEmission tests that events are emitted properly
func TestDiscord_EventEmission(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.InitContext(ctx)

	manager.SetStatus(ConnectionConnecting, nil)
	manager.SetStatus(ConnectionConnected, nil)
	manager.SetStatus(ConnectionReconnecting, nil)

	events := emitter.GetEventsByService("discord")
	assert.NotEmpty(t, events, "Expected events to be emitted")

	cancel()
	err := manager.Stop()
	require.NoError(t, err)
}

// TestDiscord_GetOpusChannels_ChecksReady tests that GetOpusChannels returns not-ready
// when the voice connection is down (Ready=false)
func TestDiscord_GetOpusChannels_ChecksReady(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	sendChan := make(chan []byte, 1)
	recvChan := make(chan *discordgo.Packet, 1)
	voiceConn := &discordgo.VoiceConnection{}

	// Set Ready=false
	voiceConn.Lock()
	voiceConn.Ready = false
	voiceConn.Unlock()

	manager.connMutex.Lock()
	manager.connection = voiceConn
	manager.opusSend = sendChan
	manager.opusRecv = recvChan
	manager.connMutex.Unlock()

	// Should return not-ready when Ready=false
	_, _, ready := manager.GetOpusChannels()
	assert.False(t, ready, "GetOpusChannels should return not-ready when Ready is false")

	// Set Ready=true
	voiceConn.Lock()
	voiceConn.Ready = true
	voiceConn.Unlock()

	// Should return ready when Ready=true
	send, recv, ready := manager.GetOpusChannels()
	assert.True(t, ready, "GetOpusChannels should return ready when Ready is true")
	assert.NotNil(t, send)
	assert.NotNil(t, recv)
}

// TestDiscord_MonitorConnection_ContextCancel tests that monitor exits on context cancellation
func TestDiscord_MonitorConnection_ContextCancel(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	ctx, cancel := context.WithCancel(context.Background())
	manager.InitContext(ctx)

	done := make(chan struct{})
	go func() {
		manager.monitorConnection(ctx)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Monitor exited cleanly
	case <-time.After(2 * time.Second):
		t.Fatal("monitorConnection did not exit after context cancellation")
	}
}
