//go:build quadlet

package installer

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// TestRenderUnitsForTheQuadletDryRun writes the units of both installer shapes where the CI quadlet job hands them to the
// quadlet generator of AlmaLinux 9's Podman (the real isolated host); the job fails when that Podman refuses a unit.
// It only renders: set SUITEWARD_QUADLET_OUT to an existing directory to get <dir>/github and <dir>/no-github.
func TestRenderUnitsForTheQuadletDryRun(t *testing.T) {
	out := os.Getenv("SUITEWARD_QUADLET_OUT")
	if out == "" {
		t.Skip("SUITEWARD_QUADLET_OUT is not set")
	}
	// The directory comes from the environment: os.Root keeps every write beneath it.
	root, err := os.OpenRoot(out)
	if err != nil {
		t.Fatalf("open SUITEWARD_QUADLET_OUT: %v", err)
	}
	defer func() { _ = root.Close() }()
	shapes := map[string]UnitParams{
		"github": isolatedParams(),
		"no-github": {
			Digest: testDigest, Tier: config.TierIsolated, HTTPPort: 18082, MCPPort: 18092, JobTimeout: "30m", NoGitHub: true,
		},
		// A host whose administrator has not run the host step of this release: the service has no executor token to mount.
		"no-executor": {Digest: testDigest, Tier: config.TierIsolated, HTTPPort: DefaultHTTPPort, MCPPort: DefaultMCPPort, NoExecutor: true},
	}
	for name, params := range shapes {
		units, err := RenderUnits(params)
		if err != nil {
			t.Fatalf("render the %s units: %v", name, err)
		}
		if err := root.MkdirAll(name, 0o750); err != nil {
			t.Fatalf("create the %s directory: %v", name, err)
		}
		for file, content := range units {
			if err := root.WriteFile(filepath.Join(name, file), content, 0o600); err != nil {
				t.Fatalf("write %s/%s: %v", name, file, err)
			}
		}
		entries, err := fs.ReadDir(root.FS(), name)
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
