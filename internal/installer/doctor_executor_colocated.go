package installer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
)

// The checks of the executor of the co-located tier: what an install leaves beside the token copies, what the operating system says of it, and
// what the service saw. The executor runs as this user, next to the coding agent, so the pins catch drift and accident and not a determined agent:
// the check says that the assurance is reduced, as the approvals of this tier do (ADR 0027, ADR 0028 section 13).

// installFix is the fix of a result that an install of this release makes right.
func (d *doctor) installFix() string {
	cmd := d.deps.command()
	return fmt.Sprintf("run `%[1]s install --upgrade` with this program when the service is of an earlier release, or `%[1]s install` again: either copies this program as the executor, makes its token and starts it", cmd)
}

// anotherInstanceFix is the fix of an executor that is registered for another instance: an install of this one skips it, so it is that instance's removal that frees the place.
func (d *doctor) anotherInstanceFix() string {
	cmd := d.deps.command()
	return "a computer has one executor, and it is registered for another instance: remove that instance (`" + cmd + " uninstall`, with its own --settings and --token-file), then run `" + cmd + " install` again for this one; until then the install leaves the registered executor as it is"
}

// checkExecutorColocated runs the checks of the executor on this computer. Doctor finds its files beside the token file that --token-file names, else
// beside the default place of this user's token file.
func (d *doctor) checkExecutorColocated(ctx context.Context) {
	tokenFile := d.tokenFile
	if tokenFile == "" {
		var err error
		if tokenFile, err = defaultTokenFile(d.deps); err != nil {
			d.fail("executor.token", err.Error(), "this is where doctor looks for the files of the executor: set the variable it names, or pass --token-file")
			return
		}
	}
	plan, err := planExecutor(d.deps, tokenFile, d.s, engineBinDir(d.rt))
	if err != nil {
		d.fail("executor.lifecycle", err.Error(), "set the variable it names, then run doctor again")
		return
	}
	d.checkExecutorToken(plan)
	d.checkExecutorColocatedBinary(plan)
	registered, running, stateErr := plan.state(ctx, d.deps)
	other := false // what is registered is another instance's executor: a computer has one, and the install skips it
	if stateErr == nil {
		other, stateErr = plan.foreign(registered)
	}
	notRunning := plan.startHint()
	if other {
		notRunning = d.anotherInstanceFix()
	}
	switch {
	case stateErr != nil:
		d.fail("executor.lifecycle", "ask the operating system about the executor: "+stateErr.Error(), d.installFix())
	case other:
		d.fail("executor.lifecycle", "the executor of this user is registered for another instance, not for the token file "+tokenFile+": a computer has one executor", d.anotherInstanceFix())
	case !registered:
		d.fail("executor.lifecycle", "the executor is not registered with the operating system: it should be "+executorMechanism(plan.goos), d.installFix())
	default:
		d.pass("executor.lifecycle", "the executor is registered as "+executorMechanism(plan.goos))
	}
	d.checkExecutorRunning(ctx, func(context.Context) error {
		switch {
		case stateErr != nil:
			return fmt.Errorf("it is not known whether the executor runs: %w", stateErr)
		case other:
			return errors.New("the executor that is registered is another instance's, so none runs for this one")
		case !running:
			return errors.New("the executor is not running")
		}
		return nil
	}, runningFixes{
		noToken:    d.installFix(),
		notRunning: notRunning,
		notSeen:    "read the executor's log (" + plan.logWhere() + "), then " + d.installFix() + ", so that the service and the executor hold one token and one port",
	})
	d.checkExecutorImage(ctx)
	d.checkExecutorLoopback(ctx)
}

// checkExecutorImage asks the engine that runs the sandboxes whether it has the Go image under its digest: the executor starts them with
// --pull=never, so without it every job is unavailable. The install pulls it.
func (d *doctor) checkExecutorImage(ctx context.Context) {
	if !d.needRuntime("executor.image") {
		return
	}
	if !d.rt.ImageExists(ctx, goprofile.Image) {
		d.fail("executor.image", "the engine has no Go image of the sandboxes ("+goprofile.Image+"): the executor starts them without pulling, so every execution job is unavailable", d.installFix())
		return
	}
	d.pass("executor.image", "the engine has the Go image of the sandboxes, under its digest")
}

// startHint is how to start the executor by hand where the operating system keeps it, and where to read why it stopped.
func (p executorPlan) startHint() string {
	switch p.goos {
	case "windows":
		return `start it with schtasks /Run /TN "` + executorTaskName + `" and read why it stopped in ` + p.log
	case "darwin":
		return "start it with launchctl kickstart -k gui/$(id -u)/" + executorLabel + " and read why it stopped in " + p.log
	default:
		return "start it with systemctl --user start " + executorUnitName + " and read why it stopped with journalctl --user -u " + executorUnitName
	}
}

