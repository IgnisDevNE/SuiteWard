package installer

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The backup of an upgrade records /status, so an upgrade of a service that is down cannot go on. On a terminal it first offers
// to start the installed service, as it is; without a terminal, or on a no, it refuses and names the command that starts it.
// --yes confirms the upgrade plan only: it never starts a service.

const startQuestion = "The service is not running, so the backup cannot record its status. Start the installed service now? [Y/n]"

var errConnectionRefused = errors.New("connection refused")

// ---- the co-located tier ----

// downUntilStarted makes /status fail until the engine was asked to start the service container.
func (u *upgradeHarness) downUntilStarted() {
	u.status = func(ctx context.Context, address string) ([]byte, error) {
		if !u.started(ContainerServe) {
			return nil, errConnectionRefused
		}
		return httpGet(ctx, address)
	}
}

func (u *upgradeHarness) started(container string) bool {
	return u.startCalls(container) > 0
}

func (u *upgradeHarness) startCalls(container string) int {
	n := 0
	for _, c := range u.f.calls {
		if normalized(c) == "start "+container {
			n++
		}
	}
	return n
}

// upgradeAt runs the upgrade with a console that types stdin, and a terminal or not.
func (u *upgradeHarness) upgradeAt(terminal bool, stdin string, args ...string) error {
	u.t.Helper()
	env := u.env()
	term, _ := fakeTerminal(terminal, stdin)
	term.Out = &u.out
	env.Term = term
	all := append([]string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--upgrade", "--backup-dir", u.backups, "--runtime", "podman", "--connection", "pm", "--image", testImage}, args...)
	return Install(context.Background(), all, env)
}

func TestUpgradeColocatedOffersToStartAServiceThatIsDown(t *testing.T) {
	for name, typed := range map[string]string{"Enter is yes": "\n", "y": "y\n"} {
		u := newUpgradeHarness(t, nil)
		u.downUntilStarted()
		if err := u.upgradeAt(true, typed, "--yes"); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.Contains(u.out.String(), startQuestion) {
			t.Errorf("%s: the question is missing:\n%s", name, u.out.String())
		}
		// The installed containers are started as they are, the database first, and nothing is created for it.
		u.assertOrder("start "+ContainerDB, "--format {{json .State}} "+ContainerDB, "start "+ContainerServe, "pg_dump")
		if got := u.startCalls(ContainerServe); got != 1 {
			t.Errorf("%s: the service was started %d times", name, got)
		}
		if before := u.changesBefore("pg_dump"); len(before) != 0 {
			t.Errorf("%s: the stack was changed before the backup, apart from starting it: %v", name, before)
		}
		if got := u.settings().Image; got != testImage {
			t.Errorf("%s: the upgrade did not go on: the settings keep %s", name, got)
		}
		u.backup()
	}
}

func TestUpgradeColocatedARunningServiceIsNotAsked(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	if err := u.upgradeAt(true, "", "--yes"); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(u.out.String(), "Start the installed service") || u.startCalls(ContainerServe) != 0 {
		t.Errorf("a service that answers was offered a start:\n%s", u.out.String())
	}
}

func TestUpgradeColocatedANoLeavesTheServiceDownAndRefuses(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	u.downUntilStarted()
	err := u.upgradeAt(true, "n\n", "--yes") // --yes confirms the plan, not a start
	if err == nil || !strings.Contains(err.Error(), "start suiteward-db") || !strings.Contains(err.Error(), "start suiteward") {
		t.Fatalf("got %v, want a refusal that names the commands that start the service", err)
	}
	if !strings.Contains(u.out.String(), startQuestion) {
		t.Errorf("--yes must not answer the question:\n%s", u.out.String())
	}
	u.noChanges()
	if u.startCalls(ContainerDB) != 0 || u.startCalls(ContainerServe) != 0 {
		t.Error("the service was started although the answer was no")
	}
	if dirs, _ := filepath.Glob(filepath.Join(u.backups, "*")); len(dirs) != 0 {
		t.Errorf("a backup was made: %v", dirs)
	}
}

func TestUpgradeColocatedWithoutATerminalRefusesAndNamesTheStart(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	u.downUntilStarted()
	err := u.upgradeAt(false, "", "--yes") // --yes does not imply the start
	if err == nil || !strings.Contains(err.Error(), "podman --connection pm start suiteward-db") || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("got %v, want the engine commands and a word on --yes", err)
	}
	u.noChanges()
	if u.startCalls(ContainerDB) != 0 || strings.Contains(u.out.String(), startQuestion) {
		t.Error("a run without a terminal started the service or asked")
	}
}

