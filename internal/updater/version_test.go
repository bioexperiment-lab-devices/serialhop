package updater

import "testing"

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
