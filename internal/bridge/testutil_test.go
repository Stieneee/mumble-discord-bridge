package bridge

import (
	"runtime"
	"sync"
	"testing"
	"time"
)

// runConcurrently runs a function n times concurrently and waits for all to complete
func runConcurrently(n int, fn func(i int)) {
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func(idx int) {
			defer wg.Done()
			fn(idx)
		}(i)
	}
	wg.Wait()
}

// runConcurrentlyWithTimeout runs functions concurrently with a timeout
func runConcurrentlyWithTimeout(t *testing.T, timeout time.Duration, n int, fn func(i int)) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		runConcurrently(n, fn)
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(timeout):
		t.Fatalf("concurrent execution timed out after %v", timeout)
	}
}

// assertNoDeadlock runs a function with a timeout to detect deadlocks
func assertNoDeadlock(t *testing.T, timeout time.Duration, fn func()) {
	t.Helper()
	done := make(chan struct{})
	go func() {
		fn()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(timeout):
		// Print goroutine stacks to help debug
		buf := make([]byte, 1024*1024)
		n := runtime.Stack(buf, true)
		t.Fatalf("potential deadlock detected after %v\nGoroutine stacks:\n%s", timeout, buf[:n])
	}
}

// createTestBridgeState creates a BridgeState for testing with mocks
func createTestBridgeState(mockLogger *MockLogger) *BridgeState {
	if mockLogger == nil {
		mockLogger = NewMockLogger()
	}
	return &BridgeState{
		BridgeConfig: &BridgeConfig{
			GID:     "test-guild-id",
			CID:     "test-channel-id",
			Command: "bridge",
		},
		Logger:       mockLogger,
		BridgeDie:    make(chan bool, 1),
		AutoChanDie:  make(chan bool, 1),
		DiscordUsers: make(map[string]DiscordUser),
		MumbleUsers:  make(map[string]bool),
		WaitExit:     &sync.WaitGroup{},
	}
}
