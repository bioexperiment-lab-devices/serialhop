package agentinfo

import (
	"encoding/json"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	internalversion "github.com/bioexperiment-lab-devices/serialhop/internal/version"
)

func TestSnapshot_VersionFromInternalVersion(t *testing.T) {
	orig := internalversion.Version
	t.Cleanup(func() { internalversion.Version = orig })
	internalversion.Version = "1.2.3+deadbee"

	got := Snapshot()
	if got.Version != "1.2.3+deadbee" {
		t.Errorf("Version: got %q, want %q", got.Version, "1.2.3+deadbee")
	}
}

func TestSnapshot_OSAndArchMatchRuntime(t *testing.T) {
	got := Snapshot()
	if got.OS != runtime.GOOS {
		t.Errorf("OS: got %q, want %q", got.OS, runtime.GOOS)
	}
	if got.Arch != runtime.GOARCH {
		t.Errorf("Arch: got %q, want %q", got.Arch, runtime.GOARCH)
	}
}

func TestSnapshot_HostnameMatchesOS(t *testing.T) {
	want, err := os.Hostname()
	if err != nil {
		t.Skipf("os.Hostname unavailable: %v", err)
	}
	got := Snapshot()
	if got.Hostname != want {
		t.Errorf("Hostname: got %q, want %q", got.Hostname, want)
	}
}

func TestSnapshot_UptimeSecondsMonotonic(t *testing.T) {
	first := Snapshot().UptimeSeconds
	time.Sleep(1100 * time.Millisecond)
	second := Snapshot().UptimeSeconds
	if second < first {
		t.Errorf("uptime went backwards: first=%d second=%d", first, second)
	}
	if second-first < 1 {
		t.Errorf("uptime did not advance after 1.1s sleep: first=%d second=%d", first, second)
	}
}

func TestSnapshot_JSONShape(t *testing.T) {
	got := Snapshot()
	b, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	s := string(b)
	for _, key := range []string{`"version"`, `"os"`, `"arch"`, `"hostname"`, `"uptime_seconds"`} {
		if !strings.Contains(s, key) {
			t.Errorf("required key %s missing from %s", key, s)
		}
	}
}

func TestSnapshot_BuildSHAFromVersion(t *testing.T) {
	cases := []struct {
		name    string
		version string
		want    string
	}{
		{"release with describe suffix", "0.27.1+abc1234", "abc1234"},
		{"plain semver", "0.27.1", ""},
		{"dev default", "dev", ""},
		{"multi-plus stays at first", "0.27.1+abc+xyz", "abc+xyz"},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			orig := internalversion.Version
			t.Cleanup(func() { internalversion.Version = orig })
			internalversion.Version = tc.version
			if got := Snapshot().BuildSHA; got != tc.want {
				t.Errorf("BuildSHA(%q): got %q, want %q", tc.version, got, tc.want)
			}
		})
	}
}

func TestInfoJSON_OmitsBuildSHAWhenEmpty(t *testing.T) {
	b, err := json.Marshal(Info{Version: "dev", OS: "linux", Arch: "amd64"})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(b), `"build_sha"`) {
		t.Errorf("build_sha should be omitted when empty: %s", b)
	}
}
