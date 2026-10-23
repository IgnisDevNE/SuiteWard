//go:build quadlet

package installer

import (
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// TestRenderUnitsForTheQuadletDryRun writes the units of both installer shapes where the CI quadlet job hands them to the
// quadlet generator of AlmaLinux 9's Podman (the real isolated host); the job fails when that Podman refuses a unit.
// It only renders: set SUITEWARD_QUADLET_OUT to a directory to get <dir>/github and <dir>/no-github.
func TestRenderUnitsForTheQuadletDryRun(t *testing.T) {
	out := os.Getenv("SUITEWARD_QUADLET_OUT")
	if out == "" {
		t.Skip("SUITEWARD_QUADLET_OUT is not set")
	}
	shapes := map[string]UnitParams{
		"github": isolatedParams(),
		"no-github": {
			Digest: testDigest, Tier: config.TierIsolated, HTTPPort: 18082, MCPPort: 18092, JobTimeout: "30m", NoGitHub: true,
		},
	}
	for name, params := range shapes {
		units, err := RenderUnits(params)
		if err != nil {
			t.Fatalf("render the %s units: %v", name, err)
		}
		dir := filepath.Join(out, name)
		if _, err := WriteUnits(dir, units); err != nil {
			t.Fatalf("write the %s units: %v", name, err)
		}
		entries, err := os.ReadDir(dir)
		if err != nil {
			t.Fatal(err)
		}
		var written []string
		for _, e := range entries {
			written = append(written, e.Name())
		}
		if !slices.Equal(written, unitNames) {
			t.Errorf("%s: wrote %v, want %v", name, written, unitNames)
		}
	}
}
