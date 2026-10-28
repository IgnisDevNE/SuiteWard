package installer

import (
	"strings"
	"testing"
)

// The co-located tier gives up some assurance against the isolated one, and that is a security disclosure: the plain run of the install ends
// with it. A plain `install --upgrade` has no summary, so it ends with the same line, once; an upgrade on a separate server never says it.
func TestAPlainColocatedUpgradeEndsWithTheReducedAssuranceLine(t *testing.T) {
	const line = reducedAssuranceNotice
	t.Run("co-located", func(t *testing.T) {
		u := newUpgradeHarness(t, nil)
		if err := u.upgrade("--image", testImage, "--yes"); err != nil {
			t.Fatal(err)
		}
		out := u.out.String()
		if got := strings.Count(out, line); got != 1 {
			t.Errorf("the plain upgrade says the line %d times, want once:\n%s", got, out)
		}
		if !strings.HasSuffix(strings.TrimSpace(out), line) {
			t.Errorf("the line is not the last thing the upgrade prints:\n%s", out)
		}
	})
	t.Run("separate server", func(t *testing.T) {
		u := newIsoUpgrade(t, nil)
		if err := u.upgrade("--image", testDigest, "--yes"); err != nil {
			t.Fatal(err)
		}
		if out := u.out.String(); strings.Contains(out, "run as your user") {
			t.Errorf("an upgrade on a separate server says the co-located line:\n%s", out)
		}
	})
}
