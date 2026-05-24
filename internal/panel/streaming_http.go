package panel

import (
	"encoding/json"
	"net/http"

	"github.com/bioexperiment-lab-devices/serialhop/internal/streamer"
)

// streamingHandler returns the http.Handler that serves the three
// protocol endpoints. The service-side proxy in internal/api will
// connect to this handler over loopback.
func streamingHandler(m streamer.Manager) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/translations", func(w http.ResponseWriter, r *http.Request) {
		writeStreamingJSON(w, http.StatusOK, map[string]any{
			"translations": m.Translations(),
		})
	})
	mux.HandleFunc("POST /api/translations/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req streamer.StartRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeStreamingJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
			return
		}
		out := m.Start(r.Context(), id, req)
		writeStreamingJSON(w, out.Status, out.Body)
	})
	mux.HandleFunc("POST /api/translations/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		var req streamer.StopRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeStreamingJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
			return
		}
		out := m.Stop(id, req.SessionID)
		if out.Status == http.StatusNoContent {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		writeStreamingJSON(w, out.Status, out.Body)
	})
	return mux
}

// writeStreamingJSON writes JSON; nil/empty body is rendered as {}.
//
// Named with the streamingJSON prefix to avoid colliding with any
// existing writeJSON helper in the panel package.
func writeStreamingJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if body == nil {
		_, _ = w.Write([]byte("{}"))
		return
	}
	if _, ok := body.(struct{}); ok {
		_, _ = w.Write([]byte("{}"))
		return
	}
	_ = json.NewEncoder(w).Encode(body)
}
