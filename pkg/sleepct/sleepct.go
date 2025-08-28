// Package sleepct provides precise sleep timing functionality.
package sleepct

import (
	"context"
	"fmt"
	"time"
)

// SleepCT - Sleep constant time step crates a sleep based ticker.
// designed to maintain a consistent sleep/tick interval.
// The sleeper can be paused waiting to be signaled from another go routine.
// This allows for the pausing of loops that do not have work to complete
type SleepCT struct {
	d      time.Duration // desired duration between targets
	t      time.Time     // last time target
	resume chan bool
	wake   time.Time // last wake time
	drift  int64     // last wake drift microseconds
}

// Start initializes the sleep controller with a target duration.
func (s *SleepCT) Start(d time.Duration) {
	s.resume = make(chan bool, 2)
	if s.t.IsZero() {
		s.d = d
		s.t = time.Now()
	} else {
		panic("SleepCT already started")
	}
}

// SleepNextTarget sleeps to the next target duration.
// If pause it set to true will sleep the duration and wait to be notified.
// The notification channel will be cleared when the thread wakes.
// SleepNextTarget should not be called more than once concurrently.
func (s *SleepCT) SleepNextTarget(ctx context.Context, pause bool) int64 {
	now := time.Now()

	// if target is zero safety net
	if s.t.IsZero() {
		fmt.Println("SleepCT reset")
		s.t = now.Add(-s.d)
	}

	// Sleep to Next Target
	s.t = s.t.Add(s.d)

	// Compute the desired sleep time to reach the target
	d := time.Until(s.t)

	// Sleep
	time.Sleep(d)

	// record the wake time
	s.wake = time.Now()
	s.drift = s.wake.Sub(s.t).Microseconds()

	// fmt.Println(s.t.UnixMilli(), d.Milliseconds(), wake.UnixMilli(), drift, pause, len(s.resume))

	// external pause control
	if pause {
		// don't pause if the notification channel has something
		if len(s.resume) == 0 {
			// fmt.Println("pause")
			select {
			case <-s.resume:
			case <-ctx.Done():
				// fmt.Println("sleepct ctx exit")
			}
			// if we did pause set the last sleep target to now
			s.t = time.Now()
		}
	}

	// Drain the resume channel
	select {
	case <-s.resume:
	default:
	}

	// return the drift for monitoring purposes
	return s.drift
}

// Notify attempts to resume a paused sleeper.
// It is safe to call notify from other processes and as often as desired.
func (s *SleepCT) Notify() {
	select {
	case s.resume <- true:
	default:
	}
}
