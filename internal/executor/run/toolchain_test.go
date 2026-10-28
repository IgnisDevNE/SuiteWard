package run

import "testing"

func TestNeedsNewerGoComparesTheGoAndToolchainLines(t *testing.T) {
	const pinned = "go1.27.3"
	for name, tc := range map[string]struct {
		goMod string
		want  bool
	}{
		"no go line (go 1.16 is read)":        {"module m\n", false},
		"an older go line":                    {"module m\n\ngo 1.21\n", false},
		"the language version of the pinned":  {"module m\n\ngo 1.27\n", false},
		"the pinned patch release":            {"module m\n\ngo 1.27.3\n", false},
		"a newer patch release":               {"module m\n\ngo 1.27.4\n", true},
		"a newer language version":            {"module m\n\ngo 1.28\n", true},
		"a release candidate of a newer one":  {"module m\n\ngo 1.28rc1\n", true},
		"a newer toolchain line":              {"module m\n\ngo 1.21\ntoolchain go1.27.4\n", true},
		"an older toolchain line":             {"module m\n\ngo 1.21\ntoolchain go1.25.5\n", false},
		"the toolchain line that names local": {"module m\n\ngo 1.21\ntoolchain local\n", false},
		"a directive that this parser lacks":  {"module m\n\ngo 1.99\n\nfuture-directive x\n", true},
		"a go.mod nobody can read":            {"module (\n", false},
		"nothing":                             {"", false},
	} {
		if got := needsNewerGo([]byte(tc.goMod), pinned); got != tc.want {
			t.Errorf("%s: needsNewerGo = %v, want %v", name, got, tc.want)
		}
	}
}
