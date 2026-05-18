//go:build windows

package agentinfo

import (
	"log/slog"

	"golang.org/x/sys/windows/registry"
)

// readMachineID returns HKLM\SOFTWARE\Microsoft\Cryptography\MachineGuid,
// the stable per-Windows-install identifier. Returns "" on any error
// (registry locked, key missing, permission denied) — Snapshot() then
// omits the field from the JSON response rather than failing the request.
func readMachineID() string {
	k, err := registry.OpenKey(
		registry.LOCAL_MACHINE,
		`SOFTWARE\Microsoft\Cryptography`,
		registry.QUERY_VALUE|registry.WOW64_64KEY,
	)
	if err != nil {
		slog.Warn("agentinfo: open Cryptography key", "err", err)
		return ""
	}
	defer k.Close()

	v, _, err := k.GetStringValue("MachineGuid")
	if err != nil {
		slog.Warn("agentinfo: read MachineGuid", "err", err)
		return ""
	}
	return v
}
