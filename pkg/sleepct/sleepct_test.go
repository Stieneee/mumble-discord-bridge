package sleepct

import (
	"context"
	"testing"
	"time"
)

func TestStart(t *testing.T) {
	s := &SleepCT{}
	duration := 100 * time.Millisecond

	s.Start(duration)

	if s.d != duration {
		t.Errorf("expected duration %v, got %v", duration, s.d)
	}
	if s.t.IsZero() {
		t.Error("expected target time to be set after Start")
	}
	if s.resume == nil {
		t.Error("expected resume channel to be initialized")
	}
	if cap(s.resume) != 2 {
		t.Errorf("expected resume channel capacity 2, got %d", cap(s.resume))
	}
}

func TestStartPanicsOnDoubleStart(t *testing.T) {
	s := &SleepCT{}
	s.Start(100 * time.Millisecond)

	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on double Start, but didn't panic")
		}
	}()

	s.Start(50 * time.Millisecond)
}

func TestNotify(t *testing.T) {
	s := &SleepCT{}
	s.Start(100 * time.Millisecond)

	// Notify should not block
	s.Notify()
	s.Notify()
	s.Notify() // Third notify should not block (channel capacity is 2)

	// Channel should have 2 items (capacity limit)
	if len(s.resume) != 2 {
		t.Errorf("expected resume channel to have 2 items, got %d", len(s.resume))
	}
}

func TestNotifyNonBlocking(t *testing.T) {
	s := &SleepCT{}
	s.Start(100 * time.Millisecond)

	// Fill the channel
	s.Notify()
	s.Notify()

	// This should not block (default case in select)
	done := make(chan bool)
	go func() {
		s.Notify() // Should return immediately
		done <- true
	}()

	select {
	case <-done:
		// Good - Notify returned
	case <-time.After(100 * time.Millisecond):
		t.Error("Notify blocked unexpectedly")
	}
}

func TestSleepNextTargetBasic(t *testing.T) {
	s := &SleepCT{}
	duration := 10 * time.Millisecond
	s.Start(duration)

	start := time.Now()
	drift := s.SleepNextTarget(context.Background(), false)
	elapsed := time.Since(start)

	// Should have slept approximately the duration
	// Allow some tolerance for scheduler variance
	if elapsed < duration-time.Millisecond {
		t.Errorf("slept too short: %v (expected ~%v)", elapsed, duration)
	}
	if elapsed > duration+50*time.Millisecond {
		t.Errorf("slept too long: %v (expected ~%v)", elapsed, duration)
	}

	// Drift should be small (< 10ms tolerance)
	if drift > 10000 { // microseconds
		t.Errorf("drift too high: %d microseconds", drift)
	}

	// Target time should have been updated
	if s.t.IsZero() {
		t.Error("target time should be set after SleepNextTarget")
	}
}

func TestSleepNextTargetWithDriftTracking(t *testing.T) {
	s := &SleepCT{}
	duration := 5 * time.Millisecond
	s.Start(duration)

	// Run several cycles and check drift stays reasonable
	for i := 0; i < 5; i++ {
		drift := s.SleepNextTarget(context.Background(), false)
		// Drift should be reasonable (allowing for scheduler)
		if drift < 0 {
			// Negative drift means we woke early, which shouldn't happen with time.Sleep
			t.Logf("cycle %d: drift %d (woke before target)", i, drift)
		}
	}
}

func TestSleepNextTargetWithPauseAndNotify(t *testing.T) {
	s := &SleepCT{}
	duration := 5 * time.Millisecond
	s.Start(duration)

	ctx := context.Background()

	// First sleep (no pause)
	_ = s.SleepNextTarget(ctx, false)

	// Start a goroutine that will notify after a short delay
	go func() {
		time.Sleep(20 * time.Millisecond)
		s.Notify()
	}()

	start := time.Now()
	_ = s.SleepNextTarget(ctx, true) // This should pause until notified
	elapsed := time.Since(start)

	// Should have paused for at least 20ms (the notify delay)
	if elapsed < 15*time.Millisecond {
		t.Errorf("pause was too short: %v (expected >= ~20ms)", elapsed)
	}
}

func TestSleepNextTargetWithPauseAndContextCancel(t *testing.T) {
	s := &SleepCT{}
	duration := 5 * time.Millisecond
	s.Start(duration)

	ctx, cancel := context.WithCancel(context.Background())

	// First sleep (no pause)
	_ = s.SleepNextTarget(ctx, false)

	// Cancel context after a short delay
	go func() {
		time.Sleep(20 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_ = s.SleepNextTarget(ctx, true) // This should pause until context cancelled
	elapsed := time.Since(start)

	// Should have paused for about 20ms
	if elapsed < 15*time.Millisecond {
		t.Errorf("pause was too short: %v (expected >= ~20ms)", elapsed)
	}

	// Target should be reset after pause
	if s.t.IsZero() {
		t.Error("target time should be set after pause")
	}
}

func TestSleepNextTargetDrainsResumeChannel(t *testing.T) {
	s := &SleepCT{}
	duration := 5 * time.Millisecond
	s.Start(duration)

	// Put one item in resume channel (drain only removes one)
	s.Notify()

	// SleepNextTarget should drain the channel
	_ = s.SleepNextTarget(context.Background(), false)

	// Channel should be drained
	if len(s.resume) != 0 {
		t.Errorf("expected resume channel to be drained, got %d items", len(s.resume))
	}
}

func TestSleepNextTargetNoPauseWhenResumeHasItems(t *testing.T) {
	s := &SleepCT{}
	duration := 5 * time.Millisecond
	s.Start(duration)

	// First sleep (no pause)
	_ = s.SleepNextTarget(context.Background(), false)

	// Put an item in resume channel
	s.Notify()

	start := time.Now()
	// This should NOT pause because resume channel has items
	_ = s.SleepNextTarget(context.Background(), true)
	elapsed := time.Since(start)

	// Should have only slept for the duration, not paused
	if elapsed > 50*time.Millisecond {
		t.Errorf("slept too long (should not have paused): %v", elapsed)
	}
}

func TestMultipleCycles(t *testing.T) {
	s := &SleepCT{}
	duration := 10 * time.Millisecond
	s.Start(duration)

	// Run multiple cycles and verify timing stability
	var drifts []int64
	for i := 0; i < 10; i++ {
		drift := s.SleepNextTarget(context.Background(), false)
		drifts = append(drifts, drift)
	}

	// Check that drift stays bounded
	maxDrift := int64(0)
	for _, d := range drifts {
		if d > maxDrift {
			maxDrift = d
		}
	}

	// Max drift should be under 50ms (50000 microseconds) even on a busy system
	if maxDrift > 50000 {
		t.Errorf("max drift too high: %d microseconds", maxDrift)
	}
}
