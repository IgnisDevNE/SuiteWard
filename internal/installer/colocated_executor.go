package installer

import (
	"bytes"
	"cmp"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// The install of the executor on the co-located tier, which executor_colocated.go describes: what the install, the upgrade and the uninstall do
// with its program, its definition and its start.

// copyProgram copies the program src to dst through a temporary file beside it and renames that over dst, so that dst is the old file or the whole
// new one. On Windows a program that runs cannot be replaced: the caller stops it first.
func copyProgram(src, dst string) error {
	in, err := os.Open(filepath.Clean(src))
	if err != nil {
		return fmt.Errorf("read %s: %w", src, err)
	}
	defer func() { _ = in.Close() }()
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".tmp-*")
	if err != nil {
		return fmt.Errorf("create a temporary file in %s: %w", filepath.Dir(dst), err)
	}
	_, copyErr := io.Copy(tmp, in)
	var chmodErr, syncErr error
	if copyErr == nil {
		chmodErr = tmp.Chmod(0o700) // a program of this user, in a directory of this user
		syncErr = tmp.Sync()
	}
	if err := errors.Join(copyErr, chmodErr, syncErr, tmp.Close()); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	if err := os.Rename(tmp.Name(), dst); err != nil {
		_ = os.Remove(tmp.Name())
		return fmt.Errorf("replace %s: %w", dst, err)
	}
	return nil
}

// executorMechanism says in a few words how the operating system keeps the executor running.
func executorMechanism(goos string) string {
	switch goos {
	case "windows":
		return "a scheduled task of your user, started at logon"
	case "darwin":
		return "a launch agent of your user, started at login"
	default:
		return "a systemd user unit, started with your user manager"
	}
}

// installExecutor makes the executor of this computer as the settings and this program say: its copy of the program and the pin beside it, the
// definition that the operating system reads, and the start. What is already as it should be is left alone: an executor that runs, with the same
// program and the same definition, is not stopped, because it may be in the middle of a job. A program that is replaced, or a definition that
// changes, stops it first (a program that runs cannot be replaced on Windows) and starts it again.
func (c *colocatedInstall) installExecutor(ctx context.Context) (executorPlan, error) {
	d := c.env.Deps
	if d.Executable == nil {
		return executorPlan{}, errors.New("the installer cannot find its own program to copy it as the executor")
	}
	exe, err := d.Executable()
	if err != nil {
		return executorPlan{}, fmt.Errorf("find this program: %w", err)
	}
	plan, err := planExecutor(d, c.tokenFile, c.s, engineBinDir(c.rt))
	if err != nil {
		return executorPlan{}, err
	}
	sid := ""
	if d.GOOS == "windows" {
		if d.UserSID == nil {
			return plan, errors.New("the installer has no way to find the SID of this user, which the scheduled task of the executor names")
		}
		if sid, err = d.UserSID(); err != nil {
			return plan, fmt.Errorf("find the SID of this user: %w", err)
		}
		if !strings.HasPrefix(sid, "S-1-") {
			return plan, fmt.Errorf("find the SID of this user: %q is not a SID", sid)
		}
	}
	sum, err := hashFile(exe)
	if err != nil {
		return plan, err
	}
	if !pathExists(plan.tokenFile) {
		return plan, fmt.Errorf("the executor's token copy %s is not there, so an executor would not start: `%s install` makes it and gives the service the token", plan.tokenFile, d.command())
	}
	registered, running, err := plan.state(ctx, d)
	if err != nil {
		return plan, err
	}
	if other, err := plan.foreign(registered); err != nil {
		return plan, err
	} else if other {
		return plan, errors.New("a computer has one executor, and the one of this user is registered for another instance's token file: this instance did not change it; remove that instance (`uninstall`), then install again")
	}
	if err := os.MkdirAll(plan.dir, 0o700); err != nil {
		return plan, fmt.Errorf("create %s: %w", plan.dir, err)
	}
	have, _ := hashFile(plan.binary) // a copy that is not there yet has no hash, and is copied
	replace := have != sum
	files := plan.definitionFiles(sid)
	changed := replace
	for path, content := range files {
		if on, err := os.ReadFile(filepath.Clean(path)); err != nil || !bytes.Equal(on, content) {
			changed = true
		}
	}
	if running && changed {
		plan.stop(ctx, d)
		running = false
	}
	if replace {
		if err := copyProgram(exe, plan.binary); err != nil {
			return plan, err
		}
	}
	for path, content := range files {
		if err := writeFileAtomic(path, content, 0o700); err != nil {
			return plan, err
		}
	}
	pin := cmp.Or(c.executorPin, sum+"  "+executorPinName) // the release's line, or the hash of the program as copied
	if err := writeFileAtomic(plan.pin, []byte(pin+"\n"), 0o700); err != nil {
		return plan, err
	}
	if running {
		return plan, nil
	}
	return plan, plan.start(ctx, d)
}

