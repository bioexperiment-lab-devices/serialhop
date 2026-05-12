package panel

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/labbridge"
)

// probeTimeout is the per-call deadline applied inside runServerProbe /
// runTunnelProbe. Independent of the probe-loop tick interval.
const probeTimeout = 5 * time.Second

// probeInterval is the periodic-tick cadence for runServerProbe /
// runTunnelProbe. Slow (30 s) because explicit triggers from action
// handlers now cover the responsive cases — the periodic tick exists
// only to detect drift the UI can't observe (server going down, etc.).
const probeInterval = 30 * time.Second

// mapServerResult turns a (Health, error) pair from labbridge.FetchHealth
// into a netLamp for the Server row.
func mapServerResult(h labbridge.Health, err error) netLamp {
	if err != nil {
		// All error classes (network, 5xx, parse) collapse to Unreachable
		// for the Server lamp — the operator just needs to know the server
		// is not responding usefully right now.
		return netLamp{kind: lampUnreachable, detail: err.Error()}
	}
	if h.ChiselOK {
		return netLamp{kind: lampOK}
	}
	return netLamp{kind: lampChiselDown, detail: h.Detail}
}

// mapTunnelResult turns a (ClientInfo, error) pair from labbridge.FetchClient
// into a netLamp for the Tunnel row.
func mapTunnelResult(info labbridge.ClientInfo, err error) netLamp {
	switch {
	case errors.Is(err, labbridge.ErrUnauthorized):
		return netLamp{kind: lampAuthFailed}
	case errors.Is(err, labbridge.ErrServerError):
		return netLamp{kind: lampServerError}
	case err != nil:
		return netLamp{kind: lampUnreachable, detail: err.Error()}
	case info.Connected:
		return netLamp{kind: lampOK}
	default:
		return netLamp{kind: lampDisconnected}
	}
}

// runServerProbe performs one /api/public/health request (or short-circuits
// if base is empty) and writes the resulting netLamp into state.
func runServerProbe(ctx context.Context, hc *http.Client, base, userAgent string, state *lampState) {
	if base == "" {
		state.setServer(netLamp{kind: lampUnreachable})
		return
	}
	cctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	h, err := labbridge.FetchHealth(cctx, hc, base, userAgent)
	state.setServer(mapServerResult(h, err))
}

// runTunnelProbe performs one /api/public/clients/{user} request, or
// short-circuits to Unreachable / NotConfigured when its inputs are
// missing, and writes the resulting netLamp into state.
func runTunnelProbe(ctx context.Context, hc *http.Client, base, user, pass, userAgent string, state *lampState) {
	if base == "" {
		state.setTunnel(netLamp{kind: lampUnreachable})
		return
	}
	if pass == "" {
		state.setTunnel(netLamp{kind: lampNotConfigured})
		return
	}
	cctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	info, err := labbridge.FetchClient(cctx, hc, base, user, pass, userAgent)
	state.setTunnel(mapTunnelResult(info, err))
}

// probeLoop runs fn(ctx) immediately, then again on every tick of a
// time.Ticker(interval), until ctx is canceled. fn is expected to be
// short-running (a single HTTP request with its own timeout); if fn
// outlasts a tick, the next tick simply waits — no concurrent invocations.
// A defer/recover wraps each call so a panic in net/http or JSON parsing
// doesn't kill the panel; panics are reported via writePanelDebugLog.
//
// A receive on trigger also invokes fn — used by UI-thread callers
// (action handlers) to refresh a lamp without waiting for the next tick.
// trigger may be nil, in which case the trigger case never selects.
func probeLoop(ctx context.Context, interval time.Duration, trigger <-chan struct{}, fn func(context.Context)) {
	call := func() {
		defer func() {
			if r := recover(); r != nil {
				writePanelDebugLog("probe_panic", errors.New(panicString(r)))
			}
		}()
		fn(ctx)
	}
	call()
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			call()
		case <-trigger:
			call()
		}
	}
}

func panicString(r any) string {
	switch v := r.(type) {
	case string:
		return v
	case error:
		return v.Error()
	default:
		return "non-string, non-error panic"
	}
}

// trySend delivers one signal to ch if its buffer has room; otherwise
// drops the signal. Used by UI-thread callers to wake a probe goroutine
// without ever blocking. Pair with a chan struct{} of buffer=1 — that
// combination naturally coalesces bursts to at most one extra run.
func trySend(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}
