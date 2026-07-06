package api

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/device"
)

// maxEnvelopeBytes caps the JSON body of POST /api/v1/devices/{id}/command.
const maxEnvelopeBytes = 32 * 1024

func (s *Server) handleV1Devices(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.deviceList())
}

func (s *Server) deviceList() DevicesResponse {
	sessions := s.reg.List()
	out := DevicesResponse{
		Devices:      make([]DeviceDTO, 0, len(sessions)),
		DiscoveredAt: s.reg.DiscoveredAt(),
	}
	for _, sess := range sessions {
		dto := DeviceDTO{
			ID:        sess.ID(),
			Type:      sess.TypeName(),
			Port:      sess.PortName(),
			Connected: sess.Connected(),
		}
		if info, ok := sess.CachedInfo(); ok {
			dto.Identify = &info
		}
		out.Devices = append(out.Devices, dto)
	}
	return out
}

func (s *Server) handleV1Discover(w http.ResponseWriter, r *http.Request) {
	if !s.reg.LockDiscovery() {
		writeError(w, http.StatusConflict, "discovery in progress", "")
		return
	}
	defer s.reg.UnlockDiscovery()
	for _, sess := range s.reg.List() {
		if sess.HasActiveJob() {
			writeError(w, http.StatusConflict, "job in progress",
				sess.ID()+" has an active job; stop it before re-discovering")
			return
		}
	}
	s.reg.CloseAll()
	time.Sleep(portSettleDelay)
	sessions, err := s.discover(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, "discovery failed", err.Error())
		return
	}
	s.reg.Replace(sessions)
	// Wait out each session's initial attach attempt so the response
	// reflects real attach outcomes instead of transient connected=false.
	// The attaches run concurrently on their own session goroutines, so
	// this sequential wait costs one slowest-attach, not a sum.
	for _, sess := range sessions {
		sess.WaitFirstAttach(r.Context())
	}
	writeJSON(w, http.StatusOK, s.deviceList())
}

func (s *Server) handleV1Command(w http.ResponseWriter, r *http.Request) {
	req, ok := decodeEnvelope(w, r)
	if !ok {
		return
	}
	sess, found := s.reg.Get(r.PathValue("id"))
	if !found {
		writeJSON(w, http.StatusNotFound, device.Err(req.ID, &device.CmdError{
			Code:    device.CodeUnknownDevice,
			Message: "no device with id " + r.PathValue("id"),
		}))
		return
	}
	resp := sess.Execute(r.Context(), req)
	writeJSON(w, httpStatusFor(resp), resp)
}

// decodeEnvelope parses and validates the command envelope. On failure it
// writes the 400 invalid_request response itself and returns ok=false.
func decodeEnvelope(w http.ResponseWriter, r *http.Request) (device.Request, bool) {
	var req device.Request
	body := http.MaxBytesReader(w, r.Body, maxEnvelopeBytes)
	if err := json.NewDecoder(body).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, device.Err(req.ID, &device.CmdError{
			Code:    device.CodeInvalidRequest,
			Message: "body is not a valid command envelope: " + err.Error(),
		}))
		return device.Request{}, false
	}
	if req.ID == "" || req.Cmd == "" {
		writeJSON(w, http.StatusBadRequest, device.Err(req.ID, &device.CmdError{
			Code:    device.CodeInvalidRequest,
			Message: `"id" and "cmd" are required`,
		}))
		return device.Request{}, false
	}
	return req, true
}

// httpStatusFor mirrors the envelope outcome as an HTTP status (spec §4).
// Device-decided outcomes are 200; hub-level unreachability is 503.
func httpStatusFor(resp device.Response) int {
	if resp.Error != nil && resp.Error.Code == device.CodeDeviceUnreachable {
		return http.StatusServiceUnavailable
	}
	return http.StatusOK
}
