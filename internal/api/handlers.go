package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/khamitovdr/lab_devices_client/internal/registry"
	labserial "github.com/khamitovdr/lab_devices_client/internal/serial"
)

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
	writeJSON(w, http.StatusOK, DevicesResponse{
		Devices:      toDTOs(s.reg.List()),
		DiscoveredAt: s.reg.DiscoveredAt(),
	})
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

type cmdParams struct {
	timeout      time.Duration
	interByte    time.Duration
	waitForReply bool
	expectedN    int // -1 = no limit
}

func parseCmdParams(r *http.Request) (cmdParams, error) {
	p := cmdParams{
		timeout:      100 * time.Millisecond,
		interByte:    25 * time.Millisecond,
		waitForReply: true,
		expectedN:    -1,
	}
	q := r.URL.Query()

	if v := q.Get("expected_response_bytes"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return p, fmt.Errorf("expected_response_bytes: %v", err)
		}
		if n != -1 && (n < 1 || n > 1024) {
			return p, fmt.Errorf("expected_response_bytes must be -1 or 1..1024 (got %d)", n)
		}
		p.expectedN = n
	}
	// Adjust defaults based on whether the caller specified a length.
	if p.expectedN > 0 {
		p.timeout = 1000 * time.Millisecond
		p.interByte = 50 * time.Millisecond
	}

	if v := q.Get("timeout_ms"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return p, fmt.Errorf("timeout_ms: %v", err)
		}
		if n < 1 || n > 60000 {
			return p, fmt.Errorf("timeout_ms must be 1..60000 (got %d)", n)
		}
		p.timeout = time.Duration(n) * time.Millisecond
	}
	if v := q.Get("inter_byte_ms"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil {
			return p, fmt.Errorf("inter_byte_ms: %v", err)
		}
		if n < 1 || n > 1000 {
			return p, fmt.Errorf("inter_byte_ms must be 1..1000 (got %d)", n)
		}
		p.interByte = time.Duration(n) * time.Millisecond
	}
	if v := q.Get("wait_for_response"); v != "" {
		switch v {
		case "true":
			p.waitForReply = true
		case "false":
			p.waitForReply = false
		default:
			return p, fmt.Errorf("wait_for_response must be true or false (got %q)", v)
		}
	}
	return p, nil
}

func parseCommandBody(r *http.Request) ([]byte, error) {
	var body CommandRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		return nil, fmt.Errorf("body: %v", err)
	}
	if len(body.Command) == 0 {
		return nil, errors.New("command must be non-empty")
	}
	out := make([]byte, len(body.Command))
	for i, v := range body.Command {
		if v < 0 || v > 255 {
			return nil, fmt.Errorf("command[%d]=%d out of range 0..255", i, v)
		}
		out[i] = byte(v)
	}
	return out, nil
}

func bytesToInts(b []byte) []int {
	out := make([]int, len(b))
	for i, v := range b {
		out[i] = int(v)
	}
	return out
}

func (s *Server) handlePostCommand(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	dev, ok := s.reg.Get(id)
	if !ok {
		writeError(w, http.StatusNotFound, "device not found", id)
		return
	}
	if !dev.TryLock() {
		writeError(w, http.StatusConflict, "device busy", "")
		return
	}
	defer dev.Unlock()

	params, err := parseCmdParams(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid query param", err.Error())
		return
	}
	cmd, err := parseCommandBody(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	resp, err := s.executeCommand(dev, cmd, params)
	if err != nil {
		// Reconnect-and-reprobe is added in Task 11. For now, surface a 503.
		slog.Warn("command i/o failed", "device", dev.ID, "err", err)
		writeError(w, http.StatusServiceUnavailable, "device i/o failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, CommandResponse{Response: bytesToInts(resp)})
}

// executeCommand performs one write+optional-read cycle against the device's
// current connection. Returns the raw response bytes, or an error on I/O
// failure (caller decides whether to reconnect).
func (s *Server) executeCommand(dev *registry.Device, cmd []byte, p cmdParams) ([]byte, error) {
	if _, err := dev.Conn.Write(cmd); err != nil {
		return nil, fmt.Errorf("write: %w", err)
	}
	if !p.waitForReply {
		return nil, nil
	}
	resp, err := labserial.ReadFrame(dev.Conn, p.timeout, p.interByte, p.expectedN)
	if err != nil {
		return nil, fmt.Errorf("read: %w", err)
	}
	return resp, nil
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
