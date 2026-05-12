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
	cases := []struct {
		name string
		h    labbridge.Health
		err  error
		want lampKind
	}{
		{"chisel ok", labbridge.Health{ChiselOK: true}, nil, lampOK},
		{"chisel down", labbridge.Health{ChiselOK: false, Detail: "connection refused"}, nil, lampChiselDown},
		{"server error", labbridge.Health{}, fmt.Errorf("wrap: %w", labbridge.ErrServerError), lampUnreachable},
		{"network error", labbridge.Health{}, errors.New("dial: connection refused"), lampUnreachable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapServerResult(tc.h, tc.err)
			if got.kind != tc.want {
				t.Errorf("kind: got %v, want %v", got.kind, tc.want)
			}
		})
	}
}

func TestMapTunnelResult(t *testing.T) {
	cases := []struct {
		name string
		info labbridge.ClientInfo
		err  error
		want lampKind
	}{
		{"connected", labbridge.ClientInfo{Port: 8089, Connected: true}, nil, lampOK},
		{"disconnected", labbridge.ClientInfo{Port: 8089, Connected: false}, nil, lampDisconnected},
		{"unauthorized", labbridge.ClientInfo{}, fmt.Errorf("wrap: %w", labbridge.ErrUnauthorized), lampAuthFailed},
		{"server error", labbridge.ClientInfo{}, fmt.Errorf("wrap: %w", labbridge.ErrServerError), lampServerError},
		{"network error", labbridge.ClientInfo{}, errors.New("dial: connection refused"), lampUnreachable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mapTunnelResult(tc.info, tc.err)
			if got.kind != tc.want {
				t.Errorf("kind: got %v, want %v", got.kind, tc.want)
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
