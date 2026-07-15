package api

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"

	"github.com/bioexperiment-lab-devices/serialhop/internal/discovery"
	labserial "github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

var rawUpgrader = websocket.Upgrader{
	ReadBufferSize:  4096,
	WriteBufferSize: 4096,
	// The device API is reached over the chisel tunnel from inside labnet;
	// there is no browser Origin to validate.
	CheckOrigin: func(*http.Request) bool { return true },
}

const (
	rawReadChunk    = 4096
	rawPongWait     = 40 * time.Second
	rawPingPeriod   = 30 * time.Second
	rawSerialReadTO = 50 * time.Millisecond

	// rawMaxMessageBytes bounds per-message memory: gorilla's default read
	// limit is unlimited, so a single huge frame from a client could OOM the
	// agent (which runs as LocalSystem). 1 MiB is generous for a serial byte
	// stream.
	rawMaxMessageBytes = 1 << 20 // 1 MiB
)

// controlMsg is one text/JSON control frame in either direction.
type controlMsg struct {
	Op     string `json:"op"`
	Baud   int    `json:"baud,omitempty"`
	Level  *bool  `json:"level,omitempty"`
	Ms     int    `json:"ms,omitempty"`
	Port   string `json:"port,omitempty"`
	Detail string `json:"detail,omitempty"`
	CTS    bool   `json:"cts,omitempty"`
	DSR    bool   `json:"dsr,omitempty"`
	RI     bool   `json:"ri,omitempty"`
	CD     bool   `json:"cd,omitempty"`
}

// rawConn serializes all writes to a websocket.Conn (gorilla forbids
// concurrent writers).
type rawConn struct {
	ws  *websocket.Conn
	wmu sync.Mutex
}

func (c *rawConn) writeBinary(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.WriteMessage(websocket.BinaryMessage, b)
}

func (c *rawConn) writeJSON(v controlMsg) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.WriteJSON(v)
}

func (c *rawConn) ping() error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.ws.WriteControl(websocket.PingMessage, nil, time.Now().Add(time.Second))
}

func (c *rawConn) close(code int, text string) {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	_ = c.ws.WriteControl(websocket.CloseMessage,
		websocket.FormatCloseMessage(code, text), time.Now().Add(time.Second))
	_ = c.ws.Close()
}

