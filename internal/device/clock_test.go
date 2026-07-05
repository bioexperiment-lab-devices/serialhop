package device

import (
	"testing"
	"time"
)

func TestFakeClockAdvanceFiresDueTimers(t *testing.T) {
	c := NewFakeClock(time.Unix(1000, 0))
	ch := c.After(5 * time.Second)
	select {
	case <-ch:
		t.Fatal("timer fired before Advance")
	default:
	}
	c.Advance(3 * time.Second)
	select {
	case <-ch:
		t.Fatal("timer fired too early")
	default:
	}
	c.Advance(2 * time.Second)
	select {
	case at := <-ch:
		if !at.Equal(time.Unix(1005, 0)) {
			t.Errorf("fired at %v", at)
		}
	default:
		t.Fatal("timer did not fire at its due time")
	}
	if !c.Now().Equal(time.Unix(1005, 0)) {
		t.Errorf("Now() = %v", c.Now())
	}
}

func TestFakeClockNonPositiveAfterFiresImmediately(t *testing.T) {
	c := NewFakeClock(time.Unix(0, 0))
	select {
	case <-c.After(0):
	default:
		t.Fatal("After(0) must fire immediately")
	}
}

func TestSystemClockNow(t *testing.T) {
	before := time.Now()
	got := SystemClock().Now()
	if got.Before(before.Add(-time.Second)) || got.After(before.Add(time.Second)) {
		t.Errorf("SystemClock().Now() = %v, wall = %v", got, before)
	}
}
