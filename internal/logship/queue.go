package logship

import (
	"context"
	"sync"
	"sync/atomic"
	"time"
)

// record is one captured log line on its way to Loki.
type record struct {
	stream string // "stdout" | "stderr"
	tsNano int64
	line   string
}

// queue is a bounded ring buffer with drop-oldest semantics.
//
// push never blocks. drainUpTo is non-blocking. waitNotify blocks until
// the next push, ctx cancellation, or the supplied timeout — whichever
// comes first.
type queue struct {
	mu      sync.Mutex
	buf     []record
	head    int // next write index
	size    int
	dropped uint64 // atomic
	notify  chan struct{}
}

func newQueue(capacity int) *queue {
	if capacity <= 0 {
		panic("queue capacity must be > 0")
	}
	return &queue{
		buf:    make([]record, capacity),
		notify: make(chan struct{}, 1),
	}
}

func (q *queue) push(r record) {
	q.mu.Lock()
	if q.size == len(q.buf) {
		// Full — overwrite the oldest. head currently points to the
		// oldest (since size==cap, head is both oldest and next-write).
		q.buf[q.head] = r
		q.head = (q.head + 1) % len(q.buf)
		q.mu.Unlock()
		atomic.AddUint64(&q.dropped, 1)
	} else {
		idx := (q.head + q.size) % len(q.buf)
		q.buf[idx] = r
		q.size++
		q.mu.Unlock()
	}
	// Non-blocking signal.
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

func (q *queue) drainUpTo(n int) []record {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.size == 0 {
		return nil
	}
	if n > q.size {
		n = q.size
	}
	out := make([]record, n)
	for i := 0; i < n; i++ {
		out[i] = q.buf[q.head]
		q.head = (q.head + 1) % len(q.buf)
	}
	q.size -= n
	return out
}

func (q *queue) takeDropped() uint64 {
	return atomic.SwapUint64(&q.dropped, 0)
}

// waitNotify blocks until: notify fires, ctx is done, or timeout elapses.
func (q *queue) waitNotify(ctx context.Context, timeout time.Duration) {
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case <-q.notify:
	case <-ctx.Done():
	case <-t.C:
	}
}
