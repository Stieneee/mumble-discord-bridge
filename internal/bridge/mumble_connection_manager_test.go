package bridge

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stieneee/gumble/gumble"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestMumble_DisconnectKickedStopsReconnection verifies kicked status stops reconnection
func TestMumble_DisconnectKickedStopsReconnection(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	config := &gumble.Config{
		Username: "test-bot",
	}

	manager := NewMumbleConnectionManager("localhost:64738", config, nil, logger, emitter)

	// Use InitContext to test handleDisconnectEvent without racing with connection loop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.InitContext(ctx)

	// Simulate kicked event
	kickedEvent := &gumble.DisconnectEvent{
		Type:   gumble.DisconnectKicked,
		String: "You were kicked",
	}
	manager.handleDisconnectEvent(kickedEvent)

	// Status should be ConnectionFailed
	assert.Equal(t, ConnectionFailed, manager.GetStatus())

	// Cleanup
	err := manager.Stop()
	require.NoError(t, err)
}

// TestMumble_DisconnectBannedStopsReconnection verifies banned status stops reconnection
func TestMumble_DisconnectBannedStopsReconnection(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	config := &gumble.Config{
		Username: "test-bot",
	}

	manager := NewMumbleConnectionManager("localhost:64738", config, nil, logger, emitter)

	// Use InitContext to test handleDisconnectEvent without racing with connection loop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.InitContext(ctx)

	// Simulate banned event
	bannedEvent := &gumble.DisconnectEvent{
		Type:   gumble.DisconnectBanned,
		String: "You are banned",
	}
	manager.handleDisconnectEvent(bannedEvent)

	// Status should be ConnectionFailed
	assert.Equal(t, ConnectionFailed, manager.GetStatus())

	err := manager.Stop()
	require.NoError(t, err)
}

// TestMumble_DisconnectErrorReconnects verifies error triggers reconnection
func TestMumble_DisconnectErrorReconnects(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	config := &gumble.Config{
		Username: "test-bot",
	}

	manager := NewMumbleConnectionManager("localhost:64738", config, nil, logger, emitter)

	// Use InitContext to test handleDisconnectEvent without racing with connection loop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.InitContext(ctx)

	// Simulate error disconnect event
	errorEvent := &gumble.DisconnectEvent{
		Type:   gumble.DisconnectError,
		String: "Connection error",
	}
	manager.handleDisconnectEvent(errorEvent)

	// Status should be ConnectionReconnecting (not Failed)
	assert.Equal(t, ConnectionReconnecting, manager.GetStatus())

	err := manager.Stop()
	require.NoError(t, err)
}

// TestMumble_DisconnectEventAfterStop ensures no panic sending to closed channel
func TestMumble_DisconnectEventAfterStop(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	config := &gumble.Config{
		Username: "test-bot",
	}

	manager := NewMumbleConnectionManager("localhost:64738", config, nil, logger, emitter)

	// Use InitContext to avoid connection loop racing with Stop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.InitContext(ctx)

	// Stop the manager
	err := manager.Stop()
	require.NoError(t, err)

	// Now try to send disconnect event - should not panic
	assert.NotPanics(t, func() {
		manager.OnDisconnect(&gumble.DisconnectEvent{
			Type:   gumble.DisconnectError,
			String: "Post-stop event",
		})
	})
}

// TestMumble_ConcurrentDisconnectEvents tests multiple disconnect events while shutting down
func TestMumble_ConcurrentDisconnectEvents(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	config := &gumble.Config{
		Username: "test-bot",
	}

	manager := NewMumbleConnectionManager("localhost:64738", config, nil, logger, emitter)

	// Use InitContext to avoid connection loop racing with Stop
	ctx, cancel := context.WithCancel(context.Background())

	manager.InitContext(ctx)

	// Start concurrent disconnect events
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			manager.OnDisconnect(&gumble.DisconnectEvent{
				Type:   gumble.DisconnectError,
				String: "Concurrent disconnect",
			})
		}()
	}

	// Stop while events are being sent
	cancel()
	err := manager.Stop()
	require.NoError(t, err)

	// Wait for all goroutines to finish
	wg.Wait()
}

