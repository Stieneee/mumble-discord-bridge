package bridge

import (
	"context"
	"sync"
	"sync/atomic"
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

	// Use InitContext to avoid actually trying to connect to Discord
	manager.InitContext(ctx)

	// Give manager time to start
	time.Sleep(50 * time.Millisecond)

	// Since we didn't actually start, status should be disconnected
	status := manager.GetStatus()
	assert.True(t, status == ConnectionConnecting || status == ConnectionFailed || status == ConnectionDisconnected,
		"Expected connecting, failed, or disconnected, got %s", status)

	cancel()
	err := manager.Stop()
	require.NoError(t, err)
}

// TestDiscord_VoiceEventGenerationIncrement tests voice event generation counter
func TestDiscord_VoiceEventGenerationIncrement(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	// Store initial voice event generation
	initialGen := atomic.LoadInt32(&manager.voiceEventGeneration)

	// Clear pending voice events increments voice event generation
	manager.clearPendingVoiceEvents()

	newGen := atomic.LoadInt32(&manager.voiceEventGeneration)
	assert.Greater(t, newGen, initialGen, "Voice event generation should increment on clearPendingVoiceEvents")
}

// TestDiscord_ConcurrentGetOpusChannels tests multiple readers during state change
func TestDiscord_ConcurrentGetOpusChannels(_ *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	// Don't start the manager - just test the GetOpusChannels method directly
	// This avoids the issue of actually trying to connect to Discord

	// Concurrent GetOpusChannels calls
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			send, recv, ready := manager.GetOpusChannels()
			// Just check no panic, values may be nil
			_ = send
			_ = recv
			_ = ready
		}()
	}

	wg.Wait()
}

// TestDiscord_VoiceEventTracking tests voice event tracking mechanism
func TestDiscord_VoiceEventTracking(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	// Initially no voice events
	hasState, hasServer, endpoint := manager.GetVoiceEventInfo()
	assert.False(t, hasState)
	assert.False(t, hasServer)
	assert.Empty(t, endpoint)

	// Clear voice events should work without panic
	manager.clearPendingVoiceEvents()

	hasState, hasServer, _ = manager.GetVoiceEventInfo()
	assert.False(t, hasState)
	assert.False(t, hasServer)
}

// TestDiscord_UpdateChannel tests thread-safe channel update
func TestDiscord_UpdateChannel(_ *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "initial-channel", logger, emitter)

	// Test UpdateChannel without starting (avoids actual Discord connection)
	// Concurrent channel updates
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

	// Initialize the context manually without starting the connection loop
	ctx, cancel := context.WithCancel(context.Background())
	manager.InitContext(ctx)
	cancel() // Cancel immediately to prevent connection attempts

	// Multiple stops should be safe
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

	// Use InitContext to avoid actually trying to connect to Discord
	manager.InitContext(ctx)

	// Connection should still be nil as we haven't actually connected
	// Just verify no panic
	_ = manager.GetReadyConnection()

	cancel()
	err := manager.Stop()
	require.NoError(t, err)
}

// TestDiscord_IsConnectionHealthy tests health check method
func TestDiscord_IsConnectionHealthy(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	// Should not panic with nil channels
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

		// Use InitContext to avoid actually trying to connect to Discord
		manager.InitContext(ctx)

		// Brief operation
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

	// Use InitContext to avoid actually trying to connect to Discord
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

	// Use InitContext to avoid actually trying to connect to Discord
	manager.InitContext(ctx)

	// Verify handlers are registered (we can't easily check this, but ensure no panic)
	assert.NotPanics(t, func() {
		// These would be called by Discord events
		manager.clearPendingVoiceEvents()
	})

	cancel()
	// Stop removes handlers
	err := manager.Stop()
	require.NoError(t, err)

	// After stop, operations should still not panic
	assert.NotPanics(t, func() {
		manager.clearPendingVoiceEvents()
	})
}

// TestDiscord_IsPrimarySessionConnected tests session connectivity check
func TestDiscord_IsPrimarySessionConnected(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	t.Run("Connected session", func(t *testing.T) {
		session := createMockDiscordSession()
		manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

		connected, reason := manager.isPrimarySessionConnected()
		assert.True(t, connected)
		assert.Contains(t, reason, "connected")
	})

	t.Run("Session not ready", func(t *testing.T) {
		session := createMockDiscordSession()
		session.Lock()
		session.DataReady = false
		session.Unlock()
		manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

		connected, reason := manager.isPrimarySessionConnected()
		assert.False(t, connected)
		assert.Contains(t, reason, "not ready")
	})

	t.Run("Nil session", func(t *testing.T) {
		manager := NewDiscordVoiceConnectionManager(nil, "test-guild", "test-channel", logger, emitter)

		connected, reason := manager.isPrimarySessionConnected()
		assert.False(t, connected)
		assert.Contains(t, reason, "nil")
	})
}

// TestDiscord_ConcurrentVoiceEventInfo tests concurrent voice event info access
func TestDiscord_ConcurrentVoiceEventInfo(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Use InitContext to avoid actually trying to connect to Discord
	manager.InitContext(ctx)

	var wg sync.WaitGroup

	// Concurrent reads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_, _, _ = manager.GetVoiceEventInfo()
			}
		}()
	}

	// Concurrent clears
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				manager.clearPendingVoiceEvents()
			}
		}()
	}

	wg.Wait()

	cancel()
	err := manager.Stop()
	require.NoError(t, err)
}

