package installer

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
)

// The sandboxes run with --pull=never from the rootless storage of the executor's account, which no one else can read: the host step pulls the
// Go image there, as that account and through its user manager, and checks that the image is there under its digest.

func TestPrepareHostPullsTheGoImageIntoTheStorageOfTheExecutor(t *testing.T) {
	h := newHostFake()
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	pull := hostCallIndex(h, "systemd-run", "podman pull "+goprofile.Image)
	exists := hostCallIndex(h, "systemd-run", "podman image exists "+goprofile.Image)
	start := hostCallIndex(h, "systemctl", "enable --now suiteward-executor.service")
	if pull < 0 || exists < 0 || start < 0 || pull >= exists || exists >= start {
		t.Fatalf("want a pull, then the check of the image, then the start of the executor; got %d, %d, %d in %v", pull, exists, start, h.lines())
	}
	for _, c := range []call{h.calls[pull], h.calls[exists]} {
		for _, want := range []string{execUserSystemd, "--wait"} {
			if !strings.Contains(c.line(), want) {
				t.Errorf("%s lacks %s: the account's own manager runs it, and the step waits for it", c.line(), want)
			}
		}
	}
	if h.has("podman", "pull") {
		t.Error("root pulled the image into its own storage, where the executor cannot see it")
	}
}

func TestPrepareHostPullsTheGoImageAgainWhenItIsThereAlready(t *testing.T) {
	h := newHostFake()
	h.imagePulled = true // an image bump is a new digest, so the step does not skip the pull
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if n := len(h.callsTo("systemd-run")); n != 2 {
		t.Errorf("want a pull and a check, got %d systemd-run calls: %v", n, h.lines())
	}
}

func TestPrepareHostStopsBeforeTheExecutorStartsWhenTheGoImageCannotBePulled(t *testing.T) {
	h := newHostFake()
	h.pullFails = true
	_, err := runHost(t, h, declineMetadataRule, false, "")
	if err == nil || !strings.Contains(err.Error(), "pull") || !strings.Contains(err.Error(), ExecutorAccount) {
		t.Fatalf("want an error that names the pull and the account, got %v", err)
	}
	if h.has("systemctl", "enable --now suiteward-executor.service") {
		t.Error("the executor was started without the image of its sandboxes")
	}
}

func TestPrepareHostStopsWhenThePulledGoImageIsNotThereUnderItsDigest(t *testing.T) {
	h := newHostFake()
	h.pullLeavesNothing = true
	_, err := runHost(t, h, declineMetadataRule, false, "")
	if err == nil || !strings.Contains(err.Error(), "digest") {
		t.Fatalf("want an error about the digest of the image, got %v", err)
	}
	if h.has("systemctl", "enable --now suiteward-executor.service") {
		t.Error("the executor was started without the image of its sandboxes")
	}
}

// A pull that fails stops the step, and what the step changed before it is not seen again by the next run: the binary then matches and the unit is
// unchanged, so a running executor would never be restarted. The pull comes first, and a run that stops there has changed nothing.
func TestPrepareHostThatCannotPullStillRestartsTheExecutorOnTheNextRun(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(h *hostFake)
	}{
		{"a replaced binary", func(h *hostFake) { h.hashes[execBinaryPath] = strings.Repeat("0", 64) }},
		{"a new token", func(h *hostFake) { h.hashes[execBinaryPath] = sha256File(t, h.exe) }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := newHostFake()
			h.exe = writeProgram(t)
			unit, err := RenderExecutorUnit(DefaultHTTPPort)
			if err != nil {
				t.Fatal(err)
			}
			h.files[execUnitFile] = string(unit) // the unit is the release's already
			h.execRunning = true
			tc.setup(h)
			if tc.name == "a replaced binary" {
				h.paths[execAuthEtc] = true // the token is there, so only the binary changes
			}
			h.pullFails = true
			if _, err := runHost(t, h, declineMetadataRule, false, ""); err == nil {
				t.Fatal("the pull failed and the step went on")
			}
			h.pullFails = false
			if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
				t.Fatal(err)
			}
			if !h.has("systemctl", execUserSystemd+" restart suiteward-executor.service") {
				t.Errorf("the executor still runs the old binary or token after the second run: %v", h.lines())
			}
		})
	}
}
