package main

import (
	"fmt"
	"math/rand"
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
