package api

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/bioexperiment-lab-devices/serialhop/internal/remoteupdate"
)

const maxUpdateBody = 4 * 1024

// handlePostAgentUpdate triggers an admin-pushed update. Returns 404 when the
// feature is disabled (indistinguishable from a binary that never had it),
// 202 when a job starts, 200 for a no-op (already current), or a mapped error.
func (s *Server) handlePostAgentUpdate(w http.ResponseWriter, r *http.Request) {
	if s.remoteUpdate == nil || !s.remoteUpdate.Enabled() {
		writeError(w, http.StatusNotFound, "not found", "")
		return
	}
	var req UpdateRequest
	body := http.MaxBytesReader(w, r.Body, maxUpdateBody)
	if err := json.NewDecoder(body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
		writeError(w, http.StatusBadRequest, "invalid request", "body is not valid JSON: "+err.Error())
		return
	}
	acc, err := s.remoteUpdate.Trigger(r.Context(), remoteupdate.Request{
		Version: req.Version, URL: req.URL, SHA256: req.SHA256,
	})
	if err != nil {
		writeError(w, statusForTriggerErr(err), triggerErrCode(err), triggerErrDetail(err))
		return
	}
	if acc.Noop {
		writeJSON(w, http.StatusOK, UpdateNoopBody{Outcome: "noop", Reason: acc.Reason})
		return
	}
	writeJSON(w, http.StatusAccepted, UpdateAcceptedBody{Accepted: true, To: acc.To})
}

// handleGetAgentUpdateStatus reports the last-known update result. 404 when the
// feature is disabled.
func (s *Server) handleGetAgentUpdateStatus(w http.ResponseWriter, _ *http.Request) {
	if s.remoteUpdate == nil || !s.remoteUpdate.Enabled() {
		writeError(w, http.StatusNotFound, "not found", "")
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, s.remoteUpdate.Status())
}

func statusForTriggerErr(err error) int {
	switch {
	case errors.Is(err, remoteupdate.ErrDisabled):
		return http.StatusNotFound
	case errors.Is(err, remoteupdate.ErrInProgress):
		return http.StatusConflict
	}
	var bad *remoteupdate.BadRequestError
	if errors.As(err, &bad) {
		return http.StatusBadRequest
	}
	var up *remoteupdate.UpstreamError
	if errors.As(err, &up) {
		return http.StatusBadGateway
	}
	return http.StatusInternalServerError
}

func triggerErrCode(err error) string {
	switch statusForTriggerErr(err) {
	case http.StatusConflict:
		return "update in progress"
	case http.StatusBadRequest:
		return "invalid request"
	case http.StatusBadGateway:
		return "release lookup failed"
	case http.StatusNotFound:
		return "not found"
	default:
		return "internal error"
	}
}

func triggerErrDetail(err error) string {
	var bad *remoteupdate.BadRequestError
	if errors.As(err, &bad) {
		return bad.Msg
	}
	var up *remoteupdate.UpstreamError
	if errors.As(err, &up) {
		return up.Err.Error()
	}
	return ""
}
