package installer

import (
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
)

// The host step reaches the executor's account the way the install guide reaches the service account: `runuser` as that account, with its
// own user bus. `systemd-run --machine=<user>@.host` goes through machined, which opens a login session for the account, and on EL9 with SELinux
// enforcing that session cannot set the context of a missing terminal, so the step failed on a real host (A1) and never in the lab.

func TestPrepareHostNeverReachesTheExecutorThroughMachined(t *testing.T) {
	h := newHostFake()
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if len(h.callsTo("runuser")) == 0 {
		t.Fatalf("no command ran as the executor's account: %v", h.lines())
	}
	for _, c := range h.calls {
		if c.name == "machinectl" || strings.Contains(c.line(), "--machine") {
			t.Errorf("%s %s goes through machined, which cannot open its login session without a terminal on EL9 with SELinux enforcing", c.name, c.line())
		}
	}
}

func TestPrepareHostRunsEveryCommandOfTheExecutorAsItsAccountOnItsOwnBus(t *testing.T) {
	h := newHostFake()
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	for _, c := range h.callsTo("runuser") {
		if !strings.HasPrefix(c.line(), execAs+" ") {
			t.Errorf("runuser %s does not run as %s with its XDG_RUNTIME_DIR and its bus", c.line(), ExecutorAccount)
		}
	}
	for _, want := range []string{
		execUserRun + " podman pull " + goprofile.Image, // inside the account's manager, in the cgroup the drop-in delegates, and waited for
		execUserRun + " podman image exists " + goprofile.Image,
		execUserctl + " daemon-reload",
		execUserctl + " enable --now suiteward-executor.service",
		execUserctl + " show --property=ActiveState --value suiteward-executor.service",
	} {
		if !h.has("runuser", want) {
			t.Errorf("missing runuser %s: %v", want, h.lines())
		}
	}
}

func TestPrepareHostStopsWithAClearMessageWhenTheBusOfTheExecutorIsMissing(t *testing.T) {
	h := newHostFake()
	delete(h.paths, execBus) // the user manager is not up
	_, err := runHost(t, h, declineMetadataRule, false, "")
	if err == nil || !strings.Contains(err.Error(), "user manager of "+ExecutorAccount) || !strings.Contains(err.Error(), execBus) {
		t.Fatalf("a missing bus must stop the run naming the user manager and the bus, got %v", err)
	}
	if h.has("runuser", "") {
		t.Errorf("a command ran as the executor's account without a bus to reach it on: %v", h.lines())
	}
}

// A manager that was just started or restarted does not answer at once: the step waits for it, bounded, before it pulls the image.
func TestPrepareHostWaitsForTheManagerOfTheExecutorBeforeUsingIt(t *testing.T) {
	for name, controllers := range map[string]string{"after a restart": "memory pids", "after a start": "cpu cpuset io memory pids"} {
		t.Run(name, func(t *testing.T) {
			h := newHostFake()
			h.controllers = controllers
			if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
				t.Fatal(err)
			}
			started, restarted := hostCallIndex(h, "systemctl", "start user@1002.service"), hostCallIndex(h, "systemctl", "restart user@1002.service")
			wait, pull := hostCallIndex(h, "runuser", execWaitEnd), hostCallIndex(h, "runuser", "podman pull "+goprofile.Image)
			if started < 0 || wait < started || wait < restarted || pull < wait {
				t.Fatalf("want the start (%d) and any restart (%d) of the manager, then the wait for it (%d), then the pull (%d): %v", started, restarted, wait, pull, h.lines())
			}
			if (restarted >= 0) != (controllers == "memory pids") {
				t.Errorf("restarted at %d with controllers %q", restarted, controllers)
			}
			if line := h.calls[wait].line(); !strings.HasPrefix(line, execAs+" timeout ") {
				t.Errorf("%s is not bounded: a manager that never answers would hold the step for good", line)
			}
		})
	}
}

// is-system-running exits non-zero for a degraded manager, which the guide accepts (checkUserManager reads the state, not the exit status).
func TestPrepareHostAcceptsADegradedManagerOfTheExecutor(t *testing.T) {
	h := newHostFake()
	h.managerState = "degraded"
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatalf("a degraded manager answers: %v", err)
	}
}

// The message says what happened: a manager that timed out did not answer within the bound, one that answered says its state, and one that could
// not be asked says why. The bound is named only when it was the reason.
func TestPrepareHostStopsBeforeThePullWhenTheManagerOfTheExecutorDoesNotBecomeReady(t *testing.T) {
	for name, c := range map[string]struct {
		set       func(*hostFake)
		want, not string
	}{
		"it is stopping":   {func(h *hostFake) { h.managerState = "stopping" }, `"stopping"`, "60 seconds"},
		"it never answers": {func(h *hostFake) { h.managerStuck = true }, "did not answer within 60 seconds", `it says`},
		"it cannot be asked": {func(h *hostFake) {
			h.failOn = map[string]error{"timeout 60 systemctl --user is-system-running": errInjected}
		}, errInjected.Error(), "60 seconds"},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHostFake()
			c.set(h)
			_, err := runHost(t, h, declineMetadataRule, false, "")
			if err == nil || !strings.Contains(err.Error(), "user manager of "+ExecutorAccount) || !strings.Contains(err.Error(), "journalctl") {
				t.Fatalf("a manager that is not ready must stop the run naming it and where to read why, got %v", err)
			}
			if !strings.Contains(err.Error(), c.want) || strings.Contains(err.Error(), c.not) {
				t.Errorf("the message must say %q and not %q: %v", c.want, c.not, err)
			}
			if h.has("runuser", "podman pull") {
				t.Errorf("the image was pulled although the manager was not ready: %v", h.lines())
			}
		})
	}
}

// OSExec cuts the command it names in an error at 120 characters, and the prefix of every command of the account (runuser, its bus) takes nearly all
// of them, so an error says what was run as the account itself.
func TestPrepareHostNamesWhatItRanAsTheExecutorsAccountWhenItFails(t *testing.T) {
	for name, c := range map[string]struct{ failing, want string }{
		"the pull":               {"podman pull", "podman pull " + goprofile.Image},
		"the check of the image": {"podman image exists", "podman image exists " + goprofile.Image},
		"the reload of units":    {"systemctl --user daemon-reload", "systemctl --user daemon-reload"},
		"the start":              {"systemctl --user enable --now", "systemctl --user enable --now " + executorUnitName},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHostFake()
			h.failOn = map[string]error{c.failing: errInjected}
			_, err := runHost(t, h, declineMetadataRule, false, "")
			if err == nil || !strings.Contains(err.Error(), c.want) || !strings.Contains(err.Error(), ExecutorAccount) {
				t.Fatalf("the error must name %q and the account, got %v", c.want, err)
			}
		})
	}
}