// checkExecutorToken checks that the executor has its token copy and that the service has the token: a service container of an earlier release
// has neither the variable nor the secret.
func (d *doctor) checkExecutorToken(plan executorPlan) {
	var problems []string
	if info, err := os.Stat(plan.tokenFile); err != nil || !info.Mode().IsRegular() {
		problems = append(problems, "the executor's token copy "+plan.tokenFile+" is not there")
	}
	raw, ok := d.raw[ContainerServe]
	if !ok {
		d.skip("executor.token", "containers")
		return
	}
	in, err := parseInspect(ContainerServe, raw)
	if err != nil {
		problems = append(problems, err.Error())
	} else if _, has := envMap(in.Config.Env)[executorAuthFileEnv]; !has {
		problems = append(problems, "the service container has no executor token: it was made by a release before the executor")
	}
	if len(problems) > 0 {
		d.fail("executor.token", strings.Join(problems, "; "), d.installFix())
		return
	}
	d.pass("executor.token", "the executor has its token copy, and the service container reads its token from "+executorAuthFileEnv)
}

// checkExecutorColocatedBinary checks the executor's copy of the program: there, as installed (its pin), and this program's build. The pin
// is the release's line of SHA256SUMS when the install was given them (--sha256sums), else the hash of the program that the install copied, which is a
// warning: nothing checked it against a release.
func (d *doctor) checkExecutorColocatedBinary(plan executorPlan) {
	sum, err := hashFile(plan.binary)
	if errors.Is(err, fs.ErrNotExist) {
		d.fail("executor.binary", "no executor binary at "+plan.binary, d.installFix())
		return
	}
	if err != nil {
		d.fail("executor.binary", err.Error(), d.installFix())
		return
	}
	pin, err := os.ReadFile(plan.pin)
	fields := strings.Fields(string(pin))
	if err != nil || len(fields) == 0 {
		d.fail("executor.binary", "no pin of the executor's binary at "+plan.pin, d.installFix())
		return
	}
	own, err := d.ownHash()
	switch {
	case err != nil:
		d.fail("executor.binary", "hash this program: "+err.Error(), "this is a bug of the installer: report it")
	case sum != fields[0]:
		d.fail("executor.binary", fmt.Sprintf("the executor's binary has changed since it was installed (SHA-256 %s, pinned %s)", sum, fields[0]), d.installFix())
	case sum != own:
		d.fail("executor.binary", fmt.Sprintf("the executor runs another build than this program (the executor's SHA-256 %s, this program's %s)", sum, own), d.installFix())
	case len(fields) >= 2 && releaseAssetOf(plan.goos).MatchString(fields[1]):
		d.pass("executor.binary", "the executor's binary (SHA-256 "+sum+") is the pinned one, this program's, and the release's line of SHA256SUMS "+fields[1]+". "+colocatedAssurance)
	default:
		d.warn("executor.binary", "the executor's binary (SHA-256 "+sum+") is the pinned one and this program's, but the pin was not made from a release's SHA256SUMS, so nothing checked it against the release. "+colocatedAssurance,
			"run `"+d.deps.command()+" install --upgrade --sha256sums <the SHA256SUMS of the release>` with that release's binary")
	}
}

// colocatedAssurance is what doctor says of the executor of this tier whether or not its pin is a release's line.
const colocatedAssurance = "This tier has reduced assurance: it runs as your user, next to your coding agent, so these pins catch drift and accident, not a determined agent"

// checkExecutorLoopback checks that this computer reaches the port that a Podman machine publishes. The executor is a program of this computer,
// and on Windows and macOS the machine's published loopback port does not always answer from outside it; the service then never sees the
// executor, and the runs read no-executor, which the owner can unblock.
func (d *doctor) checkExecutorLoopback(ctx context.Context) {
	if !d.viaSSH || d.machine.Name == "" {
		return
	}
	address := fmt.Sprintf("http://127.0.0.1:%d/readyz", d.s.HTTPPort)
	if _, err := d.fetch(ctx, address); err != nil {
		d.fail("executor.loopback", fmt.Sprintf("this computer cannot reach 127.0.0.1:%d, the port that the Podman machine %s publishes (%v), so the executor, a program of this computer, cannot reach the service: its runs read no-executor, and the owner can unblock each head", d.s.HTTPPort, d.machine.Name, err),
			"a known limit of the machine's port forwarding: restart the machine (`podman machine stop`, then `podman machine start`) and run doctor again; if it persists, the owner unblocks the heads with `/suiteward unblock`")
		return
	}
	d.pass("executor.loopback", fmt.Sprintf("this computer reaches 127.0.0.1:%d, the port that the Podman machine %s publishes", d.s.HTTPPort, d.machine.Name))
}

// logWhere is where the executor's log is: a file where the operating system has the program write one, the journal of the user's manager on Linux.
func (p executorPlan) logWhere() string {
	if p.goos == "linux" {
		return "journalctl --user -u " + executorUnitName
	}
	return p.log
}
