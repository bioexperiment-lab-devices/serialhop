package api

import (
	"log/slog"
	"net/http"
	"sort"
)

func (s *Server) handlePostDevicesDisconnect(w http.ResponseWriter, r *http.Request) {
	n := s.reg.DisconnectAll()
	slog.Info("disconnect", "released", n)
	writeJSON(w, http.StatusOK, DisconnectResponse{Released: n})
}

func (s *Server) handleGetSerialPortsDetailed(w http.ResponseWriter, r *http.Request) {
	ports, err := s.opener.ListDetailed()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list ports failed", err.Error())
		return
	}
	out := make([]DetailedPortDTO, 0, len(ports))
	for _, p := range ports {
		dto := DetailedPortDTO{
			Name:         p.Name,
			IsUSB:        p.IsUSB,
			VID:          p.VID,
			PID:          p.PID,
			SerialNumber: p.SerialNumber,
			Product:      p.Product,
		}
		if id, ok := s.reg.HasPort(p.Name); ok {
			dto.Discovered = true
			dto.DeviceID = id
		}
		out = append(out, dto)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	writeJSON(w, http.StatusOK, DetailedPortsResponse{Ports: out})
}
