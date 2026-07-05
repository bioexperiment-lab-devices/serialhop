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

func TestFakeClockAdvanceFiresMultipleDueTimersButNotLater(t *testing.T) {
	c := NewFakeClock(time.Unix(1000, 0))
	ch1 := c.After(2 * time.Second)  // due at 1002
	ch2 := c.After(7 * time.Second)  // due at 1007
	ch3 := c.After(20 * time.Second) // due at 1020

	c.Advance(10 * time.Second) // now = 1010: fires ch1 and ch2, not ch3

	select {
	case at := <-ch1:
		if !at.Equal(time.Unix(1010, 0)) {
			t.Errorf("ch1 fired at %v", at)
		}
	default:
		t.Fatal("ch1 (due at +2s) should have fired")
	}
	select {
	case at := <-ch2:
		if !at.Equal(time.Unix(1010, 0)) {
			t.Errorf("ch2 fired at %v", at)
		}
	default:
		t.Fatal("ch2 (due at +7s) should have fired")
	}
	select {
	case <-ch3:
		t.Fatal("ch3 (due at +20s) should not have fired yet")
	default:
	}
	if !c.Now().Equal(time.Unix(1010, 0)) {
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
