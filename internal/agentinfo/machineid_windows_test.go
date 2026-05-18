//go:build windows

package agentinfo

import (
	"regexp"
	"testing"
)

// machineGuidPattern matches the standard Windows MachineGuid format
// (UUID, lowercase, hyphenated). We do not require an exact UUID v4 —
// older Windows versions emit a slightly different encoding.
var machineGuidPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

func TestReadMachineID_ReturnsRealGUID(t *testing.T) {
	got := readMachineID()
	if got == "" {
		t.Skip("MachineGuid registry read failed (locked-down CI?); skipping")
	}
	if !machineGuidPattern.MatchString(got) {
		t.Errorf("MachineGuid format unexpected: %q", got)
	}
}
