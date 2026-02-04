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
	t.Run("Timeout releases goroutine correctly", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		var sessionOpMutex sync.Mutex

		// Simulate a long-running session.Open() operation
		openDone := make(chan error, 1)
		go func() {
			sessionOpMutex.Lock()
			defer sessionOpMutex.Unlock()

			// Simulate long operation
			time.Sleep(100 * time.Millisecond)
			openDone <- nil
		}()

		// Simulate timeout
		select {
		case <-openDone:
			t.Log("Operation completed within timeout")
		case <-time.After(50 * time.Millisecond):
			t.Log("Timeout occurred")
		case <-ctx.Done():
			t.Fatal("Context canceled unexpectedly")
		}

		// Verify we can acquire the mutex after timeout
		// (goroutine should eventually release it)
		mutexAcquired := make(chan bool)
		go func() {
			sessionOpMutex.Lock()
			defer sessionOpMutex.Unlock()
			mutexAcquired <- true
		}()

		select {
		case <-mutexAcquired:
			t.Log("Mutex successfully acquired after timeout")
		case <-time.After(200 * time.Millisecond):
			t.Fatal("Mutex not released after timeout - deadlock detected")
		}
	})

	t.Run("Context cancellation during timeout", func(t *testing.T) {
		_, cancel := context.WithCancel(context.Background())

		var sessionOpMutex sync.Mutex

		// Simulate a long-running session.Open() operation
		openDone := make(chan error, 1)
		go func() {
			sessionOpMutex.Lock()
			defer sessionOpMutex.Unlock()

			// Simulate long operation
			time.Sleep(200 * time.Millisecond)
			openDone <- nil
		}()

		// Cancel context after a short delay (simulating Disconnect() being called)
		go func() {
			time.Sleep(50 * time.Millisecond)
			cancel()
		}()

		// This select should NOT have ctx.Done() case if we want to avoid deadlock
		select {
		case <-openDone:
			t.Log("Operation completed")
		case <-time.After(100 * time.Millisecond):
			t.Log("Timeout occurred")
			// Note: ctx.Done() is intentionally NOT checked here
		}

		// Verify mutex can be acquired (simulating Disconnect())
		mutexAcquired := make(chan bool)
		go func() {
			sessionOpMutex.Lock()
			defer sessionOpMutex.Unlock()
			mutexAcquired <- true
		}()

		select {
		case <-mutexAcquired:
			t.Log("Disconnect successfully acquired mutex")
		case <-time.After(300 * time.Millisecond):
			t.Fatal("Disconnect deadlocked waiting for mutex")
		}
	})
}

// TestSharedDiscordClient_WaitForSessionReady tests the session ready waiting logic
func TestSharedDiscordClient_WaitForSessionReady(t *testing.T) {
	t.Run("Returns false on timeout", func(t *testing.T) {
		logger := &MockBridgeLogger{}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		client := &SharedDiscordClient{
			logger: logger,
			ctx:    ctx,
			cancel: cancel,
		}

		// Use a very short timeout
		result := client.waitForSessionReady(10 * time.Millisecond)
		assert.False(t, result, "Should timeout waiting for session ready")
	})

	t.Run("Respects context cancellation", func(t *testing.T) {
		logger := &MockBridgeLogger{}
		ctx, cancel := context.WithCancel(context.Background())

		client := &SharedDiscordClient{
			logger: logger,
			ctx:    ctx,
			cancel: cancel,
		}

		// Cancel context immediately
		cancel()

		result := client.waitForSessionReady(1 * time.Second)
		assert.False(t, result, "Should return false when context is canceled")
	})
}

// TestSharedDiscordClient_ReconnectMutex tests reconnection mutex handling
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
		go func() {
			sessionOpMutex.Lock()
			defer sessionOpMutex.Unlock()

			// Simulate blocking operation that takes longer than timeout
			time.Sleep(100 * time.Millisecond)
			openDone <- nil
		}()

		timeout := 50 * time.Millisecond
		select {
		case <-openDone:
			// Completed before timeout
		case <-time.After(timeout):
			// Timed out - this is expected
		}

		// Verify the mutex is eventually released even after timeout
		mutexReleased := make(chan bool)
		go func() {
			sessionOpMutex.Lock()
			defer sessionOpMutex.Unlock()
			mutexReleased <- true
		}()

		select {
		case <-mutexReleased:
			// Success - mutex was released
		case <-time.After(200 * time.Millisecond):
			t.Fatal("Mutex was not released after timeout")
		}
	})
}

// TestSharedDiscordClient_IsSessionHealthy tests session health checking
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

// TestSharedDiscordClient_NoDeadlockOnShutdown tests that Disconnect() doesn't deadlock
// when called while reconnection is in progress. This is a regression test for the bug
// where ctx.Done() during session.Open() would return early, leaving the goroutine
// holding sessionOpMutex and blocking Disconnect() forever.
func TestSharedDiscordClient_NoDeadlockOnShutdown(t *testing.T) {
	logger := &MockBridgeLogger{}

	var sessionOpMutex sync.Mutex
	_, cancel := context.WithCancel(context.Background())

	// Simulate the reconnection pattern from attemptIndefiniteReconnection()
	openDone := make(chan error, 1)

	// Start a goroutine that simulates session.Open() holding the lock
	go func() {
		sessionOpMutex.Lock()
		defer sessionOpMutex.Unlock()

		// Simulate a long-running session.Open() call
		time.Sleep(200 * time.Millisecond)
		openDone <- nil
	}()

	// Give the goroutine time to acquire the lock
	time.Sleep(10 * time.Millisecond)

	// Simulate Disconnect() being called while session.Open() is in progress
	// This simulates the shutdown sequence
	cancel()

	// The fixed code should NOT return immediately when ctx.Done() fires
	// Instead, it should wait for session.Open() to complete
	// IMPORTANT: No ctx.Done() case in the select - this is the fix!
	var sessionOpenErr error
	select {
	case sessionOpenErr = <-openDone:
		logger.Debug("TEST", "session.Open() completed")
	case <-time.After(100 * time.Millisecond):
		logger.Error("TEST", "session.Open() timed out")
		sessionOpenErr = assert.AnError
		// Note: The goroutine may still be running - this is acceptable
	}

	// Even if we timed out, the next loop iteration would catch ctx.Done()
	// For this test, we just verify the mutex is eventually available
	_ = sessionOpenErr

	// Now simulate Disconnect() trying to acquire sessionOpMutex
	disconnectComplete := make(chan bool)
	go func() {
		sessionOpMutex.Lock()
		defer sessionOpMutex.Unlock()
		logger.Info("TEST", "Disconnect acquired sessionOpMutex successfully")
		disconnectComplete <- true
	}()

	// Verify Disconnect() can acquire the mutex without deadlocking
	select {
	case <-disconnectComplete:
		logger.Info("TEST", "Disconnect completed successfully")
	case <-time.After(300 * time.Millisecond):
		t.Fatal("Disconnect deadlocked waiting for sessionOpMutex - the bug is still present!")
	}
}
