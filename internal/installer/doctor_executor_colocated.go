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
// what the service saw. The executor runs as this user, next to the coding agent, so the pins catch drift and accident and not a determined agent.
// The rows do not repeat that reduced assurance: the install's summary and the page that introduces this tier say it once (owner decision 19).
// Their texts are for a person with little technical background (work/m21-briefs/plain-language.md): what happened, whether it is a problem, what to do.

// installFix is the fix of a result that an install of this release makes right.
func (d *doctor) installFix() string {
	cmd := d.deps.command()
	return fmt.Sprintf("Run `%[1]s install --upgrade` if the service is from an earlier release. Otherwise run `%[1]s install` again. Either one sets up the test runner again.", cmd)
}

// anotherInstanceFix is the fix of an executor that is registered for another instance: an install of this one skips it, so it is that instance's removal that frees the place.
func (d *doctor) anotherInstanceFix() string {
	cmd := d.deps.command()
	return "A computer has one test runner, and it belongs to another instance. Remove that instance with `" + cmd + " uninstall` (pass that instance's `--settings` and `--token-file`; the default instance needs neither). Then run `" + cmd + " install` again for this one. Until then the install leaves the other test runner alone."
}

// executorTokenFile is the token file that the files of the executor are beside: the one --token-file names, else the default place of this user's
// token file, which is this instance's only with the default settings file (the rule of the install). When there is none it says why, as the one result
// of the executor: no other row is checked, because it would read the main instance's folder.
func (d *doctor) executorTokenFile() (string, bool) {
	if d.tokenFile != "" {
		return d.tokenFile, true
	}
	file, err := defaultTokenFile(d.deps)
	if err != nil {
		d.fail("executor.token", err.Error(), "Doctor looks for the files of the test runner (the executor) there. Set the variable the message names, or pass `--token-file`.")
		return "", false
	}
	isDefault, err := isDefaultSettings(d.deps.GOOS, d.settingsPath, !d.settingsNamed)
	if err != nil {
		d.fail("executor.token", err.Error(), "Pass `--token-file` with this instance's token file.")
		return "", false
	}
	if !isDefault {
		d.fail("executor.token", "`--settings` names `"+d.settingsPath+"`, which is not the default settings file, so this is a second instance. Without `--token-file`, doctor cannot tell which test runner (the executor) is yours: the default token file `"+file+"` belongs to the main instance. The test runner was not checked.",
			"Pass `--token-file` with this instance's token file (the one its install used). If this is the only instance on this computer and `"+file+"` is its token file, pass `--token-file "+quoteArg(file)+"`.")
		return "", false
	}
	return file, true
}

