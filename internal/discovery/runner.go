package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

// PostOpenSettle is the delay between opening a serial port and probing it.
// Arduino-class boards (Uno, Nano, Mini and similar with a USB-to-serial
// bridge) tie the host DTR line to the MCU reset pin through a capacitor;
// the host driver asserts DTR at port open — pulsing reset and dropping
// the board into its bootloader for ~1-2 s. Probes sent during that window
// are consumed by the bootloader and never reach user code, so the device
// appears silent even when it's running. The bug.st library's own docs
// note that even setting DTR=false in InitialStatusBits can't suppress the
// pulse on macOS/Linux, so we wait it out.
//
// Exposed as var so tests can set it to 0.
var PostOpenSettle = 2 * time.Second

// FilterPorts applies include / exclude filters per spec section 5.
// include and exclude are mutually exclusive (validated upstream); this
// function tolerates both being non-empty by giving precedence to include.
func FilterPorts(enumerated, include, exclude []string) []string {
	if len(include) > 0 {
		set := map[string]bool{}
		for _, p := range include {
			set[p] = true
		}
		out := []string{}
		for _, p := range enumerated {
			if set[p] {
				out = append(out, p)
			}
		}
		return out
	}
	if len(exclude) > 0 {
		set := map[string]bool{}
		for _, p := range exclude {
			set[p] = true
		}
		out := []string{}
		for _, p := range enumerated {
			if !set[p] {
				out = append(out, p)
			}
		}
		return out
	}
	out := make([]string, len(enumerated))
	copy(out, enumerated)
	return out
}

type probeOutcome struct {
	port   string
	conn   serial.Port
	result *ProbeResult
	reply  []byte
}

// Match is one successfully classified port: a device type was recognized
// from its probe reply, and the reply bytes are preserved for callers that
// need them (e.g. device.SessionConfig.ProbeReply).
type Match struct {
	ID       string // ordinal per (type code, port): "pump_1"
	Type     string // classification name: "pump" | "valve" | "densitometer"
	TypeCode byte
	Port     string
	Conn     serial.Port
	Reply    []byte // the identify reply the probe consumed (≥4 bytes)
}

// bytesToInts widens a byte slice for slog: the default JSON handler base64-
// encodes []byte, which makes probe traces unreadable. Returning []int gets
// the values rendered as a number array — matching the convention used by
// command response logging.
func bytesToInts(b []byte) []int {
	out := make([]int, len(b))
	for i, v := range b {
		out[i] = int(v)
	}
	return out
}

// Run probes every port in candidates concurrently (no cap),
// classifies the replies, and returns a slice of Match with sequential
// per-type IDs ("pump_1", "pump_2", ...) and the captured probe reply bytes.
// Ports that do not match a known device are closed; ports that match keep
// their connections open inside the returned matches.
func Run(ctx context.Context, opener serial.Opener, candidates []string) ([]Match, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
	sent := bytesToInts(ProbeBytes())
	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		matches []probeOutcome
	)
	for _, name := range candidates {
		wg.Add(1)
		go func(portName string) {
			defer wg.Done()
			conn, err := opener.Open(portName)
			if err != nil {
				slog.Debug("discovery: open failed", "port", portName, "err", err)
				return
			}
			if PostOpenSettle > 0 {
				time.Sleep(PostOpenSettle)
			}
			reply, res, err := Probe(conn)
			if err != nil {
				slog.Debug("discovery: probe error",
					"port", portName,
					"sent", sent,
					"reply", bytesToInts(reply),
					"err", err)
				_ = conn.Close()
				return
			}
			if res == nil {
				switch {
				case len(reply) == 0:
					slog.Debug("discovery: no device on port",
						"port", portName,
						"sent", sent,
						"reply", bytesToInts(reply))
				case len(reply) < 4:
					// A partial frame that survived Probe's retry: a device
					// is on this port but its reply keeps breaking up.
					slog.Warn("discovery: partial probe reply (device present, frame incomplete)",
						"port", portName,
						"sent", sent,
						"reply", bytesToInts(reply))
				default:
					slog.Warn("discovery: unknown device type",
						"port", portName,
						"sent", sent,
						"reply", bytesToInts(reply))
				}
				_ = conn.Close()
				return
			}
			slog.Debug("discovery: matched device",
				"port", portName,
				"sent", sent,
				"reply", bytesToInts(reply),
				"type", res.Type,
				"type_code", int(res.TypeCode))
			mu.Lock()
			matches = append(matches, probeOutcome{port: portName, conn: conn, result: res, reply: reply})
			mu.Unlock()
		}(name)
	}
	wg.Wait()

	// Sort by (TypeCode, Port) for deterministic IDs.
	sort.Slice(matches, func(i, j int) bool {
		if matches[i].result.TypeCode != matches[j].result.TypeCode {
			return matches[i].result.TypeCode < matches[j].result.TypeCode
		}
		return matches[i].port < matches[j].port
	})

	counts := map[byte]int{}
	out := make([]Match, 0, len(matches))
	for _, m := range matches {
		counts[m.result.TypeCode]++
		id := fmt.Sprintf("%s_%d", m.result.Type, counts[m.result.TypeCode])
		out = append(out, Match{
			ID:       id,
			Type:     m.result.Type,
			TypeCode: m.result.TypeCode,
			Port:     m.port,
			Conn:     m.conn,
			Reply:    m.reply,
		})
	}
	slog.Info("discovery: completed", "candidates", len(candidates), "matched", len(out))
	return out, nil
}
