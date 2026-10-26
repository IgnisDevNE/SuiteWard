package installer

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

// The installer runs on a Mac and on Linux as well: it says "this computer", never "this PC", except on the Windows-only page of the PATH.
// The rest of the wording is pinned where each page is tested (the tier and ports pages, the Docker warning, the Podman note).

func TestTheTierIsDescribedAsThisComputerOnEveryPlatform(t *testing.T) {
	for _, c := range tierChoices {
		if strings.Contains(c.Desc, "PC") {
			t.Errorf("the choice %q is described as %q: say this computer", c.Name, c.Desc)
		}
	}
	for _, args := range [][]string{{"install", "--help"}, {"enroll", "--help"}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			h := newHarness(t, "darwin")
			if code := Main(context.Background(), args, h.newEnv(), &bytes.Buffer{}); code != 0 {
				t.Fatalf("exit %d", code)
			}
			if !strings.Contains(h.out.String(), "co-located (this computer) or isolated") {
				t.Errorf("the help of --tier is:\n%s", h.out.String())
			}
		})
	}
}

func TestTheRefusalToTakeOverTheDefaultSettingsSaysThisComputer(t *testing.T) {
	target := installTarget{settingsPath: "/home/u/settings.yaml", defaultPath: true, haveSaved: true, saved: Settings{Runtime: RuntimeDocker}}
	err := guardDefaultSettings(target, RuntimePodman, "")
	if err == nil || !strings.Contains(err.Error(), "of this computer") || strings.Contains(err.Error(), "PC") {
		t.Errorf("guardDefaultSettings = %v, want it to say this computer", err)
	}
}