func rawBaud(r *http.Request) (int, error) {
	v := r.URL.Query().Get("baud")
	if v == "" {
		return 9600, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 1 || n > 4_000_000 {
		return 0, fmt.Errorf("baud must be an integer in 1..4000000")
	}
	return n, nil
}

func (s *Server) handleSerialAttach(w http.ResponseWriter, r *http.Request) {
	if !s.rawSerialEnabled {
		writeError(w, http.StatusForbidden, "raw serial disabled", "set raw_serial.enabled: true in config")
		return
	}
	port := r.PathValue("port")
	baud, err := rawBaud(r)
	if err != nil {
		writeError(w, http.StatusBadRequest, "invalid query param", err.Error())
		return
	}
	names, err := s.opener.List()
	if err != nil {
		writeError(w, http.StatusInternalServerError, "list ports failed", err.Error())
		return
	}
	if !slices.Contains(names, port) {
		writeError(w, http.StatusNotFound, "port not found", port)
		return
	}
	if id, ok := s.reg.HasPort(port); ok {
		writeError(w, http.StatusConflict, "port has discovered device", "owned by "+id)
		return
	}
	if s.reg.IsDiscovering() {
		writeError(w, http.StatusConflict, "discovery in progress", "")
		return
	}
	if !s.reg.TryAcquireRaw(port) {
		writeError(w, http.StatusConflict, "port already attached", "")
		return
	}

	ws, err := rawUpgrader.Upgrade(w, r, nil)
	if err != nil {
		// Upgrade already wrote an HTTP error response.
		s.reg.ReleaseRaw(port)
		slog.Warn("raw_attach upgrade failed", "port", port, "err", err)
		return
	}
	ws.SetReadLimit(rawMaxMessageBytes)
	s.runRawSession(&rawConn{ws: ws}, port, baud)
}

func (s *Server) runRawSession(c *rawConn, port string, baud int) {
	start := time.Now()
	var txBytes, rxBytes int64
	reason := "client_close"
	defer func() {
		_ = c.ws.Close()
		s.reg.ReleaseRaw(port)
		slog.Info("raw_attach_close",
			"port", port,
			"bytes_tx", atomic.LoadInt64(&txBytes),
			"bytes_rx", atomic.LoadInt64(&rxBytes),
			"duration_ms", time.Since(start).Milliseconds(),
			"reason", reason)
	}()

	sp, err := s.opener.OpenWithBaud(port, baud)
	if err != nil {
		reason = "open_failed"
		c.close(websocket.CloseInternalServerErr, "open failed: "+err.Error())
		return
	}
	defer func() { _ = sp.Close() }()

	slog.Info("raw_attach_open", "port", port, "remote", c.ws.RemoteAddr().String(), "baud", baud)
	_ = c.writeJSON(controlMsg{Op: "ready", Port: port, Baud: baud})

	var lastActive atomic.Int64
	lastActive.Store(time.Now().UnixNano())
	touch := func() { lastActive.Store(time.Now().UnixNano()) }

	// serial -> ws
	serialDone := make(chan struct{})
	go func() {
		defer close(serialDone)
		buf := make([]byte, rawReadChunk)
		for {
			if err := sp.SetReadTimeout(rawSerialReadTO); err != nil {
				return
			}
			n, err := sp.Read(buf)
			if err != nil {
				return
			}
			if n == 0 {
				continue
			}
			touch()
			atomic.AddInt64(&rxBytes, int64(n))
			if err := c.writeBinary(buf[:n]); err != nil {
				return
			}
		}
	}()

	// When the serial side dies, force the ws->serial ReadMessage below to
	// return immediately. Without this, gorilla's default ping handler
	// auto-pongs and keeps resetting the read deadline, so an idle client
	// would hold the raw lease forever after the port is gone. Setting an
	// already-elapsed read deadline is deadlock-free — it does not take wmu.
	go func() {
		<-serialDone
		_ = c.ws.SetReadDeadline(time.Now())
	}()

	// ping keepalive
	pingDone := make(chan struct{})
	go func() {
		t := time.NewTicker(rawPingPeriod)
		defer t.Stop()
		for {
			select {
			case <-pingDone:
				return
			case <-t.C:
				if err := c.ping(); err != nil {
					return
				}
			}
		}
	}()
	defer close(pingDone)

	// idle-timeout watchdog
	if s.rawIdleTimeout > 0 {
		idleDone := make(chan struct{})
		defer close(idleDone)
		go func() {
			t := time.NewTicker(s.rawIdleTimeout / 2)
			defer t.Stop()
			for {
				select {
				case <-idleDone:
					return
				case <-t.C:
					last := time.Unix(0, lastActive.Load())
					if time.Since(last) >= s.rawIdleTimeout {
						c.close(websocket.CloseGoingAway, "idle timeout")
						return
					}
				}
			}
		}()
	}

	// ws -> serial (this goroutine)
	_ = c.ws.SetReadDeadline(time.Now().Add(rawPongWait))
	c.ws.SetPongHandler(func(string) error {
		return c.ws.SetReadDeadline(time.Now().Add(rawPongWait))
	})

	for {
		select {
		case <-serialDone:
			reason = "read_error"
			return
		default:
		}
		mt, data, err := c.ws.ReadMessage()
		if err != nil {
			// A closed serialDone means the read unblocked because the serial
			// side died (via the watcher above), not because the client left.
			select {
			case <-serialDone:
				reason = "read_error"
			default:
				if s.rawIdleTimeout > 0 && time.Since(time.Unix(0, lastActive.Load())) >= s.rawIdleTimeout {
					reason = "idle_timeout"
				}
			}
			return
		}
		touch()
		_ = c.ws.SetReadDeadline(time.Now().Add(rawPongWait))
		switch mt {
		case websocket.BinaryMessage:
			if _, err := sp.Write(data); err != nil {
				_ = c.writeJSON(controlMsg{Op: "error", Detail: "write: " + err.Error()})
				reason = "write_error"
				return
			}
			atomic.AddInt64(&txBytes, int64(len(data)))
		case websocket.TextMessage:
			s.handleRawControl(c, sp, data)
		}
	}
}

func (s *Server) handleRawControl(c *rawConn, sp labserial.Port, data []byte) {
	var m controlMsg
	if err := json.Unmarshal(data, &m); err != nil {
		_ = c.writeJSON(controlMsg{Op: "error", Detail: "bad control frame: " + err.Error()})
		return
	}
	var err error
	switch m.Op {
	case "set_baud":
		err = sp.SetBaudRate(m.Baud)
	case "set_dtr":
		err = sp.SetDTR(m.Level != nil && *m.Level)
	case "set_rts":
		err = sp.SetRTS(m.Level != nil && *m.Level)
	case "send_break":
		err = sp.SendBreak(time.Duration(m.Ms) * time.Millisecond)
	case "drain":
		err = sp.Drain(discovery.DrainDuration)
	case "get_modem":
		var bits labserial.ModemBits
		bits, err = sp.ModemStatus()
		if err == nil {
			_ = c.writeJSON(controlMsg{Op: "modem", CTS: bits.CTS, DSR: bits.DSR, RI: bits.RI, CD: bits.CD})
			return
		}
	default:
		_ = c.writeJSON(controlMsg{Op: "error", Detail: "unknown op: " + m.Op})
		return
	}
	if err != nil {
		_ = c.writeJSON(controlMsg{Op: "error", Detail: m.Op + ": " + err.Error()})
	}
}