// TestMumble_RapidReconnection tests multiple connect/disconnect cycles
func TestMumble_RapidReconnection(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	config := &gumble.Config{
		Username: "test-bot",
	}

	// Run 10 rapid start/stop cycles
	for i := 0; i < 10; i++ {
		manager := NewMumbleConnectionManager("localhost:64738", config, nil, logger, emitter)

		// Use InitContext to avoid connection loop racing with Stop
		ctx, cancel := context.WithCancel(context.Background())

		manager.InitContext(ctx)

		// Brief operation
		time.Sleep(10 * time.Millisecond)

		cancel()
		err := manager.Stop()
		require.NoError(t, err, "Failed to stop on cycle %d", i)
	}
}

// TestMumble_StopIdempotent ensures multiple Stop calls are safe
func TestMumble_StopIdempotent(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	config := &gumble.Config{
		Username: "test-bot",
	}

	manager := NewMumbleConnectionManager("localhost:64738", config, nil, logger, emitter)

	// Use InitContext to avoid connection loop racing with Stop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.InitContext(ctx)

	// Multiple stops should be safe
	for i := 0; i < 5; i++ {
		err := manager.Stop()
		require.NoError(t, err, "Stop call %d failed", i)
	}
}

// TestMumble_GetClientThreadSafe tests concurrent GetClient calls
func TestMumble_GetClientThreadSafe(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	config := &gumble.Config{
		Username: "test-bot",
	}

	manager := NewMumbleConnectionManager("localhost:64738", config, nil, logger, emitter)

	// Use InitContext to avoid connection loop racing with Stop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.InitContext(ctx)

	// Concurrent GetClient calls
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = manager.GetClient()
		}()
	}

	wg.Wait()

	err := manager.Stop()
	require.NoError(t, err)
}

// TestMumble_UpdateAddressThreadSafe tests concurrent address updates
func TestMumble_UpdateAddressThreadSafe(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	config := &gumble.Config{
		Username: "test-bot",
	}

	manager := NewMumbleConnectionManager("localhost:64738", config, nil, logger, emitter)

	// Use InitContext to avoid connection loop racing with Stop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.InitContext(ctx)

	// Concurrent address updates
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			err := manager.UpdateAddress("localhost:" + string(rune(64738+idx)))
			_ = err
			_ = manager.GetAddress()
		}(i)
	}

	wg.Wait()

	err := manager.Stop()
	require.NoError(t, err)
}

// TestMumble_HandleDisconnectEventTypes tests all disconnect event types
func TestMumble_HandleDisconnectEventTypes(t *testing.T) {
	testCases := []struct {
		name           string
		disconnectType gumble.DisconnectType
		expectedStatus ConnectionStatus
	}{
		{"Error", gumble.DisconnectError, ConnectionReconnecting},
		{"Kicked", gumble.DisconnectKicked, ConnectionFailed},
		{"Banned", gumble.DisconnectBanned, ConnectionFailed},
		{"User", gumble.DisconnectUser, ConnectionReconnecting},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			logger := NewMockLogger()
			emitter := NewMockBridgeEventEmitter()

			config := &gumble.Config{
				Username: "test-bot",
			}

			manager := NewMumbleConnectionManager("localhost:64738", config, nil, logger, emitter)

			// Use InitContext to test handleDisconnectEvent without racing with connection loop
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()

			manager.InitContext(ctx)

			// Handle disconnect event
			event := &gumble.DisconnectEvent{
				Type:   tc.disconnectType,
				String: "Test disconnect",
			}
			manager.handleDisconnectEvent(event)

			assert.Equal(t, tc.expectedStatus, manager.GetStatus())

			err := manager.Stop()
			require.NoError(t, err)
		})
	}
}

