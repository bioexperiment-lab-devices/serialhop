package api

import (
	"context"
	"net/http"

	"github.com/khamitovdr/lab_devices_client/internal/registry"
)

// DiscoverFn runs a fresh discovery pass and returns the new device set.
// It is supplied by main wiring so the api package does not depend on
// internal/discovery directly (keeps the test surface simple).
type DiscoverFn func(ctx context.Context) ([]*registry.Device, error)

type Server struct {
	reg      *registry.Registry
	discover DiscoverFn
}

func New(reg *registry.Registry, discover DiscoverFn) *Server {
	return &Server{reg: reg, discover: discover}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /devices", s.handleGetDevices)
	mux.HandleFunc("POST /discover", s.handlePostDiscover)
	mux.HandleFunc("POST /devices/{id}/command", s.handlePostCommand)
	return mux
}

func (s *Server) handleGetDevices(w http.ResponseWriter, r *http.Request) {
	resp := DevicesResponse{
		Devices:      toDTOs(s.reg.List()),
		DiscoveredAt: s.reg.DiscoveredAt(),
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handlePostDiscover(w http.ResponseWriter, r *http.Request) {
	if !s.reg.LockDiscovery() {
		writeError(w, http.StatusConflict, "discovery in progress", "")
		return
	}
	defer s.reg.UnlockDiscovery()

	devs, err := s.discover(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "discovery failed", err.Error())
		return
	}
	s.reg.Replace(devs)
	writeJSON(w, http.StatusOK, DevicesResponse{
		Devices:      toDTOs(s.reg.List()),
		DiscoveredAt: s.reg.DiscoveredAt(),
	})
}

// handlePostCommand is implemented in Task 10.
func (s *Server) handlePostCommand(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusNotImplemented, "not implemented", "")
}

func toDTOs(devs []*registry.Device) []DeviceDTO {
	out := make([]DeviceDTO, 0, len(devs))
	for _, d := range devs {
		out = append(out, DeviceDTO{
			ID:       d.ID,
			Type:     d.Type,
			TypeCode: d.TypeCode,
			Port:     d.Port,
		})
	}
	return out
}
