package device

import (
	"sync"
	"time"
)

// Clock abstracts time so every clock-driven behavior (job progress,
// completion timers, canaries, reattach backoff) is deterministic in tests.
// Serial I/O deadlines intentionally stay on real time.
type Clock interface {
	Now() time.Time
	After(d time.Duration) <-chan time.Time
}

type sysClock struct{}

func (sysClock) Now() time.Time                         { return time.Now() }
func (sysClock) After(d time.Duration) <-chan time.Time { return time.After(d) }

// SystemClock returns the real-time Clock.
func SystemClock() Clock { return sysClock{} }

// FakeClock is a manually advanced Clock for tests.
type FakeClock struct {
	mu      sync.Mutex
	now     time.Time
	waiters []fakeWaiter
}

type fakeWaiter struct {
	at time.Time
	ch chan time.Time
}

func NewFakeClock(start time.Time) *FakeClock { return &FakeClock{now: start} }

func (c *FakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *FakeClock) After(d time.Duration) <-chan time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	ch := make(chan time.Time, 1)
	if d <= 0 {
		ch <- c.now
		return ch
	}
	c.waiters = append(c.waiters, fakeWaiter{at: c.now.Add(d), ch: ch})
	return ch
}

// Advance moves the clock forward and fires every timer due by the new time.
func (c *FakeClock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = c.now.Add(d)
	remaining := c.waiters[:0]
	for _, w := range c.waiters {
		if !w.at.After(c.now) {
			w.ch <- c.now
		} else {
			remaining = append(remaining, w)
		}
	}
	c.waiters = remaining
}
