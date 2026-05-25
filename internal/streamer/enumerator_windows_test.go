//go:build windows

package streamer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseListDevices_TwoCameras(t *testing.T) {
	data := readFixture(t, "ffmpeg_list_devices_two.txt")
	got, err := parseListDevices(data)
	if err != nil {
		t.Fatalf("parseListDevices: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("want 2 video devices, got %d (%+v)", len(got), got)
	}
	if got[0].Label != "Logitech HD Pro Webcam C920" {
		t.Errorf("got[0].Label = %q", got[0].Label)
	}
	// IDs are now slugified — URL-safe `cam-<sha1_prefix>` instead of
	// the raw DirectShow alternative name. The raw alt-name is fed
	// into SlugifyDeviceID, so the slug is deterministic.
	if !CameraIDPattern.MatchString(got[0].ID) {
		t.Errorf("got[0].ID = %q is not URL-safe (must match %s)", got[0].ID, CameraIDPattern)
	}
	if got[1].Label != "Microsoft Camera Front" {
		t.Errorf("got[1].Label = %q", got[1].Label)
	}
	if !CameraIDPattern.MatchString(got[1].ID) {
		t.Errorf("got[1].ID = %q is not URL-safe", got[1].ID)
	}
	if got[0].ID == got[1].ID {
		t.Errorf("two distinct cameras should have distinct slugs, both got %q", got[0].ID)
	}
}

func TestParseListDevices_OneCamera(t *testing.T) {
	data := readFixture(t, "ffmpeg_list_devices_one.txt")
	got, err := parseListDevices(data)
	if err != nil {
		t.Fatalf("parseListDevices: %v", err)
	}
	if len(got) != 1 || got[0].Label != "Logitech HD Pro Webcam C920" {
		t.Fatalf("want 1 camera C920, got %+v", got)
	}
}

func TestParseListDevices_Empty(t *testing.T) {
	data := readFixture(t, "ffmpeg_list_devices_empty.txt")
	got, err := parseListDevices(data)
	if err != nil {
		t.Fatalf("parseListDevices: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("want 0 cameras, got %d", len(got))
	}
}

func TestParseListDevices_SkipsAudio(t *testing.T) {
	data := readFixture(t, "ffmpeg_list_devices_one.txt")
	got, _ := parseListDevices(data)
	for _, c := range got {
		if strings.Contains(c.Label, "Microphone") {
			t.Fatalf("audio device leaked into video list: %+v", c)
		}
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	p := filepath.Join("testdata", name)
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("read fixture %s: %v", p, err)
	}
	return b
}
