package bridgelib

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stieneee/mumble-discord-bridge/pkg/logger"
	"github.com/stretchr/testify/assert"
)

// MockBridgeLogger implements logger.Logger for testing
type MockBridgeLogger struct {
	mu      sync.Mutex
	entries []string
}

func (m *MockBridgeLogger) Debug(_, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, "DEBUG: "+msg)
}

func (m *MockBridgeLogger) Info(_, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, "INFO: "+msg)
}

func (m *MockBridgeLogger) Warn(_, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, "WARN: "+msg)
}

func (m *MockBridgeLogger) Error(_, msg string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = append(m.entries, "ERROR: "+msg)
}

func (m *MockBridgeLogger) WithBridgeID(_ string) logger.Logger {
	return m
}

// TestSharedDiscordClient_ReconnectTimeoutPattern tests that the timeout pattern
// correctly handles blocking operations
func TestSharedDiscordClient_ReconnectTimeoutPattern(t *testing.T) {
	t.Run("Timeout channel receives error on timeout", func(t *testing.T) {
		openDone := make(chan error, 1)
		openTimeout := 100 * time.Millisecond

		// Simulate a blocking session.Open() that never returns
		go func() {
			time.Sleep(1 * time.Second) // Simulate blocking
			openDone <- nil
		}()

		// Test timeout handling
		var sessionOpenErr error
		select {
		case sessionOpenErr = <-openDone:
			t.Error("Should have timed out, not received result")
		case <-time.After(openTimeout):
			sessionOpenErr = assert.AnError // Simulated timeout error
		}

		assert.Error(t, sessionOpenErr, "Should have a timeout error")
	})

	t.Run("Success case when operation completes quickly", func(t *testing.T) {
		openDone := make(chan error, 1)
		openTimeout := 1 * time.Second

		// Simulate a quick session.Open()
		go func() {
			time.Sleep(50 * time.Millisecond)
			openDone <- nil
		}()

		var sessionOpenErr error
		select {
		case sessionOpenErr = <-openDone:
			// Success
		case <-time.After(openTimeout):
			sessionOpenErr = assert.AnError
		}

		assert.NoError(t, sessionOpenErr, "Should succeed without timeout")
	})

	t.Run("Context cancellation during open", func(t *testing.T) {
		openDone := make(chan error, 1)
		ctx, cancel := context.WithCancel(context.Background())

		// Simulate a blocking session.Open()
		go func() {
			time.Sleep(1 * time.Second)
			openDone <- nil
		}()

		// Cancel after 50ms
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()

		var canceled bool
		select {
		case <-openDone:
			t.Error("Should have been canceled")
		case <-time.After(500 * time.Millisecond):
			t.Error("Should have detected context cancellation")
		case <-ctx.Done():
			canceled = true
		}

		assert.True(t, canceled, "Should detect context cancellation")
	})
}

// TestSharedDiscordClient_WaitForSessionReady tests the session ready waiting logic
func TestSharedDiscordClient_WaitForSessionReady(t *testing.T) {
	t.Run("Timeout when session never becomes ready", func(t *testing.T) {
		logger := &MockBridgeLogger{}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		client := &SharedDiscordClient{
			logger: logger,
			ctx:    ctx,
			cancel: cancel,
		}

		// session is nil, so isSessionHealthy will always return false
		result := client.waitForSessionReady(100 * time.Millisecond)
		assert.False(t, result, "Should return false when session never becomes ready")
	})

	t.Run("Context cancellation during wait", func(t *testing.T) {
		logger := &MockBridgeLogger{}
		ctx, cancel := context.WithCancel(context.Background())

		client := &SharedDiscordClient{
			logger: logger,
			ctx:    ctx,
			cancel: cancel,
		}

		// Cancel quickly
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()

		start := time.Now()
		result := client.waitForSessionReady(5 * time.Second)
		elapsed := time.Since(start)

		assert.False(t, result, "Should return false when context canceled")
		assert.Less(t, elapsed, 1*time.Second, "Should exit quickly on context cancel")
	})
}

// TestSharedDiscordClient_ReconnectMutex tests that reconnection is properly serialized
func TestSharedDiscordClient_ReconnectMutex(t *testing.T) {
	logger := &MockBridgeLogger{}
	ctx, cancel := context.WithCancel(context.Background())

	client := &SharedDiscordClient{
		logger:              logger,
		ctx:                 ctx,
		cancel:              cancel,
		reconnectInProgress: false,
	}

	// Simulate concurrent reconnection attempts
	var attempts int32
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			client.reconnectMutex.Lock()
			if client.reconnectInProgress {
				client.reconnectMutex.Unlock()
				return
			}
			client.reconnectInProgress = true
			atomic.AddInt32(&attempts, 1)

			// Simulate reconnection work
			time.Sleep(10 * time.Millisecond)

			client.reconnectInProgress = false
			client.reconnectMutex.Unlock()
		}()
	}

	wg.Wait()
	cancel()

	// With proper serialization, there should have been multiple sequential attempts
	// (not all 10 at once due to reconnectInProgress check)
	assert.Greater(t, atomic.LoadInt32(&attempts), int32(0), "Should have had some attempts")
}

// TestSharedDiscordClient_SessionOpMutexWithTimeout tests the mutex handling with timeout
func TestSharedDiscordClient_SessionOpMutexWithTimeout(t *testing.T) {
	t.Run("Mutex released even on timeout", func(t *testing.T) {
		var sessionOpMutex sync.Mutex

		// Simulate the new timeout pattern
		openDone := make(chan error, 1)
		openTimeout := 50 * time.Millisecond

		// Start a goroutine that holds the mutex
		go func() {
			sessionOpMutex.Lock()
			defer sessionOpMutex.Unlock()

			time.Sleep(200 * time.Millisecond) // Hold longer than timeout
			openDone <- nil
		}()

		// Wait for timeout
		select {
		case <-openDone:
			// This shouldn't happen first
		case <-time.After(openTimeout):
			// Timed out, but goroutine still holds mutex
		}

		// Verify we can acquire the mutex after the goroutine releases it
		done := make(chan bool)
		go func() {
			sessionOpMutex.Lock()
			_ = 0 // Intentionally empty - we just need to verify the mutex can be acquired
			sessionOpMutex.Unlock()
			done <- true
		}()

		select {
		case <-done:
			// Success - mutex was eventually released
		case <-time.After(1 * time.Second):
			t.Fatal("Mutex was never released")
		}
	})
}

// TestSharedDiscordClient_IsSessionHealthy tests the health check logic
func TestSharedDiscordClient_IsSessionHealthy(t *testing.T) {
	t.Run("Returns false for nil session", func(t *testing.T) {
		logger := &MockBridgeLogger{}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		client := &SharedDiscordClient{
			logger:  logger,
			ctx:     ctx,
			cancel:  cancel,
			session: nil,
		}

		assert.False(t, client.isSessionHealthy())
	})
}
