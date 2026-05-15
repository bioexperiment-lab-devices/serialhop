package panel

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

func TestMapServerResult(t *testing.T) {
	const host = "lab.example.com"
	cases := []struct {
		name    string
		h       labbridge.Health
		err     error
		want    lampKind
		wantSub string
	}{
		{"chisel ok", labbridge.Health{ChiselOK: true}, nil, lampOK, host},
		{"chisel down", labbridge.Health{ChiselOK: false, Detail: "connection refused"}, nil, lampChiselDown, host},
		{"server error", labbridge.Health{}, fmt.Errorf("wrap: %w", labbridge.ErrServerError), lampUnreachable, ""},
		{"network error", labbridge.Health{}, errors.New("dial: connection refused"), lampUnreachable, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapServerResult(tc.h, tc.err, host)
			if got.kind != tc.want {
				t.Errorf("kind: got %v, want %v", got.kind, tc.want)
			}
			if got.sub != tc.wantSub {
				t.Errorf("sub: got %q, want %q", got.sub, tc.wantSub)
			}
		})
	}
}

func TestHostFromBase(t *testing.T) {
	cases := []struct{ in, want string }{
		{"https://lab.example.com", "lab.example.com"},
		{"http://1.2.3.4:8443/api", "1.2.3.4:8443"},
		{"lab.example.com", "lab.example.com"},
		{"", ""},
	}
	for _, tc := range cases {
		if got := hostFromBase(tc.in); got != tc.want {
			t.Errorf("hostFromBase(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestMapTunnelResult(t *testing.T) {
	cases := []struct {
		name    string
		info    labbridge.ClientInfo
		err     error
		want    lampKind
		wantSub string
	}{
		{"connected", labbridge.ClientInfo{Port: 8089, Connected: true}, nil, lampOK, "remote port 8089"},
		{"disconnected", labbridge.ClientInfo{Port: 8089, Connected: false}, nil, lampDisconnected, ""},
		{"unauthorized", labbridge.ClientInfo{}, fmt.Errorf("wrap: %w", labbridge.ErrUnauthorized), lampAuthFailed, ""},
		{"server error", labbridge.ClientInfo{}, fmt.Errorf("wrap: %w", labbridge.ErrServerError), lampServerError, ""},
		{"network error", labbridge.ClientInfo{}, errors.New("dial: connection refused"), lampUnreachable, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapTunnelResult(tc.info, tc.err)
			if got.kind != tc.want {
				t.Errorf("kind: got %v, want %v", got.kind, tc.want)
			}
			if got.sub != tc.wantSub {
				t.Errorf("sub: got %q, want %q", got.sub, tc.wantSub)
			}
		})
	}
}

func TestRunServerProbe_WritesState(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"chisel":"ok"}`))
	}))
	t.Cleanup(srv.Close)

	state := &lampState{}
	runServerProbe(context.Background(), srv.Client(), srv.URL, "ua/1", state)

	_, serverLamp, _ := state.snapshot()
	if serverLamp.kind != lampOK {
		t.Errorf("server lamp kind: got %v, want lampOK", serverLamp.kind)
	}
}

func TestRunServerProbe_EmptyBaseSetsUnreachable(t *testing.T) {
	state := &lampState{}
	runServerProbe(context.Background(), http.DefaultClient, "", "ua/1", state)

	_, serverLamp, _ := state.snapshot()
	if serverLamp.kind != lampUnreachable {
		t.Errorf("kind: got %v, want lampUnreachable", serverLamp.kind)
	}
}

func TestRunTunnelProbe_EmptyPassShortCircuits(t *testing.T) {
	called := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		called = true
	}))
	t.Cleanup(srv.Close)

	state := &lampState{}
	runTunnelProbe(context.Background(), srv.Client(), srv.URL, "u", "", "ua/1", state)

	if called {
		t.Error("HTTP server was hit despite empty pass; expected short-circuit")
	}
	_, _, tunnel := state.snapshot()
	if tunnel.kind != lampNotConfigured {
		t.Errorf("kind: got %v, want lampNotConfigured", tunnel.kind)
	}
}

func TestRunTunnelProbe_EmptyBaseSetsUnreachable(t *testing.T) {
	state := &lampState{}
	runTunnelProbe(context.Background(), http.DefaultClient, "", "u", "p", "ua/1", state)

	_, _, tunnel := state.snapshot()
	if tunnel.kind != lampUnreachable {
		t.Errorf("kind: got %v, want lampUnreachable", tunnel.kind)
	}
}

func TestRunTunnelProbe_WritesConnected(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"port":8089,"connected":true}`))
	}))
	t.Cleanup(srv.Close)

	state := &lampState{}
	runTunnelProbe(context.Background(), srv.Client(), srv.URL, "u", "p", "ua/1", state)

	_, _, tunnel := state.snapshot()
	if tunnel.kind != lampOK {
		t.Errorf("kind: got %v, want lampOK", tunnel.kind)
	}
}

