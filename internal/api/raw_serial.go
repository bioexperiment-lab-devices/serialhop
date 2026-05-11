package api

import (
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strconv"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/discovery"
	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// parseRawSettle returns the post-open settle for one raw-serial call.
// Defaults to discovery.PostOpenSettle (set from config at startup), allowing
// callers to override via post_open_settle_ms query param. 0 disables.
func parseRawSettle(r *http.Request) (time.Duration, error) {
	v := r.URL.Query().Get("post_open_settle_ms")
	if v == "" {
		return discovery.PostOpenSettle, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("post_open_settle_ms: %v", err)
	}
	if n < 0 || n > 60000 {
		return 0, fmt.Errorf("post_open_settle_ms must be 0..60000 (got %d)", n)
	}
	return time.Duration(n) * time.Millisecond, nil
}

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
	settle, err := parseRawSettle(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid query param", err.Error())
		return
	}
	cmd, err := parseCommandBody(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	start := time.Now()
	logOutcome := func(outcome string, resp []byte) {
		slog.Info("raw_serial_command",
			"port", port,
			"cmd_bytes", len(cmd),
			"resp_bytes", len(resp),
			"settle_ms", settle.Milliseconds(),
			"duration_ms", time.Since(start).Milliseconds(),
			"outcome", outcome,
		)
		slog.Debug("raw_serial_command bytes",
			"port", port,
			"cmd", bytesToInts(cmd),
			"resp", bytesToInts(resp),
		)
	}

	conn, err := s.opener.Open(port)
	if err != nil {
		logOutcome("open_failed", nil)
		writeError(w, http.StatusServiceUnavailable, "port open failed", err.Error())
		return
	}
	defer func() { _ = conn.Close() }()

	if settle > 0 {
		time.Sleep(settle)
	}

	if err := conn.Drain(discovery.DrainDuration); err != nil {
		logOutcome("drain_failed", nil)
		writeError(w, http.StatusServiceUnavailable, "port drain failed", err.Error())
		return
	}
	if _, err := conn.Write(cmd); err != nil {
		logOutcome("write_failed", nil)
		writeError(w, http.StatusServiceUnavailable, "port write failed", err.Error())
		return
	}
	if !params.waitForReply {
		logOutcome("ok", nil)
		writeJSON(w, http.StatusOK, CommandResponse{Response: bytesToInts(nil)})
		return
	}
	resp, err := labserial.ReadFrame(conn, params.timeout, params.interByte, params.expectedN)
	if err != nil {
		logOutcome("read_failed", resp)
		writeError(w, http.StatusServiceUnavailable, "port read failed", fmt.Sprintf("read: %v", err))
		return
	}
	logOutcome("ok", resp)
	writeJSON(w, http.StatusOK, CommandResponse{Response: bytesToInts(resp)})
}
