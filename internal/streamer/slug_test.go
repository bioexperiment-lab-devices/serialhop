package streamer

import "testing"

func TestSlugifyDeviceID_IsURLSafe(t *testing.T) {
	// Inputs taken from a real production paste — DirectShow alternative
	// names contain `@`, `\`, `?`, `#`, `&`, `{`, `}` etc., none of
	// which round-trip through net/http.ServeMux's `{id}` pattern.
	inputs := []string{
		`@device_pnp_\?\usb#vid_1bcf&pid_2b95&mi_00#6&8430121&0&0000#{65e8773d-8f56-11d0-a3b9-00a0c9223196}\global`,
		`@device_pnp_\?\usb#vid_046d&pid_08e5&mi_00#a&1dd9db6d&0&0000#{65e8773d-8f56-11d0-a3b9-00a0c9223196}\global`,
		"",
		"already-safe-id",
	}
	for _, in := range inputs {
		got := SlugifyDeviceID(in)
		if !CameraIDPattern.MatchString(got) {
			t.Errorf("SlugifyDeviceID(%q) = %q does not satisfy CameraIDPattern", in, got)
		}
	}
}

func TestSlugifyDeviceID_IsStable(t *testing.T) {
	const raw = `@device_pnp_\?\usb#vid_1bcf&pid_2b95&mi_00#6&8430121&0&0000#{65e8773d-8f56-11d0-a3b9-00a0c9223196}\global`
	first := SlugifyDeviceID(raw)
	for i := 0; i < 3; i++ {
		if got := SlugifyDeviceID(raw); got != first {
			t.Fatalf("slugify must be deterministic; iteration %d got %q, want %q", i, got, first)
		}
	}
}

func TestSlugifyDeviceID_DistinctInputsProduceDistinctSlugs(t *testing.T) {
	a := SlugifyDeviceID(`@device_pnp_\?\usb#vid_1bcf&pid_2b95&...`)
	b := SlugifyDeviceID(`@device_pnp_\?\usb#vid_046d&pid_08e5&...`)
	if a == b {
		t.Fatalf("distinct device paths collided: both → %q", a)
	}
}

func TestCameraIDPattern_RejectsRawDirectShowAltName(t *testing.T) {
	// Catches future regressions where someone tries to store the raw
	// alt-name directly. If this test ever passes the alt-name through
	// CameraIDPattern, the slugifier was bypassed.
	raw := `@device_pnp_\?\usb#vid_1bcf&pid_2b95`
	if CameraIDPattern.MatchString(raw) {
		t.Fatalf("CameraIDPattern accidentally matches raw DirectShow alt-name: %q", raw)
	}
}
