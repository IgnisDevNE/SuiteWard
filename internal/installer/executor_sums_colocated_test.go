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
	asBuild(t, "") // a development build
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

// A release build is never called a development build: the install says what doctor's row says, in the same words.
func TestInstallOfAReleaseBuildWithoutTheSumsDoesNotCallItADevelopmentBuild(t *testing.T) {
	asBuild(t, testDigest)
	h := newHarness(t, "linux")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	out := h.out.String()
	// The command in between names this instance's own settings and token file, as every command the install prints does.
	for _, want := range []string{
		"The test runner was not checked against the list of fingerprints of its release (SHA256SUMS), so SuiteWard cannot say that it is the release's own program. To check it, download SHA256SUMS from the same release and run `suiteward install ",
		" --upgrade --sha256sums <the release's SHA256SUMS file>`.\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "development build") {
		t.Errorf("a release build is called a development build:\n%s", out)
	}
}
