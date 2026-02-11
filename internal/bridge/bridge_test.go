package bridge

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

// TestBridge_DiscordUsersMapRace tests concurrent map access under mutex
func TestBridge_DiscordUsersMapRace(_ *testing.T) {
	bridge := createTestBridgeState(nil)

	var wg sync.WaitGroup

	// Concurrent writes
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			bridge.DiscordUsersMutex.Lock()
			bridge.DiscordUsers["user-"+string(rune('0'+idx%10))] = DiscordUser{
				username: "TestUser",
				seen:     true,
			}
			bridge.DiscordUsersMutex.Unlock()
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bridge.DiscordUsersMutex.Lock()
			for k := range bridge.DiscordUsers {
				_ = bridge.DiscordUsers[k].username
			}
			bridge.DiscordUsersMutex.Unlock()
		}()
	}

	// Concurrent deletes
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			bridge.DiscordUsersMutex.Lock()
			delete(bridge.DiscordUsers, "user-"+string(rune('0'+idx%10)))
			bridge.DiscordUsersMutex.Unlock()
		}(i)
	}

	wg.Wait()
}

// TestBridge_MumbleUsersMapRace tests concurrent Mumble user map access
func TestBridge_MumbleUsersMapRace(_ *testing.T) {
	bridge := createTestBridgeState(nil)

	var wg sync.WaitGroup

	// Concurrent writes
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			bridge.MumbleUsersMutex.Lock()
			bridge.MumbleUsers["user-"+string(rune('0'+idx%10))] = true
			bridge.MumbleUsersMutex.Unlock()
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bridge.MumbleUsersMutex.Lock()
			for k := range bridge.MumbleUsers {
				_ = bridge.MumbleUsers[k]
			}
			bridge.MumbleUsersMutex.Unlock()
		}()
	}

	wg.Wait()
}

// TestBridge_BridgeMutexLockOrder tests proper lock ordering
func TestBridge_BridgeMutexLockOrder(t *testing.T) {
	bridge := createTestBridgeState(nil)

	// Test proper lock order: BridgeMutex -> MumbleUsersMutex -> DiscordUsersMutex
	assertNoDeadlock(t, 5*time.Second, func() {
		var wg sync.WaitGroup

		for i := 0; i < 50; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// Correct lock order
				bridge.BridgeMutex.Lock()
				bridge.MumbleUsersMutex.Lock()
				bridge.DiscordUsersMutex.Lock()

				// Do some work
				bridge.MumbleUsers["test"] = true
				bridge.DiscordUsers["test"] = DiscordUser{username: "test"}

				bridge.DiscordUsersMutex.Unlock()
				bridge.MumbleUsersMutex.Unlock()
				bridge.BridgeMutex.Unlock()
			}()
		}

		wg.Wait()
	})
}

// TestBridge_IsConnected tests thread-safe IsConnected
func TestBridge_IsConnected(t *testing.T) {
	bridge := createTestBridgeState(nil)

	// Initial state
	assert.False(t, bridge.IsConnected())

	// Set connected
	bridge.BridgeMutex.Lock()
	bridge.Connected = true
	bridge.BridgeMutex.Unlock()

	assert.True(t, bridge.IsConnected())

	// Concurrent access
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = bridge.IsConnected()
		}()
	}
	wg.Wait()
}

// TestBridge_GetConnectionStates tests thread-safe state retrieval
func TestBridge_GetConnectionStates(t *testing.T) {
	bridge := createTestBridgeState(nil)

	// Set some states
	bridge.BridgeMutex.Lock()
	bridge.DiscordConnected = true
	bridge.MumbleConnected = false
	bridge.Connected = true
	bridge.BridgeMutex.Unlock()

	discord, mumble, overall := bridge.GetConnectionStates()
	assert.True(t, discord)
	assert.False(t, mumble)
	assert.True(t, overall)
}

