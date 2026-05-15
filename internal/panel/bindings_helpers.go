package panel

import "github.com/bioexperiment-lab-devices/serialhop/internal/api"

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
