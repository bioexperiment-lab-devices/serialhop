package updater

import (
	"log/slog"
	"testing"

	"github.com/bioexperiment-lab-devices/serialhop/internal/slogtest"
)

func TestIsNewer(t *testing.T) {
	cases := []struct {
		name   string
		remote string
		local  string
		want   bool
		wantOK bool // false → expect error
	}{
		{"strict greater patch", "v0.7.0", "0.6.1", true, true},
		{"older patch", "v0.6.0", "0.6.1", false, true},
		{"equal", "v0.7.0", "0.7.0", false, true},
		{"strict greater minor", "v0.7.0", "0.6.9", true, true},
		{"strict greater major", "v1.0.0", "0.99.0", true, true},
		{"leading v on local too", "v0.7.0", "v0.6.1", true, true},
		{"no leading v on remote", "0.7.0", "0.6.1", true, true},
		{"local is dev build, remote is newer release", "v0.7.0", "0.6.1+v0.6.1-7-gabc1234-dirty", true, true},
		{"local is dev build matching base", "v0.6.1", "0.6.1+v0.6.1-7-gabc1234-dirty", false, true},
		{"local is dev build older than remote base", "v0.6.2", "0.6.1+v0.6.1-7-gabc1234-dirty", true, true},
		{"malformed remote", "garbage", "0.6.1", false, false},
		{"malformed local", "v0.7.0", "garbage", false, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := IsNewer(tc.remote, tc.local)
			if tc.wantOK && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tc.wantOK && err == nil {
				t.Fatal("expected error, got nil")
			}
			if got != tc.want {
				t.Errorf("got %v, want %v", got, tc.want)
			}
		})
	}
}

func TestCompare(t *testing.T) {
	cases := []struct {
		name string
		a, b string
		want int
		err  bool
	}{
		{"a less than b", "0.6.1", "0.7.0", -1, false},
		{"a greater than b", "0.7.0", "0.6.1", 1, false},
		{"equal", "0.7.0", "0.7.0", 0, false},
		{"equal with leading v on a", "v0.7.0", "0.7.0", 0, false},
		{"equal with leading v on b", "0.7.0", "v0.7.0", 0, false},
		{"a dev build vs b release, base equal", "0.6.1+v0.6.1-7-gabc1234-dirty", "0.6.1", 0, false},
		{"a dev build base less than b", "0.6.1+v0.6.1-7-gabc1234-dirty", "0.7.0", -1, false},
		{"major diff dominates minor", "1.0.0", "0.99.0", 1, false},
		{"minor diff dominates patch", "0.7.0", "0.6.999", 1, false},
		{"malformed a", "abc", "0.7.0", 0, true},
		{"malformed b", "0.7.0", "abc", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Compare(tc.a, tc.b)
			if tc.err {
				if err == nil {
					t.Fatalf("Compare(%q, %q) = %d, nil; want error", tc.a, tc.b, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("Compare(%q, %q) returned unexpected error: %v", tc.a, tc.b, err)
			}
			if got != tc.want {
				t.Errorf("Compare(%q, %q) = %d; want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}

func TestIsNewer_LogsWarnOnParseFailure(t *testing.T) {
	rec := slogtest.NewRecorder()
	prev := slog.Default()
	slog.SetDefault(slog.New(rec))
	t.Cleanup(func() { slog.SetDefault(prev) })

	_, _ = IsNewer("garbage", "0.6.1")

	rec.AssertRecord(t, slog.LevelWarn, "updater version parse failed", map[string]any{
		"input": "garbage",
	})
}
