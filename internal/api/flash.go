package api

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/flasher"
)

func (s *Server) handlePostDevicesDisconnect(w http.ResponseWriter, r *http.Request) {
	n := s.reg.DisconnectAll()
	slog.Info("disconnect", "released", n)
	writeJSON(w, http.StatusOK, DisconnectResponse{Released: n})
}

func (s *Server) handlePostDevicesDisconnectByPort(w http.ResponseWriter, r *http.Request) {
	port := r.PathValue("port")
	if s.reg.DisconnectByPort(port) {
		slog.Info("disconnect_port", "port", port, "released", 1)
		writeJSON(w, http.StatusOK, DisconnectResponse{Released: 1})
		return
	}
	slog.Info("disconnect_port", "port", port, "released", 0)
	writeError(w, http.StatusNotFound, "device not found", port)
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

const maxFlashBodyBytes = 256 * 1024

func (s *Server) handlePostFlashPort(w http.ResponseWriter, r *http.Request) {
	if !s.flashingEnabled {
		writeError(w, http.StatusForbidden, "flashing disabled", "set flashing.enabled: true in config")
		return
	}
	port := r.PathValue("port")

	r.Body = http.MaxBytesReader(w, r.Body, maxFlashBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	var body FlashRequest
	if err := dec.Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	if body.Firmware == "" {
		writeError(w, http.StatusBadRequest, "invalid request body", "firmware: required")
		return
	}
	if (body.TestCommand == "") != (body.ExpectedResponse == "") {
		writeError(w, http.StatusBadRequest, "invalid request body",
			"test_command and expected_response: both or neither must be set")
		return
	}
	testCmd, err := decodeHexField("test_command", body.TestCommand)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	expected, err := decodeHexField("expected_response", body.ExpectedResponse)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", err.Error())
		return
	}
	firmware, err := flasher.ParseIntelHex([]byte(body.Firmware))
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body", "firmware: "+err.Error())
		return
	}
	timeoutMs := 100
	if body.TimeoutMs != nil {
		if *body.TimeoutMs < 1 || *body.TimeoutMs > 60000 {
			writeError(w, http.StatusBadRequest, "invalid request body",
				fmt.Sprintf("timeout_ms must be 1..60000 (got %d)", *body.TimeoutMs))
			return
		}
		timeoutMs = *body.TimeoutMs
	}
	interByteMs := 25
	if body.InterByteMs != nil {
		if *body.InterByteMs < 1 || *body.InterByteMs > 1000 {
			writeError(w, http.StatusBadRequest, "invalid request body",
				fmt.Sprintf("inter_byte_ms must be 1..1000 (got %d)", *body.InterByteMs))
			return
		}
		interByteMs = *body.InterByteMs
	}
	settleMs := 2000
	if body.PostOpenSettleMs != nil {
		if *body.PostOpenSettleMs < 0 || *body.PostOpenSettleMs > 60000 {
			writeError(w, http.StatusBadRequest, "invalid request body",
				fmt.Sprintf("post_open_settle_ms must be 0..60000 (got %d)", *body.PostOpenSettleMs))
			return
		}
		settleMs = *body.PostOpenSettleMs
	}

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
	if len(s.reg.List()) > 0 {
		writeError(w, http.StatusConflict, "registry not empty", "POST /devices/disconnect first")
		return
	}
	if s.reg.IsDiscovering() {
		writeError(w, http.StatusConflict, "discovery in progress", "")
		return
	}

	res, err := s.flasher.Flash(r.Context(), port, flasher.Request{
		Firmware:         firmware,
		TestCommand:      testCmd,
		ExpectedResponse: expected,
		Timeout:          time.Duration(timeoutMs) * time.Millisecond,
		InterByte:        time.Duration(interByteMs) * time.Millisecond,
		PostOpenSettle:   time.Duration(settleMs) * time.Millisecond,
		SkipBackup:       body.SkipBackup,
	})
	if err != nil {
		if errors.Is(err, flasher.ErrBusy) {
			writeError(w, http.StatusConflict, "flash in flight", "")
			return
		}
		writeError(w, http.StatusInternalServerError, "flash failed", err.Error())
		return
	}
	writeJSON(w, http.StatusOK, mapFlashResult(res, port))
}

func decodeHexField(name, value string) ([]byte, error) {
	if value == "" {
		return nil, nil
	}
	out, err := hex.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("%s: %v", name, err)
	}
	return out, nil
}

func mapFlashResult(res *flasher.Result, port string) FlashResponse {
	out := FlashResponse{
		Outcome:      res.Outcome.String(),
		Port:         port,
		Stages:       map[string]StageDTO{},
		RecoveryHint: res.RecoveryHint,
	}
	for name, st := range res.Stages {
		dto := StageDTO{
			Status:     st.Status,
			DurationMs: st.Duration.Milliseconds(),
			Error:      st.Error,
		}
		if st.FirstMismatchOffset != nil {
			dto.FirstMismatchOffset = fmt.Sprintf("0x%04X", *st.FirstMismatchOffset)
		}
		if st.VerifyStatus != "" {
			dto.VerifyStatus = st.VerifyStatus
		}
		out.Stages[name] = dto
	}
	scope := "flash_only"
	if backupStage, ok := res.Stages["backup"]; ok && backupStage.Status == "skipped" {
		scope = "skipped"
	}
	out.Backup = BackupDTO{
		Hex:       res.BackupHex,
		SavedPath: res.Backup.Path,
		SHA256:    res.Backup.SHA256,
		SizeBytes: res.Backup.SizeBytes,
		Scope:     scope,
	}
	if res.TestResult != nil {
		out.TestResult = &TestResultDTO{
			Sent:     hex.EncodeToString(res.TestResult.Sent),
			Expected: hex.EncodeToString(res.TestResult.Expected),
			Received: hex.EncodeToString(res.TestResult.Received),
			Match:    res.TestResult.Match,
		}
	}
	return out
}
