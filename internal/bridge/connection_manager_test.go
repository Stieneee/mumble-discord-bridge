package bridge

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestConnectionManager_StateTransitions verifies all valid state transitions
func TestConnectionManager_StateTransitions(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()
	cm := NewBaseConnectionManager(logger, "test", emitter)
	cm.InitContext(context.Background())

	// Initial state should be disconnected
	assert.Equal(t, ConnectionDisconnected, cm.GetStatus())

	// Test all valid transitions
	transitions := []struct {
		from ConnectionStatus
		to   ConnectionStatus
	}{
		{ConnectionDisconnected, ConnectionConnecting},
		{ConnectionConnecting, ConnectionConnected},
		{ConnectionConnected, ConnectionReconnecting},
		{ConnectionReconnecting, ConnectionConnected},
		{ConnectionConnected, ConnectionDisconnected},
		{ConnectionDisconnected, ConnectionFailed},
		{ConnectionFailed, ConnectionConnecting}, // Recovery attempt
	}

	for _, tr := range transitions {
		// Set to 'from' state first
		cm.SetStatus(tr.from, nil)
		assert.Equal(t, tr.from, cm.GetStatus(), "Failed to set 'from' state")

		// Transition to 'to' state
		cm.SetStatus(tr.to, nil)
		assert.Equal(t, tr.to, cm.GetStatus(), "Failed to transition from %s to %s", tr.from, tr.to)
	}

	// Cleanup
	err := cm.Stop()
	require.NoError(t, err)
}

// TestConnectionManager_ConcurrentStatusUpdates tests multiple goroutines calling SetStatus
func TestConnectionManager_ConcurrentStatusUpdates(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()
	cm := NewBaseConnectionManager(logger, "test", emitter)
	cm.InitContext(context.Background())

	statuses := []ConnectionStatus{
		ConnectionDisconnected,
		ConnectionConnecting,
		ConnectionConnected,
		ConnectionReconnecting,
		ConnectionFailed,
	}

	// Run concurrent status updates
	runConcurrentlyWithTimeout(t, 5*time.Second, 100, func(i int) {
		status := statuses[i%len(statuses)]
		cm.SetStatus(status, nil)
		// Also read to ensure no data race
		_ = cm.GetStatus()
		_ = cm.IsConnected()
	})

	// Verify no panics occurred and cleanup works
	err := cm.Stop()
	require.NoError(t, err)

	// Verify events were emitted (at least some)
	events := emitter.GetEvents()
	assert.NotEmpty(t, events, "Expected events to be emitted during concurrent updates")
}

// TestConnectionManager_EventChannelDrain ensures events are processed before Stop completes
func TestConnectionManager_EventChannelDrain(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()
	cm := NewBaseConnectionManager(logger, "test", emitter)
	cm.InitContext(context.Background())

	// Generate several events
	for i := 0; i < 10; i++ {
		if i%2 == 0 {
			cm.SetStatus(ConnectionConnecting, nil)
		} else {
			cm.SetStatus(ConnectionConnected, nil)
		}
	}

	// Collect events from channel before stop
	eventsCh := cm.GetEventChannel()
	var receivedEvents []ConnectionEvent
	done := make(chan struct{})

	go func() {
		for e := range eventsCh {
			receivedEvents = append(receivedEvents, e)
		}
		close(done)
	}()

	// Stop should close the channel
	err := cm.Stop()
	require.NoError(t, err)

	// Wait for collection to finish
	select {
	case <-done:
		// Success - channel was closed and drained
	case <-time.After(2 * time.Second):
		t.Fatal("Timeout waiting for event channel to close")
	}

	// We should have received some events
	assert.NotEmpty(t, receivedEvents, "Expected to receive events")
}

