package labbridge

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

const serverInfoPath = "/api/public/server-info"

// ForwardTunnel describes one chisel -L forward the agent should open.
type ForwardTunnel struct {
	Name   string `json:"name"`
	Local  string `json:"local"`
	Remote string `json:"remote"`
}

// ServerInfo is the parsed result of GET /api/public/server-info.
// Unknown fields in the response are silently ignored to allow the
// server to add new keys (e.g. agent metadata, chisel fingerprint)
// without breaking older agents.
type ServerInfo struct {
	ChiselListenPort int             `json:"chisel_listen_port"`
	LokiPushURL      string          `json:"loki_push_url"`
	ForwardTunnels   []ForwardTunnel `json:"forward_tunnels"`
}

type serverInfoBody struct {
	Chisel struct {
		ListenPort int `json:"listen_port"`
	} `json:"chisel"`
	Loki struct {
		PushURL string `json:"push_url"`
	} `json:"loki"`
	ForwardTunnels []struct {
		Name   string `json:"name"`
		Local  string `json:"local"`
		Remote string `json:"remote"`
	} `json:"forward_tunnels"`
}

// FetchServerInfo retrieves the agent-bootstrap parameters from the
// lab-bridge VPS. No Authorization header is sent.
//
// Returns wrapped ErrServerError on HTTP 5xx; plain error for transport,
// parse, validation, or unexpected status. Unknown response fields are
// silently ignored (forward-compat).
func FetchServerInfo(ctx context.Context, hc *http.Client, base, userAgent string) (ServerInfo, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+serverInfoPath, nil)
	if err != nil {
		return ServerInfo{}, fmt.Errorf("labbridge: build server-info request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := hc.Do(req)
	if err != nil {
		return ServerInfo{}, fmt.Errorf("labbridge: do server-info: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode >= 500 {
		return ServerInfo{}, fmt.Errorf("labbridge: server-info: %w (status %d)", ErrServerError, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return ServerInfo{}, fmt.Errorf("labbridge: server-info: unexpected status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return ServerInfo{}, fmt.Errorf("labbridge: read server-info body: %w", err)
	}
	var b serverInfoBody
	if err := json.Unmarshal(body, &b); err != nil {
		return ServerInfo{}, fmt.Errorf("labbridge: parse server-info body: %w", err)
	}

	if b.Chisel.ListenPort < 1 || b.Chisel.ListenPort > 65535 {
		return ServerInfo{}, fmt.Errorf("labbridge: server-info: chisel.listen_port out of range (got %d)", b.Chisel.ListenPort)
	}
	if b.Loki.PushURL == "" {
		return ServerInfo{}, fmt.Errorf("labbridge: server-info: loki.push_url is empty")
	}

	tunnels := make([]ForwardTunnel, 0, len(b.ForwardTunnels))
	for i, t := range b.ForwardTunnels {
		if t.Local == "" || t.Remote == "" {
			return ServerInfo{}, fmt.Errorf("labbridge: server-info: forward_tunnels[%d] has empty local or remote", i)
		}
		tunnels = append(tunnels, ForwardTunnel{Name: t.Name, Local: t.Local, Remote: t.Remote})
	}

	return ServerInfo{
		ChiselListenPort: b.Chisel.ListenPort,
		LokiPushURL:      b.Loki.PushURL,
		ForwardTunnels:   tunnels,
	}, nil
}
