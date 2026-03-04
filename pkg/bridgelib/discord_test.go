package bridgelib

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stieneee/mumble-discord-bridge/internal/discord"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockBridgeLibClient implements discord.Client for SharedDiscordClient tests.
type mockBridgeLibClient struct {
	mu    sync.Mutex
	ready bool
}

func (m *mockBridgeLibClient) Connect(_ context.Context) error { return nil }

func (m *mockBridgeLibClient) Disconnect(_ context.Context) error { return nil }

func (m *mockBridgeLibClient) SendMessage(_, _ string) error { return nil }

func (m *mockBridgeLibClient) GetUser(_ string) (*discord.User, error) {
	return &discord.User{}, nil
}

func (m *mockBridgeLibClient) CreateDM(_ string) (string, error) { return "", nil }

func (m *mockBridgeLibClient) GetGuild(_ string) (*discord.Guild, error) {
	return &discord.Guild{}, nil
}

func (m *mockBridgeLibClient) GetBotUserID() string { return "bot-user-id" }

func (m *mockBridgeLibClient) IsReady() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.ready
}

func (m *mockBridgeLibClient) CreateVoiceConnection(_ string) discord.VoiceConnection { return nil }

func (m *mockBridgeLibClient) SetEventHandler(_ discord.EventHandler) {}

// TestSharedDiscordClient_IsSessionHealthy verifies that IsSessionHealthy
// correctly reflects the readiness state of the underlying discord.Client.
func TestSharedDiscordClient_IsSessionHealthy(t *testing.T) {
	t.Run("returns false when client reports not ready", func(t *testing.T) {
		mock := &mockBridgeLibClient{ready: false}
		lgr := &MockTestLogger{}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sdc := &SharedDiscordClient{
			client: mock,
			logger: lgr,
			ctx:    ctx,
			cancel: cancel,
		}

		assert.False(t, sdc.IsSessionHealthy(), "expected unhealthy when client is not ready")
	})

	t.Run("returns true when client reports ready", func(t *testing.T) {
		mock := &mockBridgeLibClient{ready: true}
		lgr := &MockTestLogger{}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()

		sdc := &SharedDiscordClient{
			client: mock,
			logger: lgr,
			ctx:    ctx,
			cancel: cancel,
		}

		assert.True(t, sdc.IsSessionHealthy(), "expected healthy when client is ready")
	})
}

// TestSharedDiscordClient_SessionMonitorLoop_ContextCancellation verifies that
// the session monitor loop exits cleanly when its context is canceled.
func TestSharedDiscordClient_SessionMonitorLoop_ContextCancellation(t *testing.T) {
	mock := &mockBridgeLibClient{ready: true}
	lgr := &MockTestLogger{}
	ctx, cancel := context.WithCancel(context.Background())

	sdc := &SharedDiscordClient{
		client: mock,
		logger: lgr,
		ctx:    ctx,
		cancel: cancel,
	}

	// Run the monitor loop in a goroutine and signal when it returns.
	done := make(chan struct{})
	go func() {
		sdc.sessionMonitorLoop()
		close(done)
	}()

	// Cancel the context to trigger loop exit.
	cancel()

	select {
	case <-done:
		// Loop exited cleanly -- success.
	case <-time.After(5 * time.Second):
		t.Fatal("sessionMonitorLoop did not exit within 5 seconds after context cancellation")
	}

	// Verify that the loop logged its exit message.
	entries := lgr.getEntries()
	found := false
	for _, e := range entries {
		if containsSubstring(e, "Session monitoring loop exiting") {
			found = true
			break
		}
	}
	require.True(t, found, "expected 'Session monitoring loop exiting' in log entries, got: %v", entries)
}

// TestSharedDiscordClient_SessionMonitorLoop_LogsUnhealthy verifies that the
// monitor loop runs without panicking when the client reports unhealthy, and
// exits cleanly on context cancellation.
func TestSharedDiscordClient_SessionMonitorLoop_LogsUnhealthy(t *testing.T) {
	mock := &mockBridgeLibClient{ready: false}
	lgr := &MockTestLogger{}
	ctx, cancel := context.WithCancel(context.Background())

	sdc := &SharedDiscordClient{
		client: mock,
		logger: lgr,
		ctx:    ctx,
		cancel: cancel,
	}

	done := make(chan struct{})
	go func() {
		sdc.sessionMonitorLoop()
		close(done)
	}()

	// Let the monitor tick at least once (ticker is 15s, so we can't wait
	// that long in a unit test). Instead, cancel promptly and just verify
	// the loop starts and exits without panicking.
	// We give it a short window so the goroutine has time to enter the select.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case <-done:
		// Loop exited -- no panic.
	case <-time.After(5 * time.Second):
		t.Fatal("sessionMonitorLoop did not exit within 5 seconds after context cancellation")
	}

	// The loop should have logged that it started and that it is exiting.
	entries := lgr.getEntries()
	foundStarted := false
	foundExiting := false
	for _, e := range entries {
		if containsSubstring(e, "Session monitoring loop started") {
			foundStarted = true
		}
		if containsSubstring(e, "Session monitoring loop exiting") {
			foundExiting = true
		}
	}
	assert.True(t, foundStarted, "expected 'Session monitoring loop started' log entry, got: %v", entries)
	assert.True(t, foundExiting, "expected 'Session monitoring loop exiting' log entry, got: %v", entries)
}