// setUpExecutor installs or refreshes the executor after the stack is up. The stack does not depend on it, so a failure is a warning that says
// what to do, as the restart service's is, and doctor reports what is missing.
func (c *colocatedInstall) setUpExecutor(ctx context.Context) {
	scr := screenOn(c.out)
	plan, err := c.installExecutor(ctx)
	cmd := c.env.Deps.command()
	if err != nil {
		c.executorRow = "not set up: " + oneLine(err.Error())
		scr.Warn("Note: the executor could not be set up (%v). The stack runs, and canonical execution has nothing to run on until the executor does: fix the cause and run `%s install` again; `%s doctor` says what is missing.\n", err, cmd, cmd)
		return
	}
	c.executorRow = executorMechanism(plan.goos) + "; " + plan.binary
	scr.Check("The executor runs a copy of this program, %s, as %s.\n", plan.binary, executorMechanism(plan.goos))
	if plan.goos == "windows" {
		scr.Warn("A console window shows when the executor starts: a task of the logged-on user cannot hide it. Closing the window stops the executor, and the task starts it again within a minute.\n")
	}
	if c.s.Runtime == RuntimeDocker {
		scr.Say("The executor runs its sandboxes through Docker's own engine: Docker's own CLI pointed at Podman's socket fails closed, because it does not know the flags the sandbox needs, while Podman's docker shim (the podman-docker package) works against Podman. Use --runtime podman for Podman.\n")
	}
	scr.Say("The executor holds one credential, its own token (%s), and it reaches the service at http://127.0.0.1:%d. It runs as your user, next to your coding agent, so its assurance is lower than on a separate host: what doctor pins catches drift and accident, not a determined agent.\n", plan.tokenFile, c.s.HTTPPort)
	scr.Say("Keep one executor for each container engine: at start it removes every %s container of its engine, so a second one (the executor started by hand, say) would remove the sandboxes of the first.\n", sandboxNamePrefix)
	if c.executorPin == "" {
		scr.Say("The executor's binary was not checked against a release's SHA256SUMS: it is a copy of this program as it is. Verify the download as the install guide says, then run `%s install --upgrade --sha256sums <the SHA256SUMS of the release>` to pin it to the release's line.\n", c.env.Deps.command())
	}
}

// removeExecutor stops the executor, takes it out of the operating system and deletes its files, the token copy apart (a purge removes that).
// existed says that there was something of it to remove; foreign, that the one that is registered is another instance's, which is left alone.
func removeExecutor(ctx context.Context, d Deps, tokenFile string) (existed, foreign bool, err error) {
	plan, err := planExecutor(d, tokenFile, Settings{Runtime: RuntimePodman}, "")
	if err != nil {
		return false, false, err
	}
	registered, _, err := plan.state(ctx, d)
	if err != nil {
		if !pathExists(plan.dir) && !pathExists(plan.definition) {
			return false, false, nil // nothing of the executor was made here, so an operating system that cannot be asked has nothing to say of it
		}
		return false, false, err
	}
	if foreign, err = plan.foreign(registered); err != nil || foreign {
		return false, foreign, err // the executor of another instance is not this one's to remove
	}
	if !registered && !pathExists(plan.dir) {
		return false, false, nil
	}
	return true, false, plan.remove(ctx, d, registered)
}

