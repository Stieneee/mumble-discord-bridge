package main

import (
	"context"
	"fmt"
	"math"
	"math/rand"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/stieneee/mumble-discord-bridge/pkg/sleepct"
	"github.com/stieneee/tickerct"
)

const testCount int64 = 10000
const maxSleepInterval time.Duration = 15 * time.Millisecond
const tickerInterval time.Duration = 10 * time.Millisecond
const testDuration time.Duration = time.Duration(testCount * 10 * int64(time.Millisecond))

func testTickerBaseCase(wg *sync.WaitGroup, test *testing.T) {
	wg.Add(1)
	go func(interval time.Duration) {
		now := time.Now()
		start := now
		// start the ticker
		t := time.NewTicker(interval)
		var i int64
		for i = 0; i < testCount; i++ {
			now = <-t.C
			// fmt.Println(now)
		}
		t.Stop()
		fmt.Println("Ticker (unloaded) after", testDuration, "drifts", time.Since(start)-testDuration)
		wg.Done()
	}(tickerInterval)
}

func TestTickerBaseCase(t *testing.T) {
	wg := sync.WaitGroup{}

	testTickerBaseCase(&wg, t)

	wg.Wait()
}

func testTickerLoaded(wg *sync.WaitGroup) {
	wg.Add(1)
	go func(interval time.Duration) {
		now := time.Now()
		start := now
		// start the ticker
		t := time.NewTicker(interval)
		var i int64
		for i = 0; i < testCount; i++ {
			if i+1 < testCount {
				time.Sleep(time.Duration(float64(maxSleepInterval) * rand.Float64()))
			}
			now = <-t.C
			// fmt.Println(now)
		}
		t.Stop()
		fmt.Println("Ticker (loaded) after", testDuration, "drifts", time.Since(start)-testDuration)
		wg.Done()
	}(tickerInterval)
}

func TestTicker(t *testing.T) {
	wg := sync.WaitGroup{}

	testTickerLoaded(&wg)

	wg.Wait()
}

func testTickerCT(wg *sync.WaitGroup) {
	wg.Add(1)
	go func(interval time.Duration) {
		now := time.Now()
		start := now
		// start the ticker
		t := tickerct.NewTickerCT(interval)
		var i int64
		for i = 0; i < testCount; i++ {
			if i+1 < testCount {
				time.Sleep(time.Duration(float64(maxSleepInterval) * rand.Float64()))
			}
			now = <-t.C
			// fmt.Println(now)
		}
		t.Stop()
		fmt.Println("TickerCT (loaded) after", testDuration, "drifts", time.Since(start)-testDuration)
		wg.Done()
	}(tickerInterval)
}

func TestTickerCT(t *testing.T) {
	wg := sync.WaitGroup{}

	testTickerCT(&wg)

	wg.Wait()
}

func testSleepCT(wg *sync.WaitGroup) {
	wg.Add(1)
	go func(interval time.Duration) {
		now := time.Now()
		start := now
		// start the ticker
		s := sleepct.SleepCT{}
		s.Start(interval)
		var i int64
		for i = 0; i < testCount; i++ {
			if i+1 < testCount {
				time.Sleep(time.Duration(float64(maxSleepInterval) * rand.Float64()))
			}
			s.SleepNextTarget(context.TODO(), false)
		}
		fmt.Println("SleepCT (loaded) after", testDuration, "drifts", time.Since(start)-testDuration)
		wg.Done()
	}(tickerInterval)
}

func TestSleepCT(t *testing.T) {
	wg := sync.WaitGroup{}

	testSleepCT(&wg)

	wg.Wait()
}

func testSleepCTPause(wg *sync.WaitGroup) {
	wg.Add(1)
	go func(interval time.Duration) {
		now := time.Now()
		start := now
		// start the ticker
		s := sleepct.SleepCT{}
		s.Start(interval)
		var i int64
		for i = 0; i < testCount; i++ {
			if i+1 < testCount {
				time.Sleep(time.Duration(float64(maxSleepInterval) * rand.Float64()))
			}
			s.Notify()
			s.SleepNextTarget(context.TODO(), true)
		}
		fmt.Println("SleepCT Pause (loaded) after", testDuration, "drifts", time.Since(start)-testDuration)
		wg.Done()
	}(tickerInterval)
}

func TestSleepCTPause(t *testing.T) {
	wg := sync.WaitGroup{}

	testSleepCTPause(&wg)

	wg.Wait()
}

func TestIdleJitter(t *testing.T) {
	wg := sync.WaitGroup{}

	const testSize = 100000
	const sleepTarget = time.Millisecond

	res := make([]time.Duration, testSize)

	for i := 0; i < testSize; i++ {
		start := time.Now()
		target := start.Add(sleepTarget)

		time.Sleep(sleepTarget)

		res[i] = time.Since(target)
	}

	sort.Slice(res, func(i, j int) bool {
		return res[i] < res[j]
	})

	var total float64 = 0
	for i := 0; i < testSize; i++ {
		total += float64(res[i])
	}
	avg := time.Duration(total / testSize)

	nineFive := int64(math.Round(testSize * 0.95))
	nineNine := int64(math.Round(testSize * 0.99))
	nineNineNine := int64(math.Round(testSize * 0.999))

	fmt.Println("IdleJitter test", testSize, sleepTarget)
	fmt.Println("IdleJitter results min/avg/95/99/99.9/max", res[0], avg, res[nineFive], res[nineNine], res[nineNineNine], res[testSize-1])

	wg.Wait()
}
