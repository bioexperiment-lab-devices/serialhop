package logship

import (
	"context"
	"sync"
	"testing"
	"time"
)

func TestQueuePushDrainFIFO(t *testing.T) {
	q := newQueue(4)
	q.push(record{stream: "stdout", tsNano: 1, line: "a"})
	q.push(record{stream: "stdout", tsNano: 2, line: "b"})
	q.push(record{stream: "stderr", tsNano: 3, line: "c"})

	got := q.drainUpTo(10)
	if len(got) != 3 {
		t.Fatalf("drain returned %d records, want 3", len(got))
	}
	if got[0].line != "a" || got[1].line != "b" || got[2].line != "c" {
		t.Fatalf("FIFO violated: %+v", got)
	}
	if got[2].stream != "stderr" {
		t.Fatalf("stream lost: %+v", got[2])
	}
}

func TestQueueDrainUpToBoundsBatch(t *testing.T) {
	q := newQueue(8)
	for i := 0; i < 6; i++ {
		q.push(record{tsNano: int64(i), line: "x"})
	}
	got := q.drainUpTo(4)
	if len(got) != 4 {
		t.Fatalf("drain returned %d, want 4", len(got))
	}
	rest := q.drainUpTo(10)
	if len(rest) != 2 {
		t.Fatalf("second drain returned %d, want 2", len(rest))
	}
}

func TestQueueDropsOldestOnOverflow(t *testing.T) {
	q := newQueue(3)
	q.push(record{line: "a"})
	q.push(record{line: "b"})
	q.push(record{line: "c"})
	q.push(record{line: "d"}) // drops "a"
	q.push(record{line: "e"}) // drops "b"

	got := q.drainUpTo(10)
	if len(got) != 3 {
		t.Fatalf("drain returned %d records, want 3", len(got))
	}
	if got[0].line != "c" || got[1].line != "d" || got[2].line != "e" {
		t.Fatalf("drop-oldest broken: %+v", got)
	}
	if n := q.takeDropped(); n != 2 {
		t.Fatalf("takeDropped = %d, want 2", n)
	}
}

func TestQueueTakeDroppedZeroes(t *testing.T) {
	q := newQueue(1)
	q.push(record{line: "a"})
	q.push(record{line: "b"}) // drops "a"
	if n := q.takeDropped(); n != 1 {
		t.Fatalf("first takeDropped = %d, want 1", n)
	}
	if n := q.takeDropped(); n != 0 {
		t.Fatalf("second takeDropped = %d, want 0", n)
	}
}

func TestQueueWaitNotifyFiresOnPush(t *testing.T) {
	q := newQueue(8)
	done := make(chan struct{})
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		q.waitNotify(ctx, time.Second)
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	q.push(record{line: "a"})
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("waitNotify did not return after push")
	}
}

func TestQueueWaitNotifyTimesOut(t *testing.T) {
	q := newQueue(8)
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	q.waitNotify(ctx, 30*time.Millisecond)
	if elapsed := time.Since(start); elapsed >= 500*time.Millisecond {
		t.Fatalf("waitNotify blocked too long: %v", elapsed)
	}
}

func TestQueueWaitNotifyHonorsCtx(t *testing.T) {
	q := newQueue(8)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		q.waitNotify(ctx, time.Hour)
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("waitNotify did not return on ctx cancel")
	}
}

func TestQueueConcurrentPushesAreSafe(t *testing.T) {
	q := newQueue(4096)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(seed int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				q.push(record{tsNano: int64(seed*1000 + j), line: "x"})
			}
		}(i)
	}
	wg.Wait()

	total := 0
	for {
		batch := q.drainUpTo(256)
		if len(batch) == 0 {
			break
		}
		total += len(batch)
	}
	if total != 1600 {
		t.Fatalf("drained %d records, want 1600 (no drops expected at this size)", total)
	}
}
