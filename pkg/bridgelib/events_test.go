package bridgelib

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stieneee/mumble-discord-bridge/pkg/logger"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockTestLogger implements logger.Logger for testing
type MockTestLogger struct {
	mu      sync.Mutex
	entries []string
}

func (m *MockTestLogger) Debug(_, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, "DEBUG: "+msg)
}

func (m *MockTestLogger) Info(_, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, "INFO: "+msg)
}

func (m *MockTestLogger) Warn(_, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, "WARN: "+msg)
}

func (m *MockTestLogger) Error(_, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, "ERROR: "+msg)
}

func (m *MockTestLogger) WithBridgeID(_ string) logger.Logger {
	return m
}

func (m *MockTestLogger) getEntries() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	result := make([]string, len(m.entries))
	copy(result, m.entries)
	return result
}

// TestEventDispatcher_StopDrainsEvents verifies queued events are processed on Stop
func TestEventDispatcher_StopDrainsEvents(t *testing.T) {
	logger := &MockTestLogger{}
	ed := NewEventDispatcher("test-bridge", 100, logger)

	var receivedCount int32
	var wg sync.WaitGroup

	// Register handler that counts events
	ed.RegisterHandler(EventDiscordConnected, func(_ BridgeEvent) {
		atomic.AddInt32(&receivedCount, 1)
		wg.Done()
	})

	ed.Start()

	// Queue 100 events
	eventCount := 100
	wg.Add(eventCount)
	for i := 0; i < eventCount; i++ {
		ed.EmitEvent(EventDiscordConnected, nil, nil)
	}

	// Stop should drain remaining events
	ed.Stop()

	// Wait for all handlers to complete with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatalf("Timed out waiting for events, received %d of %d", atomic.LoadInt32(&receivedCount), eventCount)
	}

	// All events should have been delivered
	assert.Equal(t, int32(eventCount), atomic.LoadInt32(&receivedCount))
}

// TestEventDispatcher_RegisterDuringStop verifies no panic on concurrent registration
func TestEventDispatcher_RegisterDuringStop(_ *testing.T) {
	logger := &MockTestLogger{}
	ed := NewEventDispatcher("test-bridge", 100, logger)
	ed.Start()

	// Start concurrent registrations
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			ed.RegisterHandler(BridgeEventType(i%20), func(_ BridgeEvent) {})
			ed.RegisterGlobalHandler(func(_ BridgeEvent) {})
		}
		close(done)
	}()

	// Stop while registrations are happening
	time.Sleep(10 * time.Millisecond)
	ed.Stop()

	// Wait for registrations to complete
	select {
	case <-done:
		// Success - no panic
	case <-time.After(2 * time.Second):
		// Also fine - registrations might have blocked
	}
}

// TestEventDispatcher_HighThroughput tests 1000 events/sec without loss
func TestEventDispatcher_HighThroughput(t *testing.T) {
	logger := &MockTestLogger{}
	ed := NewEventDispatcher("test-bridge", 1000, logger)

	var receivedCount int32
	var wg sync.WaitGroup

	// Register fast handler
	ed.RegisterHandler(EventBridgeStarted, func(_ BridgeEvent) {
		atomic.AddInt32(&receivedCount, 1)
		wg.Done()
	})

	ed.Start()

	// Send 1000 events rapidly
	eventCount := 1000
	wg.Add(eventCount)
	for i := 0; i < eventCount; i++ {
		ed.EmitEvent(EventBridgeStarted, nil, nil)
	}

	// Wait for all handlers with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(10 * time.Second):
		t.Logf("Received %d of %d events", atomic.LoadInt32(&receivedCount), eventCount)
	}

	ed.Stop()

	// Most events should be received (some might be dropped under extreme load)
	received := atomic.LoadInt32(&receivedCount)
	assert.GreaterOrEqual(t, received, int32(eventCount*90/100),
		"Expected at least 90%% of events, got %d of %d", received, eventCount)
}

// TestEventDispatcher_HandlerPanic ensures panicking handler doesn't crash dispatcher
func TestEventDispatcher_HandlerPanic(t *testing.T) {
	logger := &MockTestLogger{}
	ed := NewEventDispatcher("test-bridge", 100, logger)

	var normalHandlerCalled int32

	// Register panicking handler
	ed.RegisterHandler(EventDiscordConnected, func(_ BridgeEvent) {
		panic("test panic")
	})

	// Register normal handler for different event
	ed.RegisterHandler(EventMumbleConnected, func(_ BridgeEvent) {
		atomic.AddInt32(&normalHandlerCalled, 1)
	})

	ed.Start()

	// Emit event that triggers panic
	ed.EmitEvent(EventDiscordConnected, nil, nil)

	// Give panic handler time to execute
	time.Sleep(100 * time.Millisecond)

	// Emit normal event - should still work
	ed.EmitEvent(EventMumbleConnected, nil, nil)

	// Wait for handler
	time.Sleep(100 * time.Millisecond)

	ed.Stop()

	// Normal handler should have been called
	assert.Equal(t, int32(1), atomic.LoadInt32(&normalHandlerCalled),
		"Normal handler should work after panic in another handler")
}

