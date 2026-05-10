package version

import "testing"

func TestBase(t *testing.T) {
	orig := Version
	t.Cleanup(func() { Version = orig })

	cases := map[string]string{
		"0.5.2+v0.5.2":              "0.5.2",
		"0.5.2+v0.5.2-3-gabc123":    "0.5.2",
		"0.5.2+v0.5.2-3-gabc-dirty": "0.5.2",
		"0.5.2":                     "0.5.2",
		"dev":                       "dev",
		"ci-pr-42":                  "ci-pr-42",
		"":                          "",
	}
	for in, want := range cases {
		Version = in
		if got := Base(); got != want {
			t.Errorf("Base() with Version=%q = %q, want %q", in, got, want)
		}
	}
}
