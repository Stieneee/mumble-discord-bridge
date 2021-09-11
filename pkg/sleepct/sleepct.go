package sleepct

import (
	"fmt"
	"time"
)

// SleepCT - Sleep constant time step crates a sleep based ticker.
// designed maintain a consistent sleep/tick interval.
// The sleeper can be paused waiting to be signaled from another go routine.
// This allows for the pausing of loops that do not have work to complete
type SleepCT struct {
	d      time.Duration // desired duration between targets
	t      time.Time     // last time target
	resume chan bool
}

func (s *SleepCT) Start(d time.Duration) {
	s.resume = make(chan bool, 1)
	if s.t.IsZero() {
		s.d = d
		s.t = time.Now()
	} else {
		panic("SleepCT already started")
	}
}

// Sleep to the next target duration.
// If pause it set to true will sleep the duration and wait to be notified.
// The notification channel will be cleared when the thread wakes.
// SleepNextTarget should not be call more than once concurrently.
func (s *SleepCT) SleepNextTarget(pause bool) int64 {
	var last time.Time

	now := time.Now()

	if s.t.IsZero() {
		fmt.Println("SleepCT reset")
		last = now.Add(-s.d)
	} else {
		last = s.t
	}

	// Sleep to Next Target
	s.t = last.Add(s.d)

	d := time.Until(s.t)

	time.Sleep(d)

	if pause {
		// wait until resume
		if len(s.resume) == 0 {
			<-s.resume
			// if we did pause set the last sleep target to now
			last = time.Now()
		}
	}

	// Drain the resume channel
	select {
	case <-s.resume:
	default:
	}

	return now.Sub(s.t).Microseconds()
}

// Notify attempts to resume a paused sleeper.
// It is safe to call notify from other processes and as often as desired.
func (s *SleepCT) Notify() {
	s.resume <- true
}
