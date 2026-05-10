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

func (s *Server) handlePostSerialCommand(w http.ResponseWriter, r *http.Request) {
	if !s.rawSerialEnabled {
		slog.Debug("raw_serial_disabled", "path", r.URL.Path)
		writeError(w, http.StatusForbidden, "raw serial disabled", "set raw_serial.enabled: true in config")
		return
	}
	port := r.PathValue("port")

	names, err := s.opener.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list ports failed", err.Error())
		return
	}
	found := false
	for _, n := range names {
		if n == port {
			found = true
			break
		}
	}
	if !found {
		writeError(w, http.StatusNotFound, "port not found", port)
		return
	}

	if id, ok := s.reg.HasPort(port); ok {
		writeError(w, http.StatusConflict, "port has discovered device",
			"use /devices/"+id+"/command instead")
		return
	}

	if s.reg.IsDiscovering() {
		writeError(w, http.StatusConflict, "discovery in progress", "")
		return
	}

	params, err := parseCmdParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid query param", err.Error())
		return
	}
	cmd, err := parseCommandBody(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	// I/O follows in Task 6.
	_ = params
	_ = cmd
	writeError(w, http.StatusNotImplemented, "not implemented", "")
}
