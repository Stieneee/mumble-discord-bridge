package main

import (
	"fmt"
	"math"
	"math/rand"
	"sort"
	"sync"
	"testing"
	"time"
)

const testCount int64 = 10000
const maxSleepInterval time.Duration = 15 * time.Millisecond
const tickerInterval time.Duration = 10 * time.Millisecond
const testDuration time.Duration = time.Duration(testCount * 10 * int64(time.Millisecond))

func Testticker(wg *sync.WaitGroup) {
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
		fmt.Println("Ticker after", testDuration, "drifts", time.Since(start)-testDuration)
		wg.Done()
	}(tickerInterval)
}

func TestTicker(t *testing.T) {
	wg := sync.WaitGroup{}

	Testticker(&wg)

	wg.Wait()
}

func TesttickerCT(wg *sync.WaitGroup) {
	wg.Add(1)
	go func(interval time.Duration) {
		now := time.Now()
		start := now
		// start the ticker
		t := NewTickerCT(interval)
		var i int64
		for i = 0; i < testCount; i++ {
			if i+1 < testCount {
				time.Sleep(time.Duration(float64(maxSleepInterval) * rand.Float64()))
			}
			now = <-t.C
			// fmt.Println(now)
		}
		t.Stop()
		fmt.Println("TickerCT after", testDuration, "drifts", time.Since(start)-testDuration)
		wg.Done()
	}(tickerInterval)
}

func TestTickerCT(t *testing.T) {
	wg := sync.WaitGroup{}

	TesttickerCT(&wg)

	wg.Wait()
}

func testSleepCT(wg *sync.WaitGroup) {
	wg.Add(1)
	go func(interval time.Duration) {
		now := time.Now()
		start := now
		// start the ticker
		s := SleepCT{
			d: interval,
			t: time.Now(),
		}
		var i int64
		for i = 0; i < testCount; i++ {
			if i+1 < testCount {
				time.Sleep(time.Duration(float64(maxSleepInterval) * rand.Float64()))
			}
			s.SleepNextTarget()
		}
		fmt.Println("SleepCT after", testDuration, "drifts", time.Since(start)-testDuration)
		wg.Done()
	}(tickerInterval)
}

func TestSleepCT(t *testing.T) {
	wg := sync.WaitGroup{}

	testSleepCT(&wg)

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
