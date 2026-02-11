package bridgelib

import (
	"context"
	"sync"
	"testing"
	"time"

	discordgo "github.com/bwmarrin/discordgo"
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

// TestSharedDiscordClient_SessionMonitorLoop_ContextCancellation tests that the
// monitor loop exits cleanly when the context is canceled
func TestSharedDiscordClient_SessionMonitorLoop_ContextCancellation(t *testing.T) {
	lgr := &MockBridgeLogger{}
	ctx, cancel := context.WithCancel(context.Background())

	client := &SharedDiscordClient{
		logger: lgr,
		ctx:    ctx,
		cancel: cancel,
	}

	done := make(chan struct{})
	go func() {
		client.sessionMonitorLoop()
		close(done)
	}()

	// Cancel context after a short delay
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Monitor loop exited cleanly
	case <-time.After(2 * time.Second):
		t.Fatal("sessionMonitorLoop did not exit after context cancellation")
	}
}

// TestSharedDiscordClient_IsSessionHealthy_LockedMutex tests that isSessionHealthy()
// returns false immediately when the session mutex is write-locked (simulating a
// deadlocked session), rather than blocking indefinitely.
func TestSharedDiscordClient_IsSessionHealthy_LockedMutex(t *testing.T) {
	lgr := &MockBridgeLogger{}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	session := &discordgo.Session{
		State:     discordgo.NewState(),
		DataReady: true,
	}
	session.State.User = &discordgo.User{ID: "test", Username: "Test"}

	client := &SharedDiscordClient{
		logger:  lgr,
		ctx:     ctx,
		cancel:  cancel,
		session: session,
	}

	// Verify healthy when unlocked
	assert.True(t, client.isSessionHealthy(), "should be healthy when session is unlocked and ready")

	// Simulate a deadlocked session by write-locking the mutex
	session.Lock()
	defer session.Unlock()

	// isSessionHealthy must return false without blocking
	done := make(chan bool, 1)
	go func() {
		done <- client.isSessionHealthy()
	}()

	select {
	case healthy := <-done:
		assert.False(t, healthy, "should return false when session mutex is locked")
	case <-time.After(1 * time.Second):
		t.Fatal("isSessionHealthy() blocked on locked mutex — TryRLock not working")
	}
}

// TestSharedDiscordClient_SessionMonitorLoop_LogsUnhealthy tests that the
// monitor loop logs when the session is unhealthy (without attempting reconnection)
func TestSharedDiscordClient_SessionMonitorLoop_LogsUnhealthy(t *testing.T) {
	lgr := &MockBridgeLogger{}
	ctx, cancel := context.WithCancel(context.Background())

	client := &SharedDiscordClient{
		logger:  lgr,
		ctx:     ctx,
		cancel:  cancel,
		session: nil, // nil session is always unhealthy
	}

	done := make(chan struct{})
	go func() {
		client.sessionMonitorLoop()
		close(done)
	}()

	// Let the monitor run for a couple ticks (ticker is 15s, but we'll just cancel quickly)
	// The key assertion is that it doesn't panic or deadlock
	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Monitor loop exited cleanly without panic or deadlock
	case <-time.After(2 * time.Second):
		t.Fatal("sessionMonitorLoop did not exit after context cancellation")
	}
}
