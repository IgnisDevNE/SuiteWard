package installer

import (
	"os"
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

// The contract lists the flags of doctor ("The command line"); the usage names the same ones, in the same order, and adds only [--plain], which every command takes.
func TestTheUsageOfDoctorNamesTheFlagsOfTheContract(t *testing.T) {
	text, err := os.ReadFile("../../docs/contracts/runtime.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(Usage, "\n") {
		if line = strings.TrimSpace(line); strings.HasPrefix(line, "suiteward doctor") {
			if synopsis := strings.TrimSuffix(line, " [--plain]"); !strings.Contains(string(text), "`"+synopsis+"`") {
				t.Errorf("the contract does not list %q", synopsis)
			}
			return
		}
	}
	t.Fatal("the usage has no doctor line")
}
