// Package labbridge is the HTTP client for the lab-bridge VPS's public
// API (see docs/superpowers/specs/2026-05-11-status-lamps-design.md).
// Stateless: callers supply *http.Client and context.Context; per-call
// timeouts live at the call site.
package labbridge

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
)

const (
	healthPath   = "/api/public/health"
	clientsPath  = "/api/public/clients/"
	maxBodyBytes = 64 << 10 // 64 KB; both responses are tiny
)

// ErrUnauthorized is returned by FetchClient on HTTP 401. The lab-bridge
// spec intentionally makes "unknown user", "wrong token", "missing
// Authorization header", and "non-Bearer scheme" indistinguishable; this
// package does not try to disambiguate them.
var ErrUnauthorized = errors.New("labbridge: unauthorized")

// ErrServerError is wrapped (via fmt.Errorf "...: %w") by FetchClient
// and FetchHealth on HTTP 5xx responses.
var ErrServerError = errors.New("labbridge: server error")

// Health is the parsed result of GET /api/public/health. The endpoint
// always returns HTTP 200; the up/down signal is in the JSON body.
type Health struct {
	ChiselOK bool
	Detail   string
}

type healthBody struct {
	Chisel string `json:"chisel"`
	Error  string `json:"error,omitempty"`
}

// FetchHealth probes the chisel-server liveness endpoint.
func FetchHealth(ctx context.Context, hc *http.Client, base, userAgent string) (Health, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+healthPath, nil)
	if err != nil {
		return Health{}, fmt.Errorf("labbridge: build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := hc.Do(req)
	if err != nil {
		return Health{}, fmt.Errorf("labbridge: do: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= 500 {
		return Health{}, fmt.Errorf("labbridge: health: %w (status %d)", ErrServerError, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return Health{}, fmt.Errorf("labbridge: health: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return Health{}, fmt.Errorf("labbridge: read health body: %w", err)
	}
	var hb healthBody
	if err := json.Unmarshal(body, &hb); err != nil {
		return Health{}, fmt.Errorf("labbridge: parse health body: %w", err)
	}
	return Health{ChiselOK: hb.Chisel == "ok", Detail: hb.Error}, nil
}

// ClientInfo and FetchClient land in Task 3.

var _ = url.PathEscape // silence the import until Task 3 uses it
