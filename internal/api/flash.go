package api

import (
	"log/slog"
	"net/http"
)

func (s *Server) handlePostDevicesDisconnect(w http.ResponseWriter, r *http.Request) {
	n := s.reg.DisconnectAll()
	slog.Info("disconnect", "released", n)
	writeJSON(w, http.StatusOK, DisconnectResponse{Released: n})
}