// TestMumble_ConfigUpdateThreadSafe tests concurrent config updates
func TestMumble_ConfigUpdateThreadSafe(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	config := &gumble.Config{
		Username: "test-bot",
	}

	manager := NewMumbleConnectionManager("localhost:64738", config, nil, logger, emitter)

	// Use InitContext to avoid connection loop racing with Stop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.InitContext(ctx)

	// Concurrent config updates and reads
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(2)
		go func(idx int) {
			defer wg.Done()
			newConfig := &gumble.Config{
				Username: "test-bot-" + string(rune('0'+idx)),
			}
			_ = manager.UpdateConfig(newConfig) //nolint:errcheck // test setup
		}(i)
		go func() {
			defer wg.Done()
			_ = manager.GetConfig()
		}()
	}

	wg.Wait()

	err := manager.Stop()
	require.NoError(t, err)
}

// TestMumble_EventListenerMethods tests the gumble.EventListener implementation
func TestMumble_EventListenerMethods(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	config := &gumble.Config{
		Username: "test-bot",
	}

	manager := NewMumbleConnectionManager("localhost:64738", config, nil, logger, emitter)

	// All these methods should not panic when called
	assert.NotPanics(t, func() {
		manager.OnConnect(&gumble.ConnectEvent{})
		manager.OnTextMessage(&gumble.TextMessageEvent{})
		manager.OnUserChange(&gumble.UserChangeEvent{})
		manager.OnChannelChange(&gumble.ChannelChangeEvent{})
		manager.OnPermissionDenied(&gumble.PermissionDeniedEvent{})
		manager.OnUserList(&gumble.UserListEvent{})
		manager.OnACL(&gumble.ACLEvent{})
		manager.OnBanList(&gumble.BanListEvent{})
		manager.OnContextActionChange(&gumble.ContextActionChangeEvent{})
		manager.OnServerConfig(&gumble.ServerConfigEvent{})
	})
}

// TestMumble_DisconnectChannelBuffer tests the buffered disconnect channel
func TestMumble_DisconnectChannelBuffer(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	config := &gumble.Config{
		Username: "test-bot",
	}

	manager := NewMumbleConnectionManager("localhost:64738", config, nil, logger, emitter)

	// Use InitContext to avoid connection loop racing with Stop
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	manager.InitContext(ctx)

	// Send multiple disconnect events rapidly - should not block
	for i := 0; i < 10; i++ {
		manager.OnDisconnect(&gumble.DisconnectEvent{
			Type:   gumble.DisconnectError,
			String: "Rapid event",
		})
	}

	// Should complete without blocking
	err := manager.Stop()
	require.NoError(t, err)
}

// TestMumble_GetAudioOutgoingNilClient tests GetAudioOutgoing with nil client
func TestMumble_GetAudioOutgoingNilClient(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	config := &gumble.Config{
		Username: "test-bot",
	}

	manager := NewMumbleConnectionManager("localhost:64738", config, nil, logger, emitter)

	// Without starting, client should be nil
	audioOut := manager.GetAudioOutgoing()
	assert.Nil(t, audioOut)
}

// TestMumble_StatusTransitionsUnderLoad tests status changes under concurrent load
func TestMumble_StatusTransitionsUnderLoad(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()

	config := &gumble.Config{
		Username: "test-bot",
	}

	manager := NewMumbleConnectionManager("localhost:64738", config, nil, logger, emitter)

	// Use InitContext to avoid connection loop racing with Stop
	ctx, cancel := context.WithCancel(context.Background())

	manager.InitContext(ctx)

	var reads, writes int32
	var wg sync.WaitGroup

	// Concurrent status reads
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				_ = manager.GetStatus()
				_ = manager.IsConnected()
				atomic.AddInt32(&reads, 1)
			}
		}()
	}

	// Concurrent status changes via disconnect events
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10; j++ {
				manager.OnDisconnect(&gumble.DisconnectEvent{
					Type:   gumble.DisconnectError,
					String: "Load test",
				})
				atomic.AddInt32(&writes, 1)
			}
		}()
	}

	wg.Wait()
	cancel()
	err := manager.Stop()
	require.NoError(t, err)

	t.Logf("Completed %d reads and %d writes", atomic.LoadInt32(&reads), atomic.LoadInt32(&writes))
}