// TestBridge_UpdateOverallConnectionState tests state update logic
func TestBridge_UpdateOverallConnectionState(t *testing.T) {
	testCases := []struct {
		name         string
		mode         BridgeMode
		discord      bool
		mumble       bool
		expectedConn bool
	}{
		{"Constant - both connected", BridgeModeConstant, true, true, true},
		{"Constant - none connected", BridgeModeConstant, false, false, false},
		{"Auto - both connected", BridgeModeAuto, true, true, true},
		{"Auto - one connected", BridgeModeAuto, true, false, false},
		{"Auto - none connected", BridgeModeAuto, false, false, false},
		{"Manual - both connected", BridgeModeManual, true, true, true},
		{"Manual - one connected", BridgeModeManual, true, false, false},
		{"Manual - none connected", BridgeModeManual, false, false, false},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			bridge := createTestBridgeState(nil)
			bridge.BridgeMutex.Lock()
			bridge.Mode = tc.mode
			bridge.DiscordConnected = tc.discord
			bridge.MumbleConnected = tc.mumble
			bridge.updateOverallConnectionState()
			result := bridge.Connected
			bridge.BridgeMutex.Unlock()

			assert.Equal(t, tc.expectedConn, result)
		})
	}
}

// TestBridge_EmitConnectionEvent tests event emission
func TestBridge_EmitConnectionEvent(t *testing.T) {
	logger := NewMockLogger()
	bridge := createTestBridgeState(logger)

	// Test Discord connection event
	bridge.EmitConnectionEvent("discord", 1, true, nil)

	bridge.BridgeMutex.Lock()
	assert.True(t, bridge.DiscordConnected)
	bridge.BridgeMutex.Unlock()

	// Test Mumble connection event
	bridge.EmitConnectionEvent("mumble", 1, true, nil)

	bridge.BridgeMutex.Lock()
	assert.True(t, bridge.MumbleConnected)
	bridge.BridgeMutex.Unlock()
}

// TestBridge_MetricsChangeCallback tests callback invocation
func TestBridge_MetricsChangeCallback(t *testing.T) {
	bridge := createTestBridgeState(nil)

	var callCount int32
	bridge.MetricsChangeCallback = func() {
		atomic.AddInt32(&callCount, 1)
	}

	// Trigger notification
	bridge.notifyMetricsChange()

	// Wait for goroutine
	time.Sleep(50 * time.Millisecond)

	assert.Greater(t, atomic.LoadInt32(&callCount), int32(0))
}

// TestBridge_ConcurrentConnectionStateUpdates tests concurrent state updates
func TestBridge_ConcurrentConnectionStateUpdates(_ *testing.T) {
	bridge := createTestBridgeState(nil)

	var wg sync.WaitGroup

	// Concurrent EmitConnectionEvent calls
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			if idx%2 == 0 {
				bridge.EmitConnectionEvent("discord", idx%5, idx%2 == 0, nil)
			} else {
				bridge.EmitConnectionEvent("mumble", idx%5, idx%2 == 1, nil)
			}
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, _ = bridge.GetConnectionStates()
			_ = bridge.IsConnected()
		}()
	}

	wg.Wait()
}

// TestBridge_StopBridgeSignal tests BridgeDie channel behavior
func TestBridge_StopBridgeSignal(t *testing.T) {
	bridge := createTestBridgeState(nil)

	// Sending to BridgeDie should not block (buffered)
	select {
	case bridge.BridgeDie <- true:
		// Success
	default:
		t.Fatal("BridgeDie channel should be buffered")
	}

	// Second send should use select default
	select {
	case bridge.BridgeDie <- true:
		// Might succeed if first was consumed
	default:
		// Also fine - channel might be full
	}
}

// TestBridge_ModeString tests BridgeMode String method
func TestBridge_ModeString(t *testing.T) {
	assert.Equal(t, "auto", BridgeModeAuto.String())
	assert.Equal(t, "manual", BridgeModeManual.String())
	assert.Equal(t, "constant", BridgeModeConstant.String())
}

// TestBridge_ConcurrentModeAccess tests concurrent mode access
func TestBridge_ConcurrentModeAccess(_ *testing.T) {
	bridge := createTestBridgeState(nil)

	var wg sync.WaitGroup

	modes := []BridgeMode{BridgeModeAuto, BridgeModeManual, BridgeModeConstant}

	// Concurrent mode changes
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			bridge.BridgeMutex.Lock()
			bridge.Mode = modes[idx%len(modes)]
			bridge.BridgeMutex.Unlock()
		}(i)
	}

	// Concurrent reads
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			bridge.BridgeMutex.Lock()
			_ = bridge.Mode
			bridge.BridgeMutex.Unlock()
		}()
	}

	wg.Wait()
}