func TestUpgradeColocatedAFailedStartRefusesAndNamesTheManualBackup(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	u.downUntilStarted()
	u.f.rules = append([]rule{{match: "start " + ContainerDB, err: errors.New("no such container")}}, u.f.rules...)
	err := u.upgradeAt(true, "\n", "--yes")
	if err == nil || !strings.Contains(err.Error(), "no such container") || !strings.Contains(err.Error(), "docs/backup.md") {
		t.Fatalf("got %v, want the failure and the manual backup", err)
	}
	u.noChanges()
}

func TestUpgradeColocatedAServiceThatNeverAnswersAfterTheStartRefuses(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	u.status = func(context.Context, string) ([]byte, error) { return nil, errConnectionRefused }
	slept := 0
	env := u.env()
	term, _ := fakeTerminal(true, "\n")
	term.Out = &u.out
	env.Term = term
	env.Sleep = func(time.Duration) { slept++ }
	args := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--upgrade", "--backup-dir", u.backups, "--runtime", "podman", "--connection", "pm", "--image", testImage, "--yes"}
	err := Install(context.Background(), args, env)
	if err == nil || !strings.Contains(err.Error(), "docs/backup.md") || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("got %v, want the last /status error and the manual backup", err)
	}
	if slept == 0 || slept > 90 {
		t.Errorf("the wait slept %d times, want a bound of about a minute", slept)
	}
	if u.startCalls(ContainerServe) != 1 {
		t.Error("the service was not started once")
	}
	u.noChanges()
}

// ---- the isolated tier ----

func (u *isoUpgrade) downUntilStarted() func(context.Context, string) ([]byte, error) {
	return func(ctx context.Context, address string) ([]byte, error) {
		if !u.f.started {
			return nil, errConnectionRefused
		}
		return httpGet(ctx, address)
	}
}

func (u *isoUpgrade) upgradeAt(terminal bool, stdin string, status func(context.Context, string) ([]byte, error), args ...string) error {
	u.t.Helper()
	fastHealthWait(u.t)
	env := u.env()
	term, _ := fakeTerminal(terminal, stdin)
	term.Out = &u.out
	env.Term = term
	env.HTTPGet = status
	return Install(context.Background(), append([]string{"--upgrade", "--tier", "isolated", "--settings", settingsFile(u.home), "--backup-dir", u.backups, "--image", testImage}, args...), env)
}

func TestUpgradeIsolatedOffersToStartAServiceThatIsDown(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	u.f.serviceRunning = false
	if err := u.upgradeAt(true, "\n", u.downUntilStarted(), "--yes"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u.out.String(), startQuestion) {
		t.Errorf("the question is missing:\n%s", u.out.String())
	}
	start, dump := u.f.index("systemctl --user start suiteward.service", 0), u.f.index("pg_dump", 0)
	if start < 0 || dump < 0 || start > dump {
		t.Errorf("the service was not started before the backup (start %d, dump %d):\n%s", start, dump, strings.Join(u.f.lines(), "\n"))
	}
	for _, change := range []string{"systemctl --user stop", "daemon-reload", "podman pull"} {
		if i := u.f.index(change, 0); i >= 0 && i < dump {
			t.Errorf("%s ran before the backup", change)
		}
	}
	if got := u.settings().Image; got != testImage {
		t.Errorf("the upgrade did not go on: the settings keep %s", got)
	}
}

func (u *isoUpgrade) settings() Settings {
	u.t.Helper()
	s, err := LoadSettings(settingsFile(u.home))
	if err != nil {
		u.t.Fatal(err)
	}
	return s
}

