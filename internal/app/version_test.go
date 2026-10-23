package app

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/installer"
)

// setBuild sets the two values the release build stamps into the binary, for the length of the test.
func setBuild(t *testing.T, version, digest string) {
	t.Helper()
	wasVersion, wasDigest := Version, installer.ImageDigest
	Version, installer.ImageDigest = version, digest
	t.Cleanup(func() { Version, installer.ImageDigest = wasVersion, wasDigest })
}

// The version is read before any setting: the machine that asks has no SUITEWARD_* environment (Env{} has no lookup at all).
func TestVersionPrintsTheBuildAndTheReleaseImageDigest(t *testing.T) {
	digest := "sha256:" + strings.Repeat("a", 64)
	for _, arg := range []string{"version", "--version", "-version"} {
		setBuild(t, "v1.2.3", digest)
		code, stdout, stderr := run([]string{arg}, Env{})
		if want := "suiteward v1.2.3\nimage " + digest + "\n"; code != 0 || stdout != want || stderr != "" {
			t.Errorf("Run(%q) = %d, stdout %q, stderr %q; want 0, %q and nothing on stderr", arg, code, stdout, stderr, want)
		}
	}
}

func TestVersionOfADevelopmentBuildSaysItCarriesNoImage(t *testing.T) {
	for name, digest := range map[string]string{"no digest": "", "an unusable digest": "sha256:not-a-digest"} {
		setBuild(t, "dev", digest)
		code, stdout, stderr := run([]string{"version"}, Env{})
		if want := "suiteward dev\nimage none (development build)\n"; code != 0 || stdout != want || stderr != "" {
			t.Errorf("%s: Run(version) = %d, stdout %q, stderr %q; want 0, %q and nothing on stderr", name, code, stdout, stderr, want)
		}
	}
}

func TestVersionTakesNoArguments(t *testing.T) {
	for _, args := range [][]string{{"version", "extra"}, {"--version", "extra"}, {"version", "--json"}} {
		code, stdout, stderr := run(args, Env{})
		if code != 2 || stdout != "" || !strings.Contains(stderr, "usage") {
			t.Errorf("Run(%q) = %d, stdout %q, stderr %q; want 2, nothing on stdout and a usage message", args, code, stdout, stderr)
		}
	}
}

func TestUsageNamesVersion(t *testing.T) {
	_, _, stderr := run(nil, Env{})
	if !strings.Contains(stderr, "suiteward version") {
		t.Errorf("the usage lacks the version command:\n%s", stderr)
	}
}
