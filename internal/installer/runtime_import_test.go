package installer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// importPath is a tarball the engine is asked to import.
func importPath(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "volume.tar")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPodmanImportsAVolumeFromAFile(t *testing.T) {
	rt, f := linuxPodman(t)
	path := importPath(t, "TARDATA")
	if err := rt.ImportVolume(context.Background(), VolumeData, path); err != nil {
		t.Fatal(err)
	}
	calls := f.find("volume import")
	if len(calls) != 1 || calls[0].line() != "volume import "+VolumeData+" "+path {
		t.Fatalf("podman was asked %v, want `volume import %s %s`", f.lines(), VolumeData, path)
	}
	f.fail("volume import", errInjected)
	if err := rt.ImportVolume(context.Background(), VolumeData, path); !errors.Is(err, errInjected) || !strings.Contains(err.Error(), "import the volume suiteward-data") {
		t.Errorf("a failing import = %v", err)
	}
}

// Docker has no `volume import`: a throwaway container with the volume mounted writable and the tarball on its standard input, as
// confined as the one that exports the volume.
func TestDockerImportsAVolumeThroughAThrowawayContainer(t *testing.T) {
	rt, f := linuxDocker(t)
	f.on("inspect --type container suiteward-db", `[{"Name":"/suiteward-db","Config":{"Image":"`+oldPin+`"}}]`)
	path := importPath(t, "TARDATA")
	if err := rt.ImportVolume(context.Background(), VolumeData, path); err != nil {
		t.Fatal(err)
	}
	calls := f.find("tar -xf")
	if len(calls) != 1 {
		t.Fatalf("the volume was not written by a throwaway container: %v", f.lines())
	}
	for _, want := range []string{"run --rm", "--interactive", "--network none", "--read-only", "--cap-drop all", "--security-opt no-new-privileges", suiteward64, "--volume " + VolumeData + ":/data ", oldPin + " tar -xf - -C /data"} {
		if !strings.Contains(calls[0].line(), want) {
			t.Errorf("the container lacks %q: %s", want, calls[0].line())
		}
	}
	if strings.Contains(calls[0].line(), ":/data:ro") {
		t.Errorf("the volume is mounted read-only, so nothing can be imported into it: %s", calls[0].line())
	}
	if calls[0].stdin != "TARDATA" {
		t.Errorf("the tarball did not reach the standard input: %q", calls[0].stdin)
	}
}

func TestDockerImportErrors(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name  string
		rules func(f *fakeEngine)
		path  string
		want  string
	}{
		{"the database cannot be inspected", func(f *fakeEngine) { f.fail("inspect --type container suiteward-db", errInjected) }, "", "inspect suiteward-db"},
		{"the database reports no image", func(f *fakeEngine) { f.on("inspect --type container suiteward-db", `[{"Name":"/suiteward-db"}]`) }, "", "reports no image"},
		{"the file cannot be opened", func(f *fakeEngine) {
			f.on("inspect --type container suiteward-db", `[{"Name":"/suiteward-db","Config":{"Image":"`+oldPin+`"}}]`)
		}, "absent.tar", "absent.tar"},
		{"the import fails", func(f *fakeEngine) {
			f.fail("tar -xf", errInjected).on("inspect --type container suiteward-db", `[{"Name":"/suiteward-db","Config":{"Image":"`+oldPin+`"}}]`)
		}, "", "import the volume suiteward-data"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rt, f := linuxDocker(t)
			tc.rules(f)
			path := tc.path
			if path == "" {
				path = importPath(t, "TARDATA")
			} else {
				path = filepath.Join(t.TempDir(), path)
			}
			err := rt.ImportVolume(ctx, VolumeData, path)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("ImportVolume = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}