func TestUpgradeIsolatedANoOrNoTerminalRefusesAndNamesTheStart(t *testing.T) {
	for name, c := range map[string]struct {
		terminal bool
		stdin    string
	}{"no": {true, "n\n"}, "no terminal": {false, ""}} {
		u := newIsoUpgrade(t, nil)
		before := u.units()
		err := u.upgradeAt(c.terminal, c.stdin, u.downUntilStarted(), "--yes")
		if err == nil || !strings.Contains(err.Error(), "systemctl --user start suiteward.service") || !strings.Contains(err.Error(), "--yes") {
			t.Fatalf("%s: got %v, want the command that starts the service and a word on --yes", name, err)
		}
		u.untouched(before)
		if u.f.started {
			t.Errorf("%s: the service was started", name)
		}
	}
}

func TestUpgradeIsolatedAFailedStartRefusesAndNamesTheManualBackup(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	u.f.failOn["systemctl --user start suiteward.service"] = errors.New("unit not found")
	err := u.upgradeAt(true, "\n", u.downUntilStarted(), "--yes")
	if err == nil || !strings.Contains(err.Error(), "unit not found") || !strings.Contains(err.Error(), "docs/backup.md") {
		t.Fatalf("got %v, want the failure and the manual backup", err)
	}
}

func TestUpgradeIsolatedAServiceThatNeverAnswersAfterTheStartRefuses(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	never := func(context.Context, string) ([]byte, error) { return nil, errConnectionRefused }
	err := u.upgradeAt(true, "\n", never, "--yes")
	if err == nil || !strings.Contains(err.Error(), "docs/backup.md") || !strings.Contains(err.Error(), "connection refused") {
		t.Fatalf("got %v, want the last /status error and the manual backup", err)
	}
	if !u.f.started {
		t.Error("the service was not started")
	}
	if i := u.f.index("pg_dump", 0); i >= 0 {
		t.Error("a backup was attempted")
	}
}

func TestUpgradeHelpSaysYesDoesNotStartTheService(t *testing.T) {
	var out strings.Builder
	h := newHarness(t, "linux")
	env := h.newEnv()
	env.Term.Out = &out
	_ = Install(context.Background(), []string{"--help"}, env) // flag.ErrHelp
	if !strings.Contains(out.String(), "does not start") {
		t.Errorf("--yes in the help does not say that it leaves a stopped service alone:\n%s", out.String())
	}
}

// ---- review: the wait has a deadline, and a no to the plan after a start does not claim that nothing changed ----

func TestUpgradeColocatedTheWaitAfterTheStartHasADeadline(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	var deadlines []time.Duration
	probed := false
	u.status = func(ctx context.Context, address string) ([]byte, error) {
		if !u.started(ContainerServe) {
			return nil, errConnectionRefused
		}
		if !probed { // the first probe after the start: a probe through the Podman machine can take seconds
			probed = true
			if dl, ok := ctx.Deadline(); ok {
				deadlines = append(deadlines, time.Until(dl))
			}
		}
		return httpGet(ctx, address)
	}
	if err := u.upgradeAt(true, "\n", "--yes"); err != nil {
		t.Fatal(err)
	}
	if len(deadlines) != 1 || deadlines[0] <= 0 || deadlines[0] > 95*time.Second {
		t.Errorf("the probes after the start have deadlines %v, want one of about 90 s for the whole wait", deadlines)
	}
}

func TestUpgradeANoToThePlanAfterAStartSaysTheServiceWasStarted(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	u.downUntilStarted()
	err := u.upgradeAt(true, "\nn\n") // yes to the start, no to the plan: no --yes
	if err == nil || !strings.Contains(err.Error(), "service was started") || strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("co-located: got %v, want that the service was started and nothing else changed", err)
	}

	i := newIsoUpgrade(t, nil)
	i.f.serviceRunning = false
	err = i.upgradeAt(true, "\nn\n", i.downUntilStarted())
	if err == nil || !strings.Contains(err.Error(), "service was started") || strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("isolated: got %v, want that the service was started and nothing else changed", err)
	}
	// Without a start, the plan's no still changes nothing.
	u = newUpgradeHarness(t, nil)
	if err := u.upgradeAt(true, "n\n"); err == nil || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("a no to the plan of a running service: got %v", err)
	}
}
