package logship

import "time"

// clock abstracts time so the shipper's backoff and batch timer are
// testable without sleeping. Tests inject a fake; production uses
// realClock.
type clock interface {
	Now() time.Time
	Sleep(d time.Duration)
}

type realClock struct{}

func (realClock) Now() time.Time        { return time.Now() }
func (realClock) Sleep(d time.Duration) { time.Sleep(d) }