func TestProbeLoop_RunsImmediatelyAndOnTick(t *testing.T) {
	var calls atomicCounter
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan struct{})
	go func() {
		probeLoop(ctx, 20*time.Millisecond, nil, func(context.Context) {
			calls.inc()
		})
		close(done)
	}()

	// Wait long enough for the immediate call + ~3 ticks.
	time.Sleep(80 * time.Millisecond)
	cancel()
	<-done

	if got := calls.load(); got < 2 {
		t.Errorf("probeLoop calls: got %d, want >=2 (immediate + at least one tick)", got)
	}
}

// atomicCounter is a tiny goroutine-safe counter, scoped to this test file.
type atomicCounter struct {
	mu sync.Mutex
	n  int
}

func (c *atomicCounter) inc() {
	c.mu.Lock()
	c.n++
	c.mu.Unlock()
}

func (c *atomicCounter) load() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.n
}

func TestTrySend_DeliversToEmptyBuffer(t *testing.T) {
	ch := make(chan struct{}, 1)
	trySend(ch)
	select {
	case <-ch:
		// got it
	default:
		t.Fatal("trySend did not deliver to empty buffered channel")
	}
}

func TestTrySend_DropsWhenBufferFull(t *testing.T) {
	ch := make(chan struct{}, 1)
	trySend(ch) // fills the buffer
	trySend(ch) // must not block; must not panic
	// Buffer should still hold exactly one item.
	<-ch
	select {
	case <-ch:
		t.Fatal("trySend queued more than one item in a buffer=1 channel")
	default:
	}
}

func TestProbeLoop_TriggerFiresCallback(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	trigger := make(chan struct{}, 1)

	// Use a very long tick interval so any callback invocation we see
	// can only have come from the trigger (or the initial priming call).
	done := make(chan struct{})
	go func() {
		probeLoop(ctx, time.Hour, trigger, func(context.Context) {
			calls.Add(1)
		})
		close(done)
	}()

	// Wait for the priming call (probeLoop runs fn once before entering
	// the ticker select).
	waitFor(t, func() bool { return calls.Load() >= 1 }, time.Second)

	// Send a trigger; expect a second invocation.
	trigger <- struct{}{}
	waitFor(t, func() bool { return calls.Load() >= 2 }, time.Second)

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("probeLoop did not return after ctx cancel")
	}
}

// waitFor polls cond until it returns true or timeout elapses.
// Test helper kept private to this file.
func waitFor(t *testing.T, cond func() bool, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("condition not satisfied within %v", timeout)
}

func TestProbeLoop_TriggerCoalescesViaBufferOne(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	gate := make(chan struct{})
	var calls atomic.Int32
	trigger := make(chan struct{}, 1)

	done := make(chan struct{})
	go func() {
		probeLoop(ctx, time.Hour, trigger, func(context.Context) {
			calls.Add(1)
			<-gate // block until the test releases
		})
		close(done)
	}()

	// Wait for the priming call to enter and block on the gate.
	waitFor(t, func() bool { return calls.Load() == 1 }, time.Second)

	// Fire 5 trySends rapidly. The buffer=1 + non-blocking send means
	// at most one signal is queued.
	for i := 0; i < 5; i++ {
		trySend(trigger)
	}

	// Release the gate once. probeLoop returns from the in-flight call,
	// enters the select, sees one queued trigger, runs fn a second time
	// (which will block on gate again).
	gate <- struct{}{}
	waitFor(t, func() bool { return calls.Load() == 2 }, time.Second)

	// Release the gate again. probeLoop returns, enters select. The
	// trigger channel is empty (coalesced), the ticker is at 1h, and
	// ctx is still live — so no further calls happen.
	gate <- struct{}{}
	time.Sleep(50 * time.Millisecond)
	if got := calls.Load(); got != 2 {
		t.Fatalf("trigger spam did not coalesce: got %d calls, want 2", got)
	}

	cancel()
	// Drain a final gate release in case the goroutine is between calls.
	select {
	case gate <- struct{}{}:
	default:
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("probeLoop did not return after ctx cancel")
	}
}

func TestProbeLoop_TickerKeepsFiring(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var calls atomic.Int32
	done := make(chan struct{})
	go func() {
		probeLoop(ctx, 20*time.Millisecond, nil, func(context.Context) {
			calls.Add(1)
		})
		close(done)
	}()

	// Initial priming call + at least two ticker-driven calls within 200 ms.
	waitFor(t, func() bool { return calls.Load() >= 3 }, 500*time.Millisecond)

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("probeLoop did not return after ctx cancel")
	}
}
