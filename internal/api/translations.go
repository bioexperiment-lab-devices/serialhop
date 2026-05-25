package api

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/bootstrap"
)

// defaultProxyTimeout bounds a single forwarded HTTP round-trip to the
// panel. The panel runs on the same host (localhost), so a generous
// 5-second timeout is plenty for normal operation while still failing
// quickly when the panel is gone.
const defaultProxyTimeout = 5 * time.Second

// maxProxyBodyBytes caps both inbound request bodies and panel response
// bodies. The streaming endpoints exchange tiny JSON envelopes; 64 KiB
// is generous slack without exposing the process to memory exhaustion.
const maxProxyBodyBytes = 64 << 10

// TranslationsProxy serves the three streaming endpoints by
// HTTP-forwarding them to the panel's localhost listener. The panel's
// listen port is looked up per-request from panel-endpoint.json so a
// panel restart is picked up without restarting the service.
type TranslationsProxy struct {
	endpointPath string
	hc           *http.Client
}

// NewTranslationsProxy constructs a proxy that resolves the panel's
// endpoint from the given JSON file path.
func NewTranslationsProxy(endpointPath string) *TranslationsProxy {
	return &TranslationsProxy{
		endpointPath: endpointPath,
		hc:           &http.Client{Timeout: defaultProxyTimeout},
	}
}

// Mount registers the proxy's three routes on the given mux. Mounting
// directly (rather than returning a sub-mux) avoids Go 1.22 ServeMux
// overlap headaches around `/api/translations` vs.
// `/api/translations/{id}/...`.
func (p *TranslationsProxy) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/translations", p.handleGet)
	mux.HandleFunc("POST /api/translations/{id}/start", p.handleStart)
	mux.HandleFunc("POST /api/translations/{id}/stop", p.handleStop)
}

// handleGet returns the panel's translation list. When the panel is
// down the proxy degrades to an empty list (200) so the operator UI
// renders cleanly instead of erroring out.
func (p *TranslationsProxy) handleGet(w http.ResponseWriter, r *http.Request) {
	body, status, err := p.forward(r.Context(), http.MethodGet, "/api/translations", nil)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"translations": []any{}})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

// handleStart forwards a session-start request to the panel verbatim.
// Panel-down maps to 503 — the operator needs to know the action
// didn't take effect.
func (p *TranslationsProxy) handleStart(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	body, err := io.ReadAll(io.LimitReader(r.Body, maxProxyBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
		return
	}
	respBody, status, perr := p.forward(r.Context(), http.MethodPost,
		"/api/translations/"+id+"/start", body)
	if perr != nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "panel not running"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(respBody) //nolint:gosec // respBody is the panel's own JSON response, forwarded verbatim
}

// handleStop forwards a session-stop request to the panel. Panel-down
// maps to 204 (idempotent): the goal state is "no stream running",
// which is already true when the panel itself isn't running.
func (p *TranslationsProxy) handleStop(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	body, err := io.ReadAll(io.LimitReader(r.Body, maxProxyBodyBytes))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad request body"})
		return
	}
	respBody, status, perr := p.forward(r.Context(), http.MethodPost,
		"/api/translations/"+id+"/stop", body)
	if perr != nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	if status == http.StatusNoContent {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(respBody) //nolint:gosec // respBody is the panel's own JSON response, forwarded verbatim
}

// forward resolves the panel endpoint from disk, builds a request, and
// returns (body, status, nil) on success. Any failure (endpoint
// missing, dial error, read error) collapses to a non-nil error so the
// caller can apply its endpoint-specific fallback.
func (p *TranslationsProxy) forward(ctx context.Context, method, path string, body []byte) ([]byte, int, error) {
	ep, err := bootstrap.ReadPanelEndpoint(p.endpointPath)
	if err != nil {
		return nil, 0, err
	}
	host := ep.Host
	if host == "" {
		host = "127.0.0.1"
	}
	url := "http://" + host + ":" + strconv.Itoa(ep.Port) + path
	var rdr io.Reader
	if len(body) > 0 {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, rdr) //nolint:gosec // url host:port comes from panel-endpoint.json written by our own panel
	if err != nil {
		return nil, 0, err
	}
	if len(body) > 0 {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := p.hc.Do(req) //nolint:gosec // see http.NewRequestWithContext above
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	out, err := io.ReadAll(io.LimitReader(resp.Body, maxProxyBodyBytes))
	if err != nil {
		return nil, 0, err
	}
	return out, resp.StatusCode, nil
}
