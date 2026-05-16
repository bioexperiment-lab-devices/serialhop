package panel

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/api"
	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
)

// ServiceCliStatus is the three-way reachability outcome the panel
// exposes to operators as the empty-state banner on the Devices and
// Ports tabs. See spec §6.4 / §7.3.
type ServiceCliStatus int

const (
	StatusOK ServiceCliStatus = iota
	// StatusUnreachable — the bootstrap cache is missing, corrupt /
	// version-mismatched, or ActualRestPort == 0. The panel doesn't
	// know where the service is even if it is running. Show:
	// "Can't reach the local service. It may have just started — wait
	// a few seconds and click Refresh."
	StatusUnreachable
	// StatusServiceDown — we know the port but the HTTP call failed
	// (connection refused, timeout, etc.). Show:
	// "Service is not running. Start it from the Status tab."
	StatusServiceDown
)

// ServiceCli is a thin typed HTTP client that talks to the local
// SerialHop service over 127.0.0.1:<ActualRestPort>. It reads the
// bootstrap cache per call so a service restart while the panel is
// open doesn't strand it on a stale port. The cache is read unanchored
// (via ReadCacheRaw): the local REST listener belongs to whichever
// service is running, regardless of whether the YAML's lab_bridge.user
// has since been edited.
type ServiceCli struct {
	cachePath string
	hc        *http.Client
}

// NewServiceCli returns a client anchored only to the given bootstrap-
// cache path. The HTTP client has a 5 s per-call timeout.
func NewServiceCli(cachePath string) *ServiceCli {
	return &ServiceCli{
		cachePath: cachePath,
		hc:        &http.Client{Timeout: 5 * time.Second},
	}
}

// baseURL reads the cache and returns "http://127.0.0.1:<port>".
// Returns StatusUnreachable on any cache-read failure or zero port.
func (c *ServiceCli) baseURL() (string, ServiceCliStatus) {
	cache, err := bootstrap.ReadCacheRaw(c.cachePath)
	if err != nil {
		return "", StatusUnreachable
	}
	if cache.ActualRestPort == 0 {
		return "", StatusUnreachable
	}
	return "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(cache.ActualRestPort)), StatusOK
}

func (c *ServiceCli) do(ctx context.Context, method, path string, out any) (ServiceCliStatus, error) {
	base, status := c.baseURL()
	if status != StatusOK {
		return status, nil
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, nil)
	if err != nil {
		return StatusOK, fmt.Errorf("build request: %w", err)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		// Treat any transport-level error as service-down; the caller has
		// no actionable distinction between "refused" and "timeout".
		return StatusServiceDown, nil
	}
	defer resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != 200 {
		return StatusServiceDown, fmt.Errorf("unexpected status %d", resp.StatusCode)
	}
	if out != nil {
		if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
			return StatusServiceDown, fmt.Errorf("decode: %w", err)
		}
	}
	return StatusOK, nil
}

// GetDevices proxies GET /devices.
func (c *ServiceCli) GetDevices(ctx context.Context) (api.DevicesResponse, ServiceCliStatus, error) {
	var out api.DevicesResponse
	status, err := c.do(ctx, "GET", "/devices", &out)
	return out, status, err
}

// Discover proxies POST /discover.
func (c *ServiceCli) Discover(ctx context.Context) (api.DevicesResponse, ServiceCliStatus, error) {
	var out api.DevicesResponse
	status, err := c.do(ctx, "POST", "/discover", &out)
	return out, status, err
}

// DisconnectAll proxies POST /devices/disconnect.
func (c *ServiceCli) DisconnectAll(ctx context.Context) (api.DisconnectResponse, ServiceCliStatus, error) {
	var out api.DisconnectResponse
	status, err := c.do(ctx, "POST", "/devices/disconnect", &out)
	return out, status, err
}

// GetPorts proxies GET /serial/ports/detailed.
func (c *ServiceCli) GetPorts(ctx context.Context) (api.DetailedPortsResponse, ServiceCliStatus, error) {
	var out api.DetailedPortsResponse
	status, err := c.do(ctx, "GET", "/serial/ports/detailed", &out)
	return out, status, err
}
