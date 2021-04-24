package sleepct

import (
	"fmt"
	"sync"
	"time"
)

// SleepCT - Sleep constant time step crates a sleep based ticker
// designed maintain a sleep/tick interval
type SleepCT struct {
	sync.Mutex
	d time.Duration // duration
	t time.Time     // last time target
}

func (s *SleepCT) Start(d time.Duration) {
	if s.t.IsZero() {
		s.d = d
		s.t = time.Now()
	} else {
		panic("SleepCT already started")
	}
}

func (s *SleepCT) SleepNextTarget() {
	s.Lock()

	now := time.Now()

	var last time.Time
	if s.t.IsZero() {
		fmt.Println("SleepCT reset")
		last = now.Add(-s.d)
	} else {
		last = s.t
	}

	// Next Target
	s.t = last.Add(s.d)

	d := s.t.Sub(now)

	time.Sleep(d)

	// delta := now.Sub(s.t)
	// fmt.Println("delta", delta, d, time.Since(s.t))

	s.Unlock()
}
