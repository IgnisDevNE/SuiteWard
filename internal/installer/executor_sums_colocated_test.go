package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// On the co-located tier --sha256sums is optional. Given, the program must be listed in the release's SHA256SUMS, as on the host step, and the
// release's line is the pin; absent, the install goes on and says once that nothing checked the executor's binary against a release.

const sumsAsset = "suiteward_v0.4.0_linux_amd64"

// writeSums writes a SHA256SUMS that lists the program of the harness under asset and two other files, and returns its path.
func writeSums(t *testing.T, h *harness, asset string) string {
	t.Helper()
	line := sha256File(t, h.program()) + "  " + asset
	text := strings.Join([]string{strings.Repeat("a", 64) + "  suiteward_v0.4.0_windows_amd64.exe", line, strings.Repeat("b", 64) + "  install.sh"}, "\n") + "\n"
	path := filepath.Join(t.TempDir(), "SHA256SUMS")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInstallPinsTheExecutorToTheReleasesLineOfTheSums(t *testing.T) {
	h := newHarness(t, "linux")
	sums := writeSums(t, h, sumsAsset)
	if err := h.podman("--sha256sums", sums); err != nil {
		t.Fatal(err)
	}
	if got, want := h.readHome("executor/executor.pin"), sha256File(t, h.program())+"  "+sumsAsset+"\n"; got != want {
		t.Errorf("the pin is %q, want the release's line %q", got, want)
	}
	if strings.Contains(h.out.String(), "not checked against") {
		t.Errorf("the binary was checked, and the output says it was not:\n%s", h.out.String())
	}
}

func TestInstallRefusesSumsThatDoNotListTheProgramBeforeChangingAnything(t *testing.T) {
	h := newHarness(t, "linux")
	sums := writeSums(t, h, "suiteward_v0.4.0_windows_amd64.exe") // the program is listed as a binary of another system
	err := h.podman("--sha256sums", sums)
	if err == nil || !strings.Contains(err.Error(), "SHA256SUMS") {
		t.Fatalf("want a refusal that names the SHA256SUMS, got %v", err)
	}
	if len(h.f.find("run --detach")) != 0 {
		t.Errorf("a container was started before the refusal: %v", h.f.calls)
	}
	if _, statErr := os.Stat(h.executorDir()); !os.IsNotExist(statErr) {
		t.Errorf("the executor's directory was made before the refusal: %v", statErr)
	}
}

func TestInstallWithoutTheSumsSaysOnceThatTheBinaryWasNotChecked(t *testing.T) {
	h := newHarness(t, "linux")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(h.out.String(), "has not been checked against"); got != 1 {
		t.Errorf("the output says it %d times, want once:\n%s", got, h.out.String())
	}
	if !strings.Contains(h.out.String(), "SHA256SUMS") {
		t.Errorf("the output does not name the SHA256SUMS:\n%s", h.out.String())
	}
}

func TestUpgradeAtTheInstalledReleasePinsTheExecutorToTheSums(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	u.have(kindSecret, SecretExecutorAuth)
	u.writeExecutorFiles() // a pin without a release's line
	sums := writeSums(t, u.harness, sumsAsset)
	if err := u.upgradeAt(false, "", "--yes", "--image", oldImage, "--sha256sums", sums); err != nil {
		t.Fatal(err)
	}
	if got, want := u.readHome("executor/executor.pin"), sha256File(t, u.program())+"  "+sumsAsset+"\n"; got != want {
		t.Errorf("the pin is %q, want the release's line %q", got, want)
	}
}