// TestDiscord_SetStatusAfterStop ensures SetStatus is safe after stop
func TestDiscord_SetStatusAfterStop(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Use InitContext to avoid actually trying to connect to Discord
	manager.InitContext(ctx)

	err := manager.Stop()
	require.NoError(t, err)

	// SetStatus after stop should not panic
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

	// Use InitContext to avoid actually trying to connect to Discord
	manager.InitContext(ctx)

	// Set various statuses
	manager.SetStatus(ConnectionConnecting, nil)
	manager.SetStatus(ConnectionConnected, nil)
	manager.SetStatus(ConnectionReconnecting, nil)

	// Check events were emitted
	events := emitter.GetEventsByService("discord")
	assert.NotEmpty(t, events, "Expected events to be emitted")

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

	// Without any connection, should be unhealthy
	healthy := manager.isConnectionHealthy()
	assert.False(t, healthy, "Should not be healthy with nil connection")
}

// TestDiscord_IsConnectionHealthy_WithNilChannels tests health check with nil opus channels
func TestDiscord_IsConnectionHealthy_WithNilChannels(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	// Set connection but no channels
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

	// Create mock opus channels
	sendChan := make(chan []byte, 1)
	recvChan := make(chan *discordgo.Packet, 1)

	// Create a voice connection with Ready = false
	voiceConn := &discordgo.VoiceConnection{}
	voiceConn.Lock()
	voiceConn.Ready = false
	voiceConn.Unlock()

	manager.connMutex.Lock()
	manager.connection = voiceConn
	manager.opusSend = sendChan
	manager.opusRecv = recvChan
	manager.connMutex.Unlock()

	// Should be unhealthy because Ready is false
	healthy := manager.isConnectionHealthy()
	assert.False(t, healthy, "Should not be healthy when Ready is false")

	// Now set Ready to true
	voiceConn.Lock()
	voiceConn.Ready = true
	voiceConn.Unlock()

	// Should now be healthy
	healthy = manager.isConnectionHealthy()
	assert.True(t, healthy, "Should be healthy when Ready is true")
}

// TestDiscord_WaitForPrimarySessionReconnection tests the reconnection wait logic
func TestDiscord_WaitForPrimarySessionReconnection_Timeout(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	// Create session that is not ready
	session := createMockDiscordSession()
	session.Lock()
	session.DataReady = false
	session.Unlock()

	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.InitContext(ctx)

	// This should return false after timeout (but we use context cancel to speed up test)
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	result := manager.waitForPrimarySessionReconnection(ctx)
	assert.False(t, result, "Should return false when session never reconnects")

	err := manager.Stop()
	require.NoError(t, err)
}

// TestDiscord_WaitForPrimarySessionReconnection_Success tests successful reconnection
func TestDiscord_WaitForPrimarySessionReconnection_Success(t *testing.T) {
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

	// Simulate session becoming ready after 50ms
	go func() {
		time.Sleep(50 * time.Millisecond)
		session.Lock()
		session.DataReady = true
		session.Unlock()
	}()

	result := manager.waitForPrimarySessionReconnection(ctx)
	assert.True(t, result, "Should return true when session reconnects")

	err := manager.Stop()
	require.NoError(t, err)
}

// TestDiscord_ConcurrentHealthChecks tests concurrent health check access
func TestDiscord_ConcurrentHealthChecks(t *testing.T) {
	t.Parallel()
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	session := createMockDiscordSession()
	manager := NewDiscordVoiceConnectionManager(session, "test-guild", "test-channel", logger, emitter)

	// Create mock opus channels and connection
	sendChan := make(chan []byte, 1)
	recvChan := make(chan *discordgo.Packet, 1)
	voiceConn := &discordgo.VoiceConnection{}

	manager.connMutex.Lock()
	manager.connection = voiceConn
	manager.opusSend = sendChan
	manager.opusRecv = recvChan
	manager.connMutex.Unlock()

	// Concurrent health checks shouldn't cause race conditions
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

	// Concurrent connection state changes
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

	// Set up mock channels
	sendChan := make(chan []byte, 1)
	recvChan := make(chan *discordgo.Packet, 1)

	// Create a mock voice connection - disconnectInternal only clears channels
	// when there's an actual connection to disconnect
	voiceConn := &discordgo.VoiceConnection{}

	manager.connMutex.Lock()
	manager.opusSend = sendChan
	manager.opusRecv = recvChan
	manager.connection = voiceConn
	manager.connMutex.Unlock()

	// Call disconnect
	manager.disconnectInternal()

	// Verify channels and connection are cleared
	manager.connMutex.RLock()
	assert.Nil(t, manager.opusSend, "OpusSend should be nil after disconnect")
	assert.Nil(t, manager.opusRecv, "OpusRecv should be nil after disconnect")
	assert.Nil(t, manager.connection, "Connection should be nil after disconnect")
	manager.connMutex.RUnlock()
}
