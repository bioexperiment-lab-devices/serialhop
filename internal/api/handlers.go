package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/agentinfo"
	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
	"github.com/bioexperiment-lab-devices/serialhop/internal/flasher"
	"github.com/bioexperiment-lab-devices/serialhop/internal/power"
	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	"github.com/bioexperiment-lab-devices/serialhop/internal/remoteupdate"
	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// DiscoverFn probes ports and returns started device sessions; wired to
// discovery.Run + device.LookupDriver by the app.
type DiscoverFn func(ctx context.Context) ([]*device.Session, error)

type Server struct {
	reg              *registry.Registry
	discover         DiscoverFn
	opener           labserial.Opener
	flasher          flasher.Flasher
	flashingEnabled  bool
	keepAwake        power.KeepAwake
	rawSerialEnabled bool
	rawIdleTimeout   time.Duration
	remoteUpdate     *remoteupdate.Manager
}

func New(
	reg *registry.Registry,
	discover DiscoverFn,
	opener labserial.Opener,
	fl flasher.Flasher,
	flashingEnabled bool,
	keepAwake power.KeepAwake,
	rawSerialEnabled bool,
	rawIdleTimeout time.Duration,
	remoteUpdate *remoteupdate.Manager,
) *Server {
	return &Server{
		reg: reg, discover: discover, opener: opener,
		flasher: fl, flashingEnabled: flashingEnabled, keepAwake: keepAwake,
		rawSerialEnabled: rawSerialEnabled, rawIdleTimeout: rawIdleTimeout,
		remoteUpdate: remoteUpdate,
	}
}

// Handler returns the HTTP routing table. Device control lives under
// /api/v1; infra routes keep their original paths (external contracts).
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/v1/devices", s.handleV1Devices)
	mux.HandleFunc("POST /api/v1/discover", s.handleV1Discover)
	mux.HandleFunc("POST /api/v1/devices/{id}/command", s.handleV1Command)
	mux.HandleFunc("POST /devices/disconnect", s.handlePostDevicesDisconnect)
	mux.HandleFunc("GET /serial/ports/detailed", s.handleGetSerialPortsDetailed)
	mux.HandleFunc("GET /serial/ports/{port}/attach", s.handleSerialAttach)
	mux.HandleFunc("POST /flash/{port}", s.handlePostFlashPort)
	mux.HandleFunc("GET /agent/info", s.handleGetAgentInfo)
	mux.HandleFunc("POST /agent/update", s.handlePostAgentUpdate)
	mux.HandleFunc("GET /agent/update/status", s.handleGetAgentUpdateStatus)
	mux.HandleFunc("GET /power/keep-awake", s.handleGetKeepAwake)
	mux.HandleFunc("POST /power/keep-awake/enable", s.handlePostKeepAwakeEnable)
	mux.HandleFunc("POST /power/keep-awake/disable", s.handlePostKeepAwakeDisable)
	return logMiddleware(mux)
}

// portSettleDelay gives the OS / USB-serial driver a moment to fully release
// COM port handles after Close before discovery tries to re-open them.
const portSettleDelay = 100 * time.Millisecond

// handleGetAgentInfo returns the agent's self-description for server-side
// polling. Best-effort: never fails. See
// docs/superpowers/specs/2026-05-18-agent-info-endpoint-design.md.
func (s *Server) handleGetAgentInfo(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, agentinfo.Snapshot())
}

// keepAwakeStatusBody is the response body for the three /power/keep-awake
// routes. Defined here, not in types.go, so it stays close to the
// handlers that produce it.
type keepAwakeStatusBody struct {
	Active bool `json:"active"`
}

// handleGetKeepAwake reports the current power-request state.
func (s *Server) handleGetKeepAwake(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, keepAwakeStatusBody{Active: s.keepAwake.Active()})
}

// handlePostKeepAwakeEnable activates the power request. Idempotent.
// On syscall failure returns 500 with the underlying error in `detail`;
// the service-side Active flag stays unchanged on failure.
func (s *Server) handlePostKeepAwakeEnable(w http.ResponseWriter, _ *http.Request) {
	const reason = "SerialHop panel: operator-requested keep-awake"
	if err := s.keepAwake.Enable(reason); err != nil {
		slog.Warn("keep-awake enable failed", "err", err)
		writeError(w, http.StatusInternalServerError, "keep-awake enable failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, keepAwakeStatusBody{Active: s.keepAwake.Active()})
}

// handlePostKeepAwakeDisable clears the power request. Idempotent. On
// syscall failure returns 500; the service-side Active flag is left at
// its current value so the next Enable short-circuits (consistent with
// our best-effort knowledge of OS state).
func (s *Server) handlePostKeepAwakeDisable(w http.ResponseWriter, _ *http.Request) {
	if err := s.keepAwake.Disable(); err != nil {
		slog.Warn("keep-awake disable failed", "err", err)
		writeError(w, http.StatusInternalServerError, "keep-awake disable failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, keepAwakeStatusBody{Active: s.keepAwake.Active()})
}
