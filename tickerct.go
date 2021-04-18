package main

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// A Ticker holds a channel that delivers ``ticks'' of a clockinter
// at intervals.
type TickerCT struct {
	sync.Mutex
	C    <-chan time.Time // The channel on which the ticks are delivered.
	c    chan<- time.Time // internal use
	r    *time.Timer      // internal timer
	d    time.Duration    // the set duration
	last time.Time        // the last time the ticker ticked
	stop bool             // mark the ticker as stopped
}

// NewTickerCT returns a new Ticker containing a channel that will send
// the time on the channel after each tick. The period of the ticks is
// specified by the duration argument. The ticker queue ticks.
// The duration d must be greater than zero; if not, NewTickerCT will
// panic. Stop the ticker to release associated resources.
func NewTickerCT(d time.Duration) *TickerCT {
	if d <= 0 {
		panic(errors.New("non-positive interval for NewTickerCT"))
	}

	// Give the channel a large buffer to allow clients to catchup
	c := make(chan time.Time, 100)

	t := &TickerCT{
		C:    c,
		c:    c,
		d:    d,
		last: time.Now(),
		stop: false,
	}

	t.Lock()
	t.r = time.AfterFunc(d, func() { t.tick() })
	t.Unlock()

	return t
}

func (t *TickerCT) tick() {
	t.Lock()
	if t.stop {
		fmt.Println("stopped")
		return
	}

	now := time.Now()
	t.c <- now

	current := t.last.Add(t.d)
	target := current.Add(t.d)

	d := target.Sub(now)

	// if d.Microseconds() < 1 {
	// 	d = time.Duration(time.Microsecond)
	// }
	// delta := now.Sub(current)
	// fmt.Println("delta", delta, d)

	t.r.Reset(d)
	t.last = current
	t.Unlock()
}

func (t *TickerCT) Stop() {
	t.stop = true
	if t.r != nil {
		t.r.Stop()
	}
}
