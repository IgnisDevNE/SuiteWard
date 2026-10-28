package installer

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"strings"
	"testing"
)

// On the co-located tier the executor runs as the user, next to the coding agent, so the pin catches drift and accident and not a determined agent
// (the install's summary and the page that introduces this tier say that once, not each row). A pin that was not made from a release's SHA256SUMS is a warning, with the command that makes it right.

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
			// The owner's example of a text a person can read (decision 19): what happened, that it is normal, what to do.
			if want := "The test runner has not been checked against an official release. This is normal for a development build."; r.Detail != want {
				t.Errorf("the detail is %q, want %q", r.Detail, want)
			}
			if want := "To check it, run `suiteward install --upgrade --sha256sums <the release's SHA256SUMS file>` with the program from that release."; r.Fix != want {
				t.Errorf("the fix is %q, want %q", r.Fix, want)
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
			for _, want := range []string{testReleaseAsset(goos), "matches the official release"} {
				if !strings.Contains(r.Detail, want) {
					t.Errorf("the detail %q lacks %q", r.Detail, want)
				}
			}
		})
	}
}
