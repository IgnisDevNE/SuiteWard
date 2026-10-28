package installer

import (
	"errors"
	"os"
	"strings"
	"testing"
)

// --sha256sums that is present and blank is a mistake of the command line (a script that passes "$SUMS" with SUMS unset), never "no check". And the
// isolated install and upgrade, which have no use for it, refuse it instead of letting an administrator believe that it checked something.

func TestColocatedInstallRefusesABlankSHA256SUMS(t *testing.T) {
	h := newHarness(t, "linux")
	err := h.podman("--sha256sums", "")
	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "unset variable") || !strings.Contains(err.Error(), "--sha256sums") {
		t.Fatalf("want a usage error that names the flag and an unset variable, got %v", err)
	}
	if len(h.f.find("run --detach")) != 0 {
		t.Errorf("a container was started before the refusal: %v", h.f.calls)
	}
	if _, statErr := os.Stat(h.executorDir()); !os.IsNotExist(statErr) {
		t.Errorf("the executor was set up although the flag was blank: %v", statErr)
	}
}

func TestColocatedUpgradeRefusesABlankSHA256SUMS(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	err := u.upgradeAt(false, "", "--yes", "--image", oldImage, "--sha256sums", "")
	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "unset variable") {
		t.Fatalf("want a usage error that names an unset variable, got %v", err)
	}
	u.noChanges()
}

func TestPrepareHostRefusesABlankSHA256SUMSAsAnUnsetVariable(t *testing.T) {
	h := newHostFake()
	h.exe = writeProgram(t)
	_, err := runHostAsGiven(t, h, map[string]string{"metadata-rule": "no", "sha256sums": ""}, false, "")
	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "unset variable") {
		t.Fatalf("want a usage error that names an unset variable, got %v", err)
	}
}

func TestIsolatedInstallRefusesSHA256SUMSAndNamesTheHostStep(t *testing.T) {
	h := newHarness(t, "linux")
	err := h.install("--tier", "isolated", "--sha256sums", "/root/release/SHA256SUMS")
	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "install host --sha256sums") {
		t.Fatalf("want a usage error that names `sudo suiteward install host --sha256sums`, got %v", err)
	}
	if !strings.Contains(err.Error(), "`sudo /usr/local/bin/suiteward install host --sha256sums") {
		t.Errorf("the host step is named by the path that sudo finds: %v", err)
	}
}

func TestIsolatedUpgradeRefusesSHA256SUMSAndNamesTheHostStep(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	err := u.upgrade("--image", testDigest, "--yes", "--sha256sums", "/root/release/SHA256SUMS")
	if !errors.Is(err, ErrUsage) || !strings.Contains(err.Error(), "install host --sha256sums") {
		t.Fatalf("want a usage error that names `sudo suiteward install host --sha256sums`, got %v", err)
	}
	if !strings.Contains(err.Error(), "`sudo /usr/local/bin/suiteward install host --sha256sums") {
		t.Errorf("the host step is named by the path that sudo finds: %v", err)
	}
}
