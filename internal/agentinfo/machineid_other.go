//go:build !windows

package agentinfo

// readMachineID returns the host's stable machine identifier. On non-Windows
// platforms there is no canonical source comparable to the Windows registry
// MachineGuid, so we return "" — Snapshot() then leaves Info.MachineID at
// its zero value and the field is omitted from JSON via `omitempty`.
//
// The production fleet runs on Windows; macOS/Linux are dev builds where
// the missing field is acceptable per the design spec.
func readMachineID() string {
	return ""
}
