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

	"github.com/bioexperiment-lab-devices/serialhop/internal/discovery"
	"github.com/bioexperiment-lab-devices/serialhop/internal/flasher"
	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

type DiscoverFn func(ctx context.Context) ([]*registry.Device, error)

type Server struct {
	reg              *registry.Registry
	discover         DiscoverFn
	opener           labserial.Opener
	rawSerialEnabled bool
	flasher          flasher.Flasher
	flashingEnabled  bool
}

func New(
	reg *registry.Registry,
	discover DiscoverFn,
	opener labserial.Opener,
	rawSerialEnabled bool,
	fl flasher.Flasher,
	flashingEnabled bool,
) *Server {
	return &Server{
		reg:              reg,
		discover:         discover,
		opener:           opener,
		rawSerialEnabled: rawSerialEnabled,
		flasher:          fl,
		flashingEnabled:  flashingEnabled,
	}
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /devices", s.handleGetDevices)
	mux.HandleFunc("POST /discover", s.handlePostDiscover)
	mux.HandleFunc("POST /devices/{id}/command", s.handlePostCommand)
	mux.HandleFunc("GET /serial/ports", s.handleGetSerialPorts)
	mux.HandleFunc("POST /serial/ports/{port}/command", s.handlePostSerialCommand)
	mux.HandleFunc("POST /devices/disconnect", s.handlePostDevicesDisconnect)
	mux.HandleFunc("GET /serial/ports/detailed", s.handleGetSerialPortsDetailed)
	return mux
}

func (s *Server) handleGetDevices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, DevicesResponse{
		Devices:      toDTOs(s.reg.List()),
		DiscoveredAt: s.reg.DiscoveredAt(),
	})
}

// portSettleDelay gives the OS / USB-serial driver a moment to fully release
// COM port handles after Close before discovery tries to re-open them.
const portSettleDelay = 100 * time.Millisecond

func (s *Server) handlePostDiscover(w http.ResponseWriter, r *http.Request) {
	if !s.reg.LockDiscovery() {
		writeError(w, http.StatusConflict, "discovery in progress", "")
		return
	}
	defer s.reg.UnlockDiscovery()

	// Close all currently-open device ports before probing. Without this, the
	// next probe's Open() finds the same COM ports locked by our own old
	// handles and silently skips them, returning an empty result.
	s.reg.CloseAll()
	time.Sleep(portSettleDelay)

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

// maxCommandBodyBytes caps the JSON body size for POST /devices/{id}/command.
// A worst-case 1024-byte command serialises to ~4KB of JSON; 32KB leaves
// generous slack for whitespace and over-spec'd clients without exposing
// the process to memory exhaustion via an unbounded body.
const maxCommandBodyBytes = 32 * 1024

// maxCommandLen is the upper bound on the decoded byte command. Mirrors the
// 1024-byte ceiling on expected_response_bytes so neither direction of the
// transaction can be coerced into pathological I/O.
const maxCommandLen = 1024

func parseCommandBody(w http.ResponseWriter, r *http.Request) ([]byte, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxCommandBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var body CommandRequest
	if err := dec.Decode(&body); err != nil {
		return nil, fmt.Errorf("body: %v", err)
	}
	if len(body.Command) == 0 {
		return nil, errors.New("command must be non-empty")
	}
	if len(body.Command) > maxCommandLen {
		return nil, fmt.Errorf("command length %d exceeds max %d", len(body.Command), maxCommandLen)
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
	cmd, err := parseCommandBody(w, r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}

	start := time.Now()
	logOutcome := func(outcome string, resp []byte) {
		slog.Info("command",
			"device", dev.ID,
			"cmd_bytes", len(cmd),
			"resp_bytes", len(resp),
			"duration_ms", time.Since(start).Milliseconds(),
			"outcome", outcome,
		)
		slog.Debug("command bytes", "device", dev.ID, "cmd", bytesToInts(cmd), "resp", bytesToInts(resp))
	}

	resp, err := s.executeCommand(dev, cmd, params)
	if err == nil {
		logOutcome("ok", resp)
		writeJSON(w, http.StatusOK, CommandResponse{Response: bytesToInts(resp)})
		return
	}
	slog.Warn("command i/o failed; attempting reconnect", "device", dev.ID, "err", err)

	if recErr := s.tryReconnect(dev); recErr != nil {
		// reconnect itself failed
		switch {
		case errors.Is(recErr, errIdentityChanged):
			s.reg.Remove(dev.ID)
			logOutcome("identity_changed", nil)
			writeError(w, http.StatusServiceUnavailable, "device identity changed", recErr.Error())
		default:
			logOutcome("unreachable", nil)
			writeError(w, http.StatusServiceUnavailable, "device unreachable", recErr.Error())
		}
		return
	}

	resp, err = s.executeCommand(dev, cmd, params)
	if err != nil {
		logOutcome("io_failed", nil)
		writeError(w, http.StatusServiceUnavailable, "device i/o failed", err.Error())
		return
	}
	logOutcome("ok_after_reconnect", resp)
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

// errIdentityChanged is returned by tryReconnect when the re-probed device
// no longer matches the stored type code.
var errIdentityChanged = errors.New("device identity changed")

// tryReconnect closes the device's current connection, re-opens the port,
// re-probes it, and verifies the type code matches. On success the device's
// Conn field is replaced with the new connection.
func (s *Server) tryReconnect(dev *registry.Device) error {
	_ = dev.Conn.Close()
	dev.Conn = nil
	conn, err := dev.Opener.Open(dev.Port)
	if err != nil {
		return fmt.Errorf("reopen %s: %w", dev.Port, err)
	}
	reply, res, err := probeAdapter(conn)
	if err != nil {
		slog.Debug("reprobe: probe error",
			"device", dev.ID,
			"port", dev.Port,
			"sent", bytesToInts(discovery.ProbeBytes()),
			"reply", bytesToInts(reply),
			"err", err)
		_ = conn.Close()
		return fmt.Errorf("reprobe %s: %w", dev.Port, err)
	}
	if res == nil {
		slog.Debug("reprobe: no reply or unknown device",
			"device", dev.ID,
			"port", dev.Port,
			"sent", bytesToInts(discovery.ProbeBytes()),
			"reply", bytesToInts(reply))
		_ = conn.Close()
		return fmt.Errorf("%w: no reply on reprobe", errIdentityChanged)
	}
	slog.Debug("reprobe: got reply",
		"device", dev.ID,
		"port", dev.Port,
		"sent", bytesToInts(discovery.ProbeBytes()),
		"reply", bytesToInts(reply),
		"type", res.Type,
		"type_code", int(res.TypeCode))
	if res.TypeCode != dev.TypeCode {
		_ = conn.Close()
		return fmt.Errorf("%w: expected type=%d, got type=%d", errIdentityChanged, dev.TypeCode, res.TypeCode)
	}
	dev.Conn = conn
	return nil
}

// probeAdapter is a swappable function for tests. Defaults to the real probe.
var probeAdapter = func(p labserial.Port) ([]byte, *discovery.ProbeResult, error) {
	return discovery.Probe(p)
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
