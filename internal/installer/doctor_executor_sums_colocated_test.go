package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// On the co-located tier the executor runs as the user, next to the coding agent, so the pin catches drift and accident and not a determined agent
// (that sentence stays in both results). A pin that was not made from a release's SHA256SUMS is a warning, with the command that makes it right.

// testReleaseAsset is the name of the program in the release's SHA256SUMS, for the system of the fake.
func testReleaseAsset(goos string) string {
	name := "suiteward_v0.4.0_" + goos + "_amd64"
	if goos == "windows" {
		name += ".exe"
	}
	return name
}

func writePin(t *testing.T, f *fakeEngine, name string) {
	t.Helper()
	sum := sha256.Sum256([]byte(coloProgramText))
	if err := os.WriteFile(coloExecFilesOf(t, f).pin, []byte(hex.EncodeToString(sum[:])+"  "+name+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDoctorColocatedWarnsWhenThePinWasNotMadeFromTheReleasesSums(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			f, s := colocated{goos: goos}.build(t)
			writePin(t, f, "suiteward-executor")
			code, res, _ := runDoctor(t, f, s)
			if code != 0 {
				t.Errorf("a warning is not a failure: exit %d", code)
			}
			r := res["executor.binary"]
			if !r.OK || !r.Warning {
				t.Fatalf("executor.binary = %+v, want a passing warning", r)
			}
			for _, want := range []string{"SHA256SUMS", "reduced assurance"} {
				if !strings.Contains(r.Detail, want) {
					t.Errorf("the detail %q lacks %q", r.Detail, want)
				}
			}
			if !strings.Contains(r.Fix, "install --upgrade --sha256sums <the SHA256SUMS of the release>") {
				t.Errorf("the fix %q must name the upgrade with the release's sums", r.Fix)
			}
		})
	}
}

func TestDoctorColocatedPassesWhenThePinIsTheReleasesLine(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			f, s := colocated{goos: goos}.build(t)
			writePin(t, f, testReleaseAsset(goos))
			_, res, _ := runDoctor(t, f, s)
			r := res["executor.binary"]
			if !r.OK || r.Warning {
				t.Fatalf("executor.binary = %+v, want a plain pass", r)
			}
			for _, want := range []string{testReleaseAsset(goos), "reduced assurance"} {
				if !strings.Contains(r.Detail, want) {
					t.Errorf("the detail %q lacks %q", r.Detail, want)
				}
			}
		})
	}
}
