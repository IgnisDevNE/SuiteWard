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

// errProcessesNotListed is why a first install on Windows sets up no test runner: the listing of the programs that run fails, so the installer
// could never stop or update one. Running the install again changes nothing, and setUpExecutor leaves that advice out.
var errProcessesNotListed = errors.New("SuiteWard could not list the programs running on this computer (PowerShell may be restricted here). Without that list it could never stop or update the test runner, so it did not install it. No test runner files were written. If your organization restricts PowerShell, ask your administrator, or install SuiteWard on a separate server")

// installExecutor makes the executor of this computer as the settings and this program say: its copy of the program and the pin beside it, the
// definition that the operating system reads, and the start. What is already as it should be is left alone: an executor that runs, with the same
// program and the same definition, is not stopped, because it may be in the middle of a job. A program that is replaced, or a definition that
// changes, stops it first (a program that runs cannot be replaced on Windows) and starts it again.
func (c *colocatedInstall) installExecutor(ctx context.Context) (executorPlan, error) {
	d := c.env.Deps
	if d.Executable == nil {
		return executorPlan{}, errors.New("the installer cannot find its own program, so it cannot copy it for the test runner")
	}
	exe, err := d.Executable()
	if err != nil {
		return executorPlan{}, fmt.Errorf("could not find the installer's own program: %w", err)
	}
	plan, err := planExecutor(d, c.tokenFile, c.s, engineBinDir(c.rt))
	if err != nil {
		return executorPlan{}, err
	}
	sid, err := windowsSID(d)
	if err != nil {
		return plan, err
	}
	sum, err := hashFile(exe)
	if err != nil {
		return plan, err
	}
	if !pathExists(plan.tokenFile) {
		return plan, fmt.Errorf("the test runner's token file `%s` is missing, so the test runner could not start. Run `%s`: it creates the token file and gives the service the same token", plan.tokenFile, d.suggest("install"))
	}
	registered, running, err := plan.state(ctx, d)
	if err != nil {
		return plan, err
	}
	if other, err := plan.foreign(registered); err != nil {
		return plan, err
	} else if other {
		return plan, fmt.Errorf("a computer has one test runner, and yours belongs to another instance. This instance left it as it is. To use this instance's own, remove the other instance with %s, then run `%s` again", d.removeOtherInstance(), d.suggest("install"))
	}
	// state lists the programs when a task is there; a first install has not yet. An executor that the installer can never list again can never be
	// updated or removed by it (PowerShell in constrained language mode), so it is not set up.
	if plan.goos == "windows" && !registered {
		if _, err := plan.listProcesses(ctx, d); err != nil {
			return plan, fmt.Errorf("%w. Detail: %w", errProcessesNotListed, err)
		}
	}
	if err := os.MkdirAll(plan.dir, 0o700); err != nil {
		return plan, fmt.Errorf("create %s: %w", plan.dir, err)
	}
	have, _ := hashFile(plan.binary) // a copy that is not there yet has no hash, and is copied
	replace := have != sum
	files := plan.definitionFiles(sid)
	changed := replace || filesDiffer(files)
	// A stop that leaves a process fails here, before anything is replaced.
	if plan.mustStop(registered, running, changed) {
		if err := plan.stop(ctx, d); err != nil {
			return plan, err
		}
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

// windowsSID is the SID of this user, which the scheduled task of Windows names; the other systems have none.
func windowsSID(d Deps) (string, error) {
	if d.GOOS != "windows" {
		return "", nil
	}
	if d.UserSID == nil {
		return "", errors.New("the installer cannot find your Windows user ID (SID), which the scheduled task needs")
	}
	sid, err := d.UserSID()
	if err != nil {
		return "", fmt.Errorf("could not find the Windows user ID (SID) of this user: %w", err)
	}
	if !strings.HasPrefix(sid, "S-1-") {
		return "", fmt.Errorf("could not find the Windows user ID (SID) of this user: %q is not one", sid)
	}
	return sid, nil
}

// filesDiffer says whether a file is missing or holds other bytes than the content that is meant for it.
func filesDiffer(files map[string][]byte) bool {
	for path, content := range files {
		if on, err := os.ReadFile(filepath.Clean(path)); err != nil || !bytes.Equal(on, content) {
			return true
		}
	}
	return false
}

// setUpExecutor installs or refreshes the executor after the stack is up. The stack does not depend on it, so a failure is a warning that says
// what to do, as the restart service's is, and doctor reports what is missing.
func (c *colocatedInstall) setUpExecutor(ctx context.Context) {
	scr := screenOn(c.out)
	plan, err := c.installExecutor(ctx)
	d := c.env.Deps
	if err != nil {
		c.executorRow = "not set up: " + oneLine(err.Error())
		fix := " Fix the cause, then run `" + d.suggest("install") + "` again."
		if errors.Is(err, errProcessesNotListed) {
			fix = "" // the install cannot fix a restricted PowerShell, and the error says who can
		}
		scr.Warn("Note: the test runner (the executor) could not be set up: %v. SuiteWard itself runs, but it cannot run tests until the test runner works.%s `%s` lists what is missing.\n", err, fix, d.suggest("doctor"))
		return
	}
	c.executorRow = executorMechanism(plan.goos) + "; " + plan.binary
	scr.Check("The test runner (the executor) is set up as %s. It runs a copy of this program: %s.\n", executorMechanism(plan.goos), plan.binary)
	if plan.goos == "windows" {
		scr.Say("The test runner has no window. It runs in the background, and if it stops it starts again after about %d seconds.\n", executorRestartSeconds)
	}
	if c.s.Runtime == RuntimeDocker {
		scr.Say("The test runner starts its sandboxes through Docker. Docker's own command line cannot drive Podman: it stops, because it does not know the options the sandboxes need. Podman's docker command (the podman-docker package) does work with Podman. To use Podman, install with `--runtime podman`.\n")
	}
	scr.Say("The test runner has one credential, its own token (%s), and it reaches SuiteWard at http://127.0.0.1:%d.\n", plan.tokenFile, c.s.HTTPPort)
	scr.Say("Keep only one test runner (the executor) for each container program. When it starts, it removes every container of its container program whose name starts with %s, so a second test runner (one started by hand, say) would remove the sandboxes of the first.\n", sandboxNamePrefix)
	if c.executorPin == "" {
		scr.Say("The test runner has not been checked against an official release. This is normal for a development build. To check it, run `%s` with the program from that release.\n", d.suggest("install", "--upgrade --sha256sums <the release's SHA256SUMS file>"))
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
		// Not a kill by name: the wrapper starts the program again after its pause, and a name would also end another instance's executor. With the
		// task deleted, nothing starts it at the next sign-in; signing out ends everything of the user that still runs.
		return fmt.Sprintf("run `schtasks /End /TN \"%[1]s\"`, then `schtasks /Delete /TN \"%[1]s\" /F`, sign out of Windows and sign in again to end what still runs, then delete that folder", executorTaskName)
	case "darwin":
		return fmt.Sprintf("run `launchctl bootout gui/$(id -u)/%[1]s` to stop it, then delete that folder and `~/Library/LaunchAgents/%[1]s.plist`", executorLabel)
	default:
		return fmt.Sprintf("run `systemctl --user disable --now %s` where your user session runs to stop it, then delete that folder and its unit file", executorUnitName)
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
		return "", fmt.Errorf("`--settings` names `%[1]s`, which is not the default settings file, so this is a second instance. Without `--token-file`, this command would use the default token file `%[2]s`, which belongs to the main instance (its test runner is next to it). Nothing was changed. Pass `--token-file` with a file of this instance's own. If this is the only instance on this computer and `%[2]s` is its token file, pass `--token-file %[3]s`", t.settingsPath, file, quoteArg(file))
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
		return "", fmt.Errorf("%w: `--sha256sums` is empty (an unset variable?). Give the release's SHA256SUMS file, or leave the flag out", ErrUsage)
	}
	if d.Executable == nil {
		return "", errors.New("the installer cannot find its own program, so it cannot check it against the SHA256SUMS file")
	}
	exe, err := d.Executable()
	if err != nil {
		return "", fmt.Errorf("could not find the installer's own program: %w", err)
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
func upgradeColocatedCurrent(ctx context.Context, r *Resolver, env Env, rt Runtime, s Settings, tokenFile, image, pin string) error {
	d := refreshCopy(ctx, env) // first: the hint names the program as the refreshed copy answers
	r.say("Nothing to upgrade: already on %s. Check the instance with `%s`.\n", digestOf(image), d.suggest("doctor"))
	env.Deps = d
	refreshExecutor(ctx, env, rt, s, tokenFile, pin)
	return holdPage(r.screen)
}