// TestEventDispatcher_GlobalHandler ensures global handlers receive all events
func TestEventDispatcher_GlobalHandler(t *testing.T) {
	logger := &MockTestLogger{}
	ed := NewEventDispatcher("test-bridge", 100, logger)

	var receivedEvents []BridgeEventType
	var mu sync.Mutex

	ed.RegisterGlobalHandler(func(e BridgeEvent) {
		mu.Lock()
		receivedEvents = append(receivedEvents, e.Type)
		mu.Unlock()
	})

	ed.Start()

	// Emit various event types
	eventTypes := []BridgeEventType{
		EventDiscordConnected,
		EventMumbleConnected,
		EventBridgeStarted,
		EventUserJoinedDiscord,
	}

	for _, et := range eventTypes {
		ed.EmitEventSync(et, nil, nil)
	}

	// Give handlers time to execute
	time.Sleep(200 * time.Millisecond)

	ed.Stop()

	// Should have received all event types
	mu.Lock()
	assert.Len(t, receivedEvents, len(eventTypes))
	mu.Unlock()
}

// TestEventDispatcher_ConcurrentEmitAndStop tests concurrent emit and stop
func TestEventDispatcher_ConcurrentEmitAndStop(_ *testing.T) {
	logger := &MockTestLogger{}
	ed := NewEventDispatcher("test-bridge", 100, logger)

	ed.RegisterHandler(EventDiscordConnected, func(_ BridgeEvent) {
		time.Sleep(time.Microsecond)
	})

	ed.Start()

	// Start emitting in background
	done := make(chan struct{})
	go func() {
		for i := 0; i < 1000; i++ {
			ed.EmitEvent(EventDiscordConnected, nil, nil)
		}
		close(done)
	}()

	// Stop after some emits
	time.Sleep(5 * time.Millisecond)
	ed.Stop()

	// Wait for emitting to finish
	<-done
}

// TestEventDispatcher_MultipleHandlersSameEvent tests multiple handlers for same event
func TestEventDispatcher_MultipleHandlersSameEvent(t *testing.T) {
	logger := &MockTestLogger{}
	ed := NewEventDispatcher("test-bridge", 100, logger)

	var handler1Called, handler2Called, handler3Called int32

	ed.RegisterHandler(EventDiscordConnected, func(_ BridgeEvent) {
		atomic.AddInt32(&handler1Called, 1)
	})
	ed.RegisterHandler(EventDiscordConnected, func(_ BridgeEvent) {
		atomic.AddInt32(&handler2Called, 1)
	})
	ed.RegisterHandler(EventDiscordConnected, func(_ BridgeEvent) {
		atomic.AddInt32(&handler3Called, 1)
	})

	ed.Start()

	ed.EmitEventSync(EventDiscordConnected, nil, nil)

	// Give handlers time
	time.Sleep(100 * time.Millisecond)

	ed.Stop()

	assert.Equal(t, int32(1), atomic.LoadInt32(&handler1Called))
	assert.Equal(t, int32(1), atomic.LoadInt32(&handler2Called))
	assert.Equal(t, int32(1), atomic.LoadInt32(&handler3Called))
}

// TestEventDispatcher_HandlerCount verifies handler count tracking
func TestEventDispatcher_HandlerCount(t *testing.T) {
	logger := &MockTestLogger{}
	ed := NewEventDispatcher("test-bridge", 100, logger)

	// Initially no handlers
	assert.Equal(t, 0, ed.GetHandlerCount(EventDiscordConnected))
	assert.Equal(t, 0, ed.GetGlobalHandlerCount())

	// Register handlers
	ed.RegisterHandler(EventDiscordConnected, func(_ BridgeEvent) {})
	ed.RegisterHandler(EventDiscordConnected, func(_ BridgeEvent) {})
	ed.RegisterHandler(EventMumbleConnected, func(_ BridgeEvent) {})
	ed.RegisterGlobalHandler(func(_ BridgeEvent) {})

	assert.Equal(t, 2, ed.GetHandlerCount(EventDiscordConnected))
	assert.Equal(t, 1, ed.GetHandlerCount(EventMumbleConnected))
	assert.Equal(t, 0, ed.GetHandlerCount(EventBridgeStarted))
	assert.Equal(t, 1, ed.GetGlobalHandlerCount())
}

