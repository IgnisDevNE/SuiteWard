package installer

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestDoctorLive runs doctor read-only against a co-located Podman stack that is already running. It is off unless
// SUITEWARD_LIVE_CONNECTION names the rootless Podman connection (for example podman-machine-default); the settings are built
// from the inspect output of the running containers and written to a temporary file.
func TestDoctorLive(t *testing.T) {
	connection := os.Getenv("SUITEWARD_LIVE_CONNECTION")
	if connection == "" {
		t.Skip("set SUITEWARD_LIVE_CONNECTION to run doctor against a running co-located stack")
	}
	ctx := context.Background()
	out, err := OSExec(ctx, "podman", []string{"--connection", connection, "inspect", "--type", "container", ContainerServe}, nil)
	if err != nil {
		t.Fatal(err)
	}
	serve, err := parseInspect(ContainerServe, out)
	if err != nil {
		t.Fatal(err)
	}
	s, diffs := knownContainerFields(serve)
	if len(diffs) > 0 {
		t.Fatalf("the service container's carried fields: %+v", diffs)
	}
	s.Connection = connection
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	code := Doctor(ctx, OSDeps(), path, testWriter{t}, false)
	t.Logf("exit code %d", code)
}

type testWriter struct{ t *testing.T }

func (w testWriter) Write(p []byte) (int, error) { w.t.Log("\n" + string(p)); return len(p), nil }
