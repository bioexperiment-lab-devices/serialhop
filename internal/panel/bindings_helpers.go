package panel

import (
	"github.com/bioexperiment-lab-devices/serialhop/internal/api"
	"github.com/bioexperiment-lab-devices/serialhop/internal/discovery"
)

// normalizeDevicesResponse and normalizeDetailedPortsResponse exist
// solely to guarantee non-nil slices in the JSON the Wails bindings
// hand to the SPA.
//
// Why: ServiceCli.do returns early without populating its `out` value
// when the bootstrap cache is missing or the loopback HTTP call fails.
// `out` is the zero value of the response struct, which means a nil
// `Devices` / `Ports` slice. Go marshals a nil slice as JSON `null`,
// not `[]` — and the SPA's DevicesTab/PortsTab evaluate `.length` at
// the top of their render functions. `null.length` throws TypeError
// during render; with no ancestor ErrorBoundary, React unmounts the
// whole tree (including the frameless TitleBar) and the user is
// stranded with a blank, uncloseable window.
//
// Returning an empty (`len() == 0`, non-nil) slice is also the
// truthful answer in this state: "we did not observe any devices /
// ports," which is what the empty-state banner says anyway. The
// reachability outcome is conveyed by Status, not by the slice
// nil-ness.
func normalizeDevicesResponse(in api.DevicesResponse) api.DevicesResponse {
	if in.Devices == nil {
		in.Devices = []api.DeviceDTO{}
	}
	return in
}

func normalizeDetailedPortsResponse(in api.DetailedPortsResponse) api.DetailedPortsResponse {
	if in.Ports == nil {
		in.Ports = []api.DetailedPortDTO{}
	}
	return in
}

// filterDetailedPorts is the pure transform behind GetPorts's discovery-
// filter. include/exclude are the config.discovery slices (mutually
// exclusive; include wins when both are non-empty, matching
// discovery.FilterPorts). With both slices empty the response is
// returned unchanged — no allocation, no sort, no surprises.
func filterDetailedPorts(resp api.DetailedPortsResponse, include, exclude []string) api.DetailedPortsResponse {
	if len(include) == 0 && len(exclude) == 0 {
		return resp
	}
	names := make([]string, len(resp.Ports))
	for i, p := range resp.Ports {
		names[i] = p.Name
	}
	keep := discovery.FilterPorts(names, include, exclude)
	keepSet := make(map[string]struct{}, len(keep))
	for _, n := range keep {
		keepSet[n] = struct{}{}
	}
	out := make([]api.DetailedPortDTO, 0, len(keep))
	for _, p := range resp.Ports {
		if _, ok := keepSet[p.Name]; ok {
			out = append(out, p)
		}
	}
	resp.Ports = out
	return resp
}