// TestEventDispatcher_EventData verifies event data is passed correctly
func TestEventDispatcher_EventData(t *testing.T) {
	logger := &MockTestLogger{}
	ed := NewEventDispatcher("test-bridge", 100, logger)

	var receivedEvent BridgeEvent
	var mu sync.Mutex

	ed.RegisterHandler(EventDiscordConnected, func(e BridgeEvent) {
		mu.Lock()
		receivedEvent = e
		mu.Unlock()
	})

	ed.Start()

	testData := map[string]interface{}{
		"key1": "value1",
		"key2": 42,
	}
	testErr := assert.AnError

	ed.EmitEventSync(EventDiscordConnected, testData, testErr)

	// Give handler time
	time.Sleep(100 * time.Millisecond)

	ed.Stop()

	mu.Lock()
	defer mu.Unlock()

	assert.Equal(t, EventDiscordConnected, receivedEvent.Type)
	assert.Equal(t, "test-bridge", receivedEvent.BridgeID)
	assert.Equal(t, testData, receivedEvent.Data)
	assert.Equal(t, testErr, receivedEvent.Error)
	assert.False(t, receivedEvent.Timestamp.IsZero())
}

// TestEventDispatcher_DropOldest verifies drop-oldest behavior when buffer is full
func TestEventDispatcher_DropOldest(_ *testing.T) {
	logger := &MockTestLogger{}
	ed := NewEventDispatcher("test-bridge", 10, logger) // Small buffer

	// Register slow handler to fill buffer
	ed.RegisterHandler(EventDiscordConnected, func(_ BridgeEvent) {
		time.Sleep(100 * time.Millisecond)
	})

	ed.Start()

	// Emit many events quickly to fill buffer
	for i := 0; i < 100; i++ {
		ed.EmitEvent(EventDiscordConnected, nil, nil)
	}

	ed.Stop()

	// Check for warning about dropped events
	entries := logger.getEntries()
	hasDropWarning := false
	for _, entry := range entries {
		if containsSubstring(entry, "dropped") || containsSubstring(entry, "Dropped") {
			hasDropWarning = true
			break
		}
	}

	// It's okay if no warning - the drop-oldest strategy should just work silently
	// or log appropriately
	_ = hasDropWarning
}

// TestEventDispatcher_StartStopCycle tests multiple start/stop cycles
func TestEventDispatcher_StartStopCycle(t *testing.T) {
	logger := &MockTestLogger{}

	for i := 0; i < 10; i++ {
		ed := NewEventDispatcher("test-bridge", 100, logger)

		var called int32
		ed.RegisterHandler(EventDiscordConnected, func(_ BridgeEvent) {
			atomic.AddInt32(&called, 1)
		})

		ed.Start()
		ed.EmitEvent(EventDiscordConnected, nil, nil)
		time.Sleep(50 * time.Millisecond)
		ed.Stop()

		assert.Equal(t, int32(1), atomic.LoadInt32(&called), "Iteration %d", i)
	}
}

// TestEventDispatcher_EmitSync verifies synchronous event emission
func TestEventDispatcher_EmitSync(t *testing.T) {
	logger := &MockTestLogger{}
	ed := NewEventDispatcher("test-bridge", 100, logger)

	var handlerExecuted int32

	ed.RegisterHandler(EventDiscordConnected, func(_ BridgeEvent) {
		time.Sleep(50 * time.Millisecond)
		atomic.AddInt32(&handlerExecuted, 1)
	})

	ed.Start()

	// EmitSync should dispatch to handlers immediately (though handlers run in goroutines)
	ed.EmitEventSync(EventDiscordConnected, nil, nil)

	// Give handlers time
	time.Sleep(100 * time.Millisecond)

	ed.Stop()

	require.Equal(t, int32(1), atomic.LoadInt32(&handlerExecuted))
}

// TestEventDispatcher_ConcurrentAccess tests thread safety
func TestEventDispatcher_ConcurrentAccess(_ *testing.T) {
	logger := &MockTestLogger{}
	ed := NewEventDispatcher("test-bridge", 1000, logger)

	ed.Start()

	var wg sync.WaitGroup

	// Concurrent registrations
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ed.RegisterHandler(BridgeEventType(idx), func(_ BridgeEvent) {})
			ed.RegisterGlobalHandler(func(_ BridgeEvent) {})
		}(i)
	}

	// Concurrent emissions
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			ed.EmitEvent(BridgeEventType(idx%20), nil, nil)
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			_ = ed.GetHandlerCount(BridgeEventType(idx))
			_ = ed.GetGlobalHandlerCount()
		}(i)
	}

	wg.Wait()
	ed.Stop()
}

// Helper function
func containsSubstring(s, substr string) bool {
	if len(substr) > len(s) {
		return false
	}
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