// checkExecutorColocated runs the checks of the executor on this computer, beside the token file that executorTokenFile finds.
func (d *doctor) checkExecutorColocated(ctx context.Context) {
	tokenFile, ok := d.executorTokenFile()
	if !ok {
		return
	}
	plan, err := planExecutor(d.deps, tokenFile, d.s, engineBinDir(d.rt))
	if err != nil {
		d.fail("executor.lifecycle", err.Error(), "Do what the message says, then run `"+d.deps.command()+" doctor` again.")
		return
	}
	registered, running, stateErr := plan.state(ctx, d.deps)
	other := false // what is registered is another instance's executor: a computer has one, and the install skips it
	if stateErr == nil {
		other, stateErr = plan.foreign(registered)
	}
	fix, notRunning := d.installFix(), plan.startHint()
	if other { // no install makes this instance an executor of its own while that one is registered
		fix = d.anotherInstanceFix()
		notRunning = fix
	}
	d.checkExecutorToken(plan) // the install makes the token copy and the service's token whatever becomes of the executor, so its fix stays the install's
	d.checkExecutorColocatedBinary(plan, fix)
	current, definitionErr := true, error(nil) // the registered definition is this build's: the one of another instance is not read
	if stateErr == nil && !other && registered {
		current, definitionErr = plan.definitionIsCurrent(d.deps)
	}
	switch {
	case stateErr != nil:
		d.fail("executor.lifecycle", "Doctor could not ask the operating system about the test runner: "+stateErr.Error(), d.installFix())
	case other:
		d.fail("executor.lifecycle", "The test runner on this computer belongs to another instance, not to the one that uses the token file `"+tokenFile+"`. A computer has one test runner.", fix)
	case !registered:
		d.fail("executor.lifecycle", "The test runner is not set up on this computer. It should be "+executorMechanism(plan.goos)+".", d.installFix())
	case definitionErr != nil:
		d.fail("executor.lifecycle", "Doctor could not read the test runner's task definition: "+definitionErr.Error(), d.installFix())
	case !current:
		d.fail("executor.lifecycle", "The scheduled task of the test runner is not the one this version of SuiteWard makes (it may come from an older version, or it was changed). This needs your action.", d.installFix())
	default:
		d.pass("executor.lifecycle", "The test runner is set up as "+executorMechanism(plan.goos)+".")
	}
	d.checkExecutorRunning(ctx, func(context.Context) error {
		switch {
		case stateErr != nil:
			return fmt.Errorf("doctor could not tell whether the test runner is running: %w", stateErr)
		case other:
			return errors.New("the test runner on this computer belongs to another instance, so none runs for this one")
		case !running:
			return errors.New("the test runner is not running")
		}
		return nil
	}, runningFixes{
		noToken:    d.installFix(),
		notRunning: notRunning,
		notSeen:    "Read the test runner's log (" + plan.logWhere() + ") to see why. Then run `" + d.deps.command() + " install` again: it makes the service and the test runner use the same token and port.",
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
		d.fail("executor.image", "The container program does not have the Go image that the test runner's sandboxes use ("+goprofile.Image+"). The test runner does not download it, so no test can run.", d.installFix())
		return
	}
	d.pass("executor.image", "The container program has the Go image that the test runner's sandboxes use.")
}

// startHint is how to start the executor by hand where the operating system keeps it, and where to read why it stopped.
func (p executorPlan) startHint() string {
	switch p.goos {
	case "windows":
		return "Start it with `schtasks /Run /TN \"" + executorTaskName + "\"`. To see why it stopped, read `" + p.log + "`."
	case "darwin":
		return "Start it with `launchctl kickstart -k gui/$(id -u)/" + executorLabel + "`. To see why it stopped, read `" + p.log + "`."
	default:
		return "Start it with `systemctl --user start " + executorUnitName + "`. To see why it stopped, run `journalctl --user -u " + executorUnitName + "`."
	}
}

// checkExecutorToken checks that the executor has its token copy and that the service has the token: a service container of an earlier release
// has neither the variable nor the secret.
func (d *doctor) checkExecutorToken(plan executorPlan) {
	var problems []string
	if info, err := os.Stat(plan.tokenFile); err != nil || !info.Mode().IsRegular() {
		problems = append(problems, "The token file of the test runner (the executor) is missing: `"+plan.tokenFile+"`.")
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
		problems = append(problems, "The service has no token for the test runner: it was made by an earlier release.")
	}
	if len(problems) > 0 {
		d.fail("executor.token", strings.Join(problems, " "), d.installFix())
		return
	}
	d.pass("executor.token", "The test runner (the executor) has its token file, and the service is set up to read the matching token.")
}

// checkExecutorColocatedBinary checks the executor's copy of the program: there, as installed (its pin), and this program's build. The pin
// is the release's line of SHA256SUMS when the install was given them (--sha256sums), else the hash of the program that the install copied, which is a
// warning: nothing checked it against a release.
func (d *doctor) checkExecutorColocatedBinary(plan executorPlan, fix string) {
	sum, err := hashFile(plan.binary)
	if errors.Is(err, fs.ErrNotExist) {
		d.fail("executor.binary", "The test runner's program is missing: `"+plan.binary+"`.", fix)
		return
	}
	if err != nil {
		d.fail("executor.binary", err.Error(), fix)
		return
	}
	pin, err := os.ReadFile(plan.pin)
	fields := strings.Fields(string(pin))
	if err != nil || len(fields) == 0 {
		d.fail("executor.binary", "The record of the test runner's fingerprint is missing: `"+plan.pin+"`.", fix)
		return
	}
	own, err := d.ownHash()
	switch {
	case err != nil:
		d.fail("executor.binary", "Doctor could not read its own program: "+err.Error(), "This looks like a bug in the installer. Please report it.")
	case sum != fields[0]:
		d.fail("executor.binary", fmt.Sprintf("The test runner's program has changed since it was installed. Its fingerprint (SHA-256 %s) no longer matches the recorded one (%s). Do not go on until this is fixed.", sum, fields[0]), fix)
	case sum != own:
		d.fail("executor.binary", fmt.Sprintf("The test runner is a different version from this program. Its fingerprint (SHA-256) is %s; this program's is %s. This needs your action.", sum, own), fix)
	case len(fields) >= 2 && releaseAssetOf(plan.goos).MatchString(fields[1]):
		d.pass("executor.binary", "The test runner's program matches the official release: its fingerprint (SHA-256 "+sum+") is listed in the release's SHA256SUMS as "+fields[1]+".")
	default:
		d.warn("executor.binary", "The test runner has not been checked against an official release. This is normal for a development build.",
			"To check it, run `"+d.deps.command()+" install --upgrade --sha256sums <the release's SHA256SUMS file>` with the program from that release.")
	}
}

// checkExecutorLoopback checks that this computer reaches the port that a Podman machine publishes. The executor is a program of this computer,
// and on Windows and macOS the machine's published loopback port does not always answer from outside it; the service then never sees the
// executor, and the runs read no-executor, which the owner can unblock.
func (d *doctor) checkExecutorLoopback(ctx context.Context) {
	if !d.viaSSH || d.machine.Name == "" {
		return
	}
	address := fmt.Sprintf("http://127.0.0.1:%d/readyz", d.s.HTTPPort)
	if _, err := d.fetch(ctx, address); err != nil {
		d.fail("executor.loopback", fmt.Sprintf("This computer cannot reach `127.0.0.1:%d`, the port that the Podman machine %s publishes (%v). The test runner cannot reach the service either, so its test runs end with `no-executor`. The owner can unblock each revision with `/suiteward unblock`.", d.s.HTTPPort, d.machine.Name, err),
			"This is a known limit of the machine's port forwarding. Run `podman machine stop`, then `podman machine start`, then run doctor again. If it persists, the owner unblocks each revision with `/suiteward unblock`.")
		return
	}
	d.pass("executor.loopback", fmt.Sprintf("This computer reaches `127.0.0.1:%d`, the port that the Podman machine %s publishes.", d.s.HTTPPort, d.machine.Name))
}

// logWhere is where the executor's log is: a file where the operating system has the program write one, the journal of the user's manager on Linux.
func (p executorPlan) logWhere() string {
	if p.goos == "linux" {
		return "journalctl --user -u " + executorUnitName
	}
	return p.log
}
