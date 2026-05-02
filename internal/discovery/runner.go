package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"sort"
	"sync"

	"github.com/bioexperiment-lab-devices/serialhop/internal/registry"
	"github.com/bioexperiment-lab-devices/serialhop/internal/serial"
)

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
}

// Run probes every port in candidates concurrently (no cap), classifies the
// replies, and returns a slice of *registry.Device with sequential per-type
// IDs ("pump_1", "pump_2", ...). Ports that do not match a known device are
// closed; ports that match keep their connections open inside the returned
// devices.
func Run(ctx context.Context, opener serial.Opener, candidates []string) ([]*registry.Device, error) {
	if len(candidates) == 0 {
		return nil, nil
	}
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
			res, err := Probe(conn)
			if err != nil {
				slog.Debug("discovery: probe error", "port", portName, "err", err)
				_ = conn.Close()
				return
			}
			if res == nil {
				slog.Debug("discovery: no device on port", "port", portName)
				_ = conn.Close()
				return
			}
			mu.Lock()
			matches = append(matches, probeOutcome{port: portName, conn: conn, result: res})
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
	devs := make([]*registry.Device, 0, len(matches))
	for _, m := range matches {
		counts[m.result.TypeCode]++
		id := fmt.Sprintf("%s_%d", m.result.Type, counts[m.result.TypeCode])
		devs = append(devs, &registry.Device{
			ID:       id,
			Type:     m.result.Type,
			TypeCode: m.result.TypeCode,
			Port:     m.port,
			Conn:     m.conn,
			Opener:   opener,
		})
	}
	slog.Info("discovery: completed", "candidates", len(candidates), "matched", len(devs))
	return devs, nil
}