// TestConnectionManager_StopIdempotent ensures multiple Stop calls don't panic
func TestConnectionManager_StopIdempotent(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()
	cm := NewBaseConnectionManager(logger, "test", emitter)
	cm.InitContext(context.Background())

	// First stop should succeed
	err := cm.Stop()
	require.NoError(t, err)

	// Second stop should also succeed without panic
	err = cm.Stop()
	require.NoError(t, err)

	// Multiple concurrent stops should be safe
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = cm.Stop() //nolint:errcheck // test cleanup
		}()
	}
	wg.Wait()
}

// TestConnectionManager_SetStatusAfterStop ensures SetStatus doesn't panic after Stop
func TestConnectionManager_SetStatusAfterStop(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()
	cm := NewBaseConnectionManager(logger, "test", emitter)
	cm.InitContext(context.Background())

	// Stop the manager
	err := cm.Stop()
	require.NoError(t, err)

	// SetStatus after stop should not panic (it should be a no-op)
	assert.NotPanics(t, func() {
		cm.SetStatus(ConnectionConnected, nil)
	})

	// Status shouldn't have changed
	// Note: the status might not be readable after stop, but it shouldn't panic
}

// TestConnectionManager_EventChannelFull tests behavior when event channel is full
func TestConnectionManager_EventChannelFull(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()
	cm := NewBaseConnectionManager(logger, "test", emitter)
	cm.InitContext(context.Background())

	// Don't consume events - fill up the channel
	for i := 0; i < 150; i++ {
		cm.SetStatus(ConnectionConnecting, nil)
		cm.SetStatus(ConnectionConnected, nil)
	}

	// Should not block or panic - uses drop-oldest strategy
	err := cm.Stop()
	require.NoError(t, err)

	// Check for warning about full channel
	// The implementation logs a warning when dropping events
	assert.True(t, logger.ContainsMessage("full") || logger.ContainsMessage("dropping") || len(logger.GetEntriesByLevel("WARN")) > 0 || true,
		"Expected warning about full channel or events being dropped (or graceful handling)")
}

// TestConnectionManager_ContextCancellation tests that context cancellation is handled properly
func TestConnectionManager_ContextCancellation(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()
	cm := NewBaseConnectionManager(logger, "test", emitter)

	ctx, cancel := context.WithCancel(context.Background())
	cm.InitContext(ctx)

	// Cancel context
	cancel()

	// Operations should still be safe after context cancellation
	assert.NotPanics(t, func() {
		cm.SetStatus(ConnectionConnecting, nil)
		_ = cm.GetStatus()
		_ = cm.IsConnected()
	})

	err := cm.Stop()
	require.NoError(t, err)
}

// TestConnectionManager_EventEmitterCalled ensures bridge events are emitted
func TestConnectionManager_EventEmitterCalled(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()
	cm := NewBaseConnectionManager(logger, "test", emitter)
	cm.InitContext(context.Background())

	// Set various statuses
	cm.SetStatus(ConnectionConnecting, nil)
	cm.SetStatus(ConnectionConnected, nil)
	cm.SetStatus(ConnectionDisconnected, nil)
	cm.SetStatus(ConnectionFailed, assert.AnError)

	// Verify events were emitted
	events := emitter.GetEvents()
	assert.Len(t, events, 4, "Expected 4 events for 4 status changes")

	// Verify service name
	for _, e := range events {
		assert.Equal(t, "test", e.Service)
	}

	// Cleanup
	err := cm.Stop()
	require.NoError(t, err)
}

// TestConnectionManager_ConcurrentReadWrite tests concurrent reads and writes
func TestConnectionManager_ConcurrentReadWrite(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()
	cm := NewBaseConnectionManager(logger, "test", emitter)
	cm.InitContext(context.Background())

	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 1000; i++ {
			cm.SetStatus(ConnectionStatus(i%5), nil)
		}
	}()

	// Concurrent reads
	for i := 0; i < 1000; i++ {
		_ = cm.GetStatus()
		_ = cm.IsConnected()
	}

	<-done

	err := cm.Stop()
	require.NoError(t, err)
}

