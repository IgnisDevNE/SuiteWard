package installer

import (
	"strings"
	"testing"
)

// Every command prints its screen or its plain lines and takes --plain, so a line of the usage that lists its flags one by one names [--plain],
// and the others say [flags]. `suiteward version` prints two lines and draws nothing.
func TestEveryCommandLineOfTheUsageTakesPlain(t *testing.T) {
	for _, line := range strings.Split(strings.TrimSpace(Usage), "\n") {
		if strings.TrimSpace(line) == "suiteward version" {
			continue
		}
		if !strings.Contains(line, "[--plain]") && !strings.Contains(line, "[flags]") {
			t.Errorf("the usage line %q names neither [--plain] nor [flags]", strings.TrimSpace(line))
		}
	}
}

func TestTheUsageNamesVersion(t *testing.T) {
	if !strings.Contains(Usage, "suiteward version\n") {
		t.Errorf("the usage lacks the version command:\n%s", Usage)
	}
}