// TestBridge_UserCountTracking tests user count accuracy
func TestBridge_UserCountTracking(t *testing.T) {
	bridge := createTestBridgeState(nil)

	// Add users
	bridge.MumbleUsersMutex.Lock()
	bridge.MumbleUsers["user1"] = true
	bridge.MumbleUsers["user2"] = true
	bridge.MumbleUsers["user3"] = true
	bridge.MumbleUserCount = 3
	bridge.MumbleUsersMutex.Unlock()

	bridge.DiscordUsersMutex.Lock()
	bridge.DiscordUsers["discord1"] = DiscordUser{username: "user1"}
	bridge.DiscordUsers["discord2"] = DiscordUser{username: "user2"}
	bridge.DiscordUsersMutex.Unlock()

	// Verify counts
	bridge.MumbleUsersMutex.Lock()
	assert.Equal(t, 3, len(bridge.MumbleUsers))
	assert.Equal(t, 3, bridge.MumbleUserCount)
	bridge.MumbleUsersMutex.Unlock()

	bridge.DiscordUsersMutex.Lock()
	assert.Equal(t, 2, len(bridge.DiscordUsers))
	bridge.DiscordUsersMutex.Unlock()
}

// TestBridge_WaitExitUsage tests WaitGroup usage
func TestBridge_WaitExitUsage(t *testing.T) {
	bridge := createTestBridgeState(nil)

	// Add to wait group
	bridge.WaitExit.Add(3)

	var completed int32

	// Complete work
	for i := 0; i < 3; i++ {
		go func() {
			defer bridge.WaitExit.Done()
			atomic.AddInt32(&completed, 1)
		}()
	}

	// Wait should complete
	done := make(chan struct{})
	go func() {
		bridge.WaitExit.Wait()
		close(done)
	}()

	select {
	case <-done:
		assert.Equal(t, int32(3), atomic.LoadInt32(&completed))
	case <-time.After(2 * time.Second):
		t.Fatal("WaitExit.Wait() timed out")
	}
}

// TestBridge_ConnectionManagerNilSafe tests nil manager handling
func TestBridge_ConnectionManagerNilSafe(t *testing.T) {
	bridge := createTestBridgeState(nil)

	// Managers should be nil initially
	assert.Nil(t, bridge.DiscordVoiceConnectionManager)
	assert.Nil(t, bridge.MumbleConnectionManager)

	// EmitConnectionEvent should still work
	assert.NotPanics(t, func() {
		bridge.EmitConnectionEvent("discord", 0, false, nil)
		bridge.EmitConnectionEvent("mumble", 0, false, nil)
	})
}

// TestBridge_ContextCancellation tests context-based shutdown
func TestBridge_ContextCancellation(t *testing.T) {
	bridge := createTestBridgeState(nil)

	ctx, cancel := context.WithCancel(context.Background())
	bridge.connectionCtx = ctx
	bridge.connectionCancel = cancel

	// Cancel should work
	cancel()

	// Context should be done
	select {
	case <-bridge.connectionCtx.Done():
		// Success
	default:
		t.Fatal("Context should be canceled")
	}
}

// TestBridge_StartTimeSetting tests StartTime field
func TestBridge_StartTimeSetting(t *testing.T) {
	bridge := createTestBridgeState(nil)

	// Initially zero
	assert.True(t, bridge.StartTime.IsZero())

	// Set start time
	now := time.Now()
	bridge.BridgeMutex.Lock()
	bridge.StartTime = now
	bridge.BridgeMutex.Unlock()

	bridge.BridgeMutex.Lock()
	assert.Equal(t, now, bridge.StartTime)
	bridge.BridgeMutex.Unlock()
}

// TestBridge_EmitUserEvent tests user event emission
func TestBridge_EmitUserEvent(t *testing.T) {
	bridge := createTestBridgeState(nil)

	// Without BridgeInstance, should not panic
	assert.NotPanics(t, func() {
		bridge.EmitUserEvent("discord", 0, "testuser", nil)
		bridge.EmitUserEvent("mumble", 1, "testuser", nil)
	})
}

// TestBridge_DiscordUserStruct tests DiscordUser struct fields
func TestBridge_DiscordUserStruct(t *testing.T) {
	user := DiscordUser{
		username: "TestUser",
		seen:     true,
		dm:       nil,
	}

	assert.Equal(t, "TestUser", user.username)
	assert.True(t, user.seen)
	assert.Nil(t, user.dm)
}