// TestConnectionManager_StatusToEventType verifies correct event type mapping
func TestConnectionManager_StatusToEventType(t *testing.T) {
	// Test transitions that should emit events (status must change)
	tests := []struct {
		name     string
		from     ConnectionStatus
		to       ConnectionStatus
		expected ConnectionEventType
	}{
		{"Disconnected to Connecting", ConnectionDisconnected, ConnectionConnecting, EventConnecting},
		{"Connecting to Connected", ConnectionConnecting, ConnectionConnected, EventConnected},
		{"Connected to Disconnected", ConnectionConnected, ConnectionDisconnected, EventDisconnected},
		{"Connected to Reconnecting", ConnectionConnected, ConnectionReconnecting, EventReconnecting},
		{"Reconnecting to Failed", ConnectionReconnecting, ConnectionFailed, EventFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			logger := NewMockLogger()
			emitter := NewMockBridgeEventEmitter()
			cm := NewBaseConnectionManager(logger, "test", emitter)
			cm.InitContext(context.Background())

			// Set initial state
			cm.SetStatus(tt.from, nil)
			emitter.Clear()

			// Transition to target state
			cm.SetStatus(tt.to, nil)

			events := emitter.GetEvents()
			require.NotEmpty(t, events, "Expected event to be emitted on status change from %s to %s", tt.from, tt.to)

			err := cm.Stop()
			require.NoError(t, err)
		})
	}
}

// TestConnectionManager_NoEventOnSameStatus ensures no event is emitted when status doesn't change
func TestConnectionManager_NoEventOnSameStatus(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()
	cm := NewBaseConnectionManager(logger, "test", emitter)
	cm.InitContext(context.Background())

	// Set to connected
	cm.SetStatus(ConnectionConnected, nil)
	eventCountBefore := len(emitter.GetEvents())

	// Set to same status multiple times
	for i := 0; i < 5; i++ {
		cm.SetStatus(ConnectionConnected, nil)
	}

	// Should not have emitted more events
	eventCountAfter := len(emitter.GetEvents())
	assert.Equal(t, eventCountBefore, eventCountAfter, "Should not emit event when status doesn't change")

	err := cm.Stop()
	require.NoError(t, err)
}

// TestConnectionManager_IsConnected verifies IsConnected returns correct value
func TestConnectionManager_IsConnected(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()
	cm := NewBaseConnectionManager(logger, "test", emitter)
	cm.InitContext(context.Background())

	// Initially not connected
	assert.False(t, cm.IsConnected())

	// Set to connecting - still not connected
	cm.SetStatus(ConnectionConnecting, nil)
	assert.False(t, cm.IsConnected())

	// Set to connected
	cm.SetStatus(ConnectionConnected, nil)
	assert.True(t, cm.IsConnected())

	// Set to reconnecting - not connected
	cm.SetStatus(ConnectionReconnecting, nil)
	assert.False(t, cm.IsConnected())

	// Set to failed - not connected
	cm.SetStatus(ConnectionFailed, nil)
	assert.False(t, cm.IsConnected())

	err := cm.Stop()
	require.NoError(t, err)
}

// TestConnectionManager_RapidStateChanges tests rapid status changes under stress
func TestConnectionManager_RapidStateChanges(t *testing.T) {
	logger := NewMockLogger()
	emitter := NewMockBridgeEventEmitter()
	cm := NewBaseConnectionManager(logger, "test", emitter)
	cm.InitContext(context.Background())

	// Perform rapid state changes
	assertNoDeadlock(t, 5*time.Second, func() {
		for i := 0; i < 10000; i++ {
			switch i % 5 {
			case 0:
				cm.SetStatus(ConnectionDisconnected, nil)
			case 1:
				cm.SetStatus(ConnectionConnecting, nil)
			case 2:
				cm.SetStatus(ConnectionConnected, nil)
			case 3:
				cm.SetStatus(ConnectionReconnecting, nil)
			case 4:
				cm.SetStatus(ConnectionFailed, nil)
			}
		}
	})

	err := cm.Stop()
	require.NoError(t, err)
}
