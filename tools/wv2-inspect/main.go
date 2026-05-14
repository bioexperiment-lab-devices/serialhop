// wv2-inspect attaches to a WebView2 instance via its Chrome DevTools
// Protocol endpoint (--remote-debugging-port) and captures everything
// the WebView is doing — console messages, uncaught exceptions, browser
// log entries, and the final rendered DOM — into a JSON file.
//
// Usage:
//
//	wv2-inspect -port 9222 -duration 10s -out wv2.json
//
// The target Wails app must be launched with
// WEBVIEW2_ADDITIONAL_BROWSER_ARGUMENTS=--remote-debugging-port=9222 so
// WebView2 exposes its CDP endpoint. This tool exists to make CI
// failures of the panel diagnosable without remote-desktop access to
// the runner.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type cdpMessage struct {
	ID     int             `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type capture struct {
	Targets    []map[string]any `json:"targets"`
	ConsoleAPI []map[string]any `json:"console_api"`
	Exceptions []map[string]any `json:"exceptions"`
	LogEntries []map[string]any `json:"log_entries"`
	DOM        string           `json:"dom"`
	BodyInner  string           `json:"body_inner"`
	WailsKeys  map[string]any   `json:"wails_keys"`
}

func main() {
	port := flag.Int("port", 9222, "remote-debugging-port the WebView2 instance is listening on")
	duration := flag.Duration("duration", 10*time.Second, "how long to listen for console + exception events before dumping")
	out := flag.String("out", "wv2.json", "output file path")
	flag.Parse()

	cap := &capture{
		WailsKeys: make(map[string]any),
	}

	// 1. Get the targets list.
	resp, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/json", *port))
	if err != nil {
		writeFatal(*out, cap, fmt.Errorf("get /json: %w", err))
		return
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		writeFatal(*out, cap, fmt.Errorf("read /json: %w", err))
		return
	}
	if err := json.Unmarshal(body, &cap.Targets); err != nil {
		writeFatal(*out, cap, fmt.Errorf("parse /json: %w", err))
		return
	}

	// 2. Find a page target.
	var wsURL string
	for _, t := range cap.Targets {
		if t["type"] == "page" {
			if u, ok := t["webSocketDebuggerUrl"].(string); ok {
				wsURL = u
				break
			}
		}
	}
	if wsURL == "" {
		writeFatal(*out, cap, fmt.Errorf("no page target found in %d entries", len(cap.Targets)))
		return
	}

	// 3. Connect to the page target's WebSocket.
	ctx, cancel := context.WithTimeout(context.Background(), *duration+5*time.Second)
	defer cancel()
	conn, httpResp, err := websocket.DefaultDialer.DialContext(ctx, wsURL, nil)
	if httpResp != nil {
		_ = httpResp.Body.Close()
	}
	if err != nil {
		writeFatal(*out, cap, fmt.Errorf("ws dial: %w", err))
		return
	}
	defer conn.Close() //nolint:errcheck

	var (
		mu       sync.Mutex
		idCount  atomic.Int64
		pending  = make(map[int]chan cdpMessage)
		stopRead = make(chan struct{})
	)

	// 4. Reader goroutine: collect events, route results to pending.
	go func() {
		defer close(stopRead)
		for {
			var msg cdpMessage
			if err := conn.ReadJSON(&msg); err != nil {
				return
			}
			if msg.Method != "" {
				mu.Lock()
				var raw map[string]any
				_ = json.Unmarshal(msg.Params, &raw)
				switch msg.Method {
				case "Runtime.consoleAPICalled":
					cap.ConsoleAPI = append(cap.ConsoleAPI, raw)
				case "Runtime.exceptionThrown":
					cap.Exceptions = append(cap.Exceptions, raw)
				case "Log.entryAdded":
					cap.LogEntries = append(cap.LogEntries, raw)
				}
				mu.Unlock()
				continue
			}
			if msg.ID != 0 {
				mu.Lock()
				ch, ok := pending[msg.ID]
				if ok {
					delete(pending, msg.ID)
				}
				mu.Unlock()
				if ok {
					ch <- msg
					close(ch)
				}
			}
		}
	}()

	call := func(method string, params any) (json.RawMessage, error) {
		id := int(idCount.Add(1))
		ch := make(chan cdpMessage, 1)
		mu.Lock()
		pending[id] = ch
		mu.Unlock()
		payload := map[string]any{"id": id, "method": method}
		if params != nil {
			payload["params"] = params
		}
		if err := conn.WriteJSON(payload); err != nil {
			return nil, err
		}
		select {
		case msg := <-ch:
			if len(msg.Error) > 0 {
				return nil, fmt.Errorf("cdp error on %s: %s", method, msg.Error)
			}
			return msg.Result, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}

	// 5. Enable domains.
	if _, err := call("Runtime.enable", nil); err != nil {
		writeFatal(*out, cap, fmt.Errorf("cdp Runtime.enable: %w", err))
		return
	}
	if _, err := call("Log.enable", nil); err != nil {
		writeFatal(*out, cap, fmt.Errorf("cdp Log.enable: %w", err))
		return
	}
	if _, err := call("Page.enable", nil); err != nil {
		writeFatal(*out, cap, fmt.Errorf("cdp Page.enable: %w", err))
		return
	}

	// 6. Wait for events to accumulate.
	time.Sleep(*duration)

	// 7. Dump the DOM + a few interesting globals.
	dom, err := evalString(call, "document.documentElement.outerHTML")
	if err == nil {
		cap.DOM = dom
	}
	bodyInner, err := evalString(call, "document.body.innerHTML")
	if err == nil {
		cap.BodyInner = bodyInner
	}

	// 8. Check the keys we expect Wails to inject.
	for _, expr := range []string{
		"typeof window.go",
		"typeof window.runtime",
		"typeof window.go && window.go.main && typeof window.go.main.App",
		"typeof window.wailsbindings",
		"document.querySelectorAll('script').length",
		"document.querySelectorAll('script[src*=\"wails/runtime\"]').length",
		"document.querySelectorAll('script[src*=\"wails/ipc\"]').length",
		"document.getElementById('root') && document.getElementById('root').childElementCount",
	} {
		v, err := evalAny(call, expr)
		if err != nil {
			cap.WailsKeys[expr] = fmt.Sprintf("ERR: %v", err)
		} else {
			cap.WailsKeys[expr] = v
		}
	}

	writeJSON(*out, cap)
}

func evalString(call func(string, any) (json.RawMessage, error), expr string) (string, error) {
	raw, err := call("Runtime.evaluate", map[string]any{
		"expression":    expr,
		"returnByValue": true,
	})
	if err != nil {
		return "", err
	}
	var r struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return "", err
	}
	return r.Result.Value, nil
}

func evalAny(call func(string, any) (json.RawMessage, error), expr string) (any, error) {
	raw, err := call("Runtime.evaluate", map[string]any{
		"expression":    expr,
		"returnByValue": true,
	})
	if err != nil {
		return nil, err
	}
	var r struct {
		Result struct {
			Value any `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, err
	}
	return r.Result.Value, nil
}

func writeJSON(path string, cap *capture) {
	f, err := os.Create(path) //nolint:gosec // CI tool, path is CLI flag
	if err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
	defer f.Close() //nolint:errcheck
	enc := json.NewEncoder(f)
	enc.SetIndent("", "  ")
	_ = enc.Encode(cap)
}

func writeFatal(path string, cap *capture, err error) {
	cap.LogEntries = append(cap.LogEntries, map[string]any{
		"source": "wv2-inspect",
		"level":  "error",
		"text":   err.Error(),
	})
	writeJSON(path, cap)
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
