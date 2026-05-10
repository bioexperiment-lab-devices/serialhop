package api

import (
	"log/slog"
	"net/http"
	"sort"
)

func (s *Server) handleGetSerialPorts(w http.ResponseWriter, r *http.Request) {
	if !s.rawSerialEnabled {
		slog.Debug("raw_serial_disabled", "path", r.URL.Path)
		writeError(w, http.StatusForbidden, "raw serial disabled", "set raw_serial.enabled: true in config")
		return
	}
	names, err := s.opener.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list ports failed", err.Error())
		return
	}
	sort.Strings(names)
	out := make([]PortDTO, 0, len(names))
	for _, n := range names {
		dto := PortDTO{Name: n}
		if id, ok := s.reg.HasPort(n); ok {
			dto.Discovered = true
			dto.DeviceID = id
		}
		out = append(out, dto)
	}
	slog.Info("raw_serial_list", "count", len(out))
	writeJSON(w, http.StatusOK, PortsResponse{Ports: out})
}