// removeByHand is how a person stops the executor and removes it where the operating system keeps it, for an uninstall that could not: the
// commands of each system, then the executor's directory.
func removeByHand(goos string) string {
	switch goos {
	case "windows":
		return fmt.Sprintf("end the scheduled task with `schtasks /End /TN \"%[1]s\"`, end the process %[2]s if it still runs, delete the task with `schtasks /Delete /TN \"%[1]s\" /F`, and delete that directory", executorTaskName, executorBinaryName(goos))
	case "darwin":
		return fmt.Sprintf("`launchctl bootout gui/$(id -u)/%[1]s` stops it and unloads it, and deleting that directory and ~/Library/LaunchAgents/%[1]s.plist removes it", executorLabel)
	default:
		return fmt.Sprintf("where the user manager of this user runs, `systemctl --user disable --now %s` stops it, and deleting that directory and its unit file removes it", executorUnitName)
	}
}

// pathExists says whether something is at the path.
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// instanceTokenFile is the token file of the instance an install or an upgrade works on: --token-file, else the per-user default, which is this
// instance's only with the default settings file (the rule of tokenFileOf). The executor's files, its unit and its start are beside the token file,
// so another settings file with no --token-file would plan in, stop and restart the main instance's executor. It refuses before anything is changed.
func instanceTokenFile(r *Resolver, env Env, t installTarget) (string, error) {
	if file := r.preset("token-file", ""); file != "" {
		return file, nil
	}
	file, err := defaultTokenFile(env.Deps)
	if err != nil {
		return "", err
	}
	isDefault, err := isDefaultSettings(env.Deps.GOOS, t.settingsPath, t.defaultPath)
	if err != nil {
		return "", err
	}
	if !isDefault {
		return "", fmt.Errorf("--settings names %[1]s, which is not the default settings file, so this is a second instance, and without --token-file this command would use the default token file %[2]s, which is the main instance's (its executor and its token copy are beside it): pass --token-file with a file of this instance's own; if this is the only instance on this computer and %[2]s is its token file, pass --token-file %[3]s; nothing was changed", t.settingsPath, file, quoteArg(file))
	}
	return file, nil
}

// refreshExecutor is what an upgrade at the installed release does about the executor: the same as an install, so that a program that was
// replaced since reaches the executor's copy, and an executor that stopped is started. It needs the token copy that the install made.
func refreshExecutor(ctx context.Context, env Env, rt Runtime, s Settings, tokenFile, pin string) {
	c := &colocatedInstall{githubInstall: githubInstall{rt: rt, env: env, out: env.Term.Out, s: s}, tokenFile: tokenFile, executorPin: pin}
	c.setUpExecutor(ctx)
}

// releasePin is the pin that --sha256sums gives the executor: the line of the release's SHA256SUMS that lists this program for this system, checked
// before anything is changed. It is empty when the flag is not given: the pin is then the hash of the program as copied.
func releasePin(r *Resolver, d Deps) (string, error) {
	sumsFile, given := r.given("sha256sums")
	if !given {
		return "", nil
	}
	if strings.TrimSpace(sumsFile) == "" {
		return "", fmt.Errorf("%w: --sha256sums is empty (an unset variable?): give the SHA256SUMS file of the release, or leave the flag out", ErrUsage)
	}
	if d.Executable == nil {
		return "", errors.New("the installer cannot find its own program to check it against the SHA256SUMS")
	}
	exe, err := d.Executable()
	if err != nil {
		return "", fmt.Errorf("find this program: %w", err)
	}
	sum, err := hashFile(exe)
	if err != nil {
		return "", err
	}
	sums, err := os.ReadFile(filepath.Clean(sumsFile))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", sumsFile, err)
	}
	return pinFromSums(string(sums), sum, sumsFile, d.GOOS)
}

// upgradeColocatedCurrent is an upgrade of a stack that runs the release already: it says so, and sets the executor up as an install does, so that a
// program that was replaced since, or a pin of the release's sums that was given now, reaches the executor.
func upgradeColocatedCurrent(ctx context.Context, r *Resolver, env Env, rt Runtime, s Settings, t installTarget, tokenFile, image, pin string) error {
	d := refreshCopy(ctx, env) // first: the hint names the program as the refreshed copy answers
	r.say("Nothing to upgrade: already on %s. Check the instance with `%s`.\n", digestOf(image), doctorHint(d.command(), t.settingsPath, t.defaultPath))
	env.Deps = d
	refreshExecutor(ctx, env, rt, s, tokenFile, pin)
	return holdPage(r.screen)
}
