package installer

import (
	"bytes"
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
		if sid, err = d.UserSID(); err != nil || !strings.HasPrefix(sid, "S-1-") {
			return plan, fmt.Errorf("find the SID of this user: %v", err)
		}
	}
	sum, err := hashFile(exe)
	if err != nil {
		return plan, err
	}
	_, running, err := plan.state(ctx, d)
	if err != nil {
		return plan, err
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
	if err := writeFileAtomic(plan.pin, []byte(sum+"  "+executorPinName+"\n"), 0o700); err != nil {
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
		scr.Say("The executor runs its sandboxes through Docker's own engine: a docker command that reaches Podman's socket (its docker shim, for one) fails closed, because it does not know the flags the sandbox needs. Use --runtime podman for Podman.\n")
	}
	scr.Say("The executor holds one credential, its own token (%s), and it reaches the service at http://127.0.0.1:%d. It runs as your user, next to your coding agent, so its assurance is lower than on a separate host: what doctor pins catches drift and accident, not a determined agent.\n", plan.tokenFile, c.s.HTTPPort)
	scr.Say("Keep one executor for each container engine: at start it removes every %s container of its engine, so a second one (the executor started by hand, say) would remove the sandboxes of the first.\n", sandboxNamePrefix)
}

// removeExecutor stops the executor, takes it out of the operating system and deletes its files, the token copy apart (a purge removes that).
// existed says that there was something of it to remove.
func removeExecutor(ctx context.Context, d Deps, tokenFile string) (existed bool, err error) {
	plan, err := planExecutor(d, tokenFile, Settings{Runtime: RuntimePodman}, "")
	if err != nil {
		return false, err
	}
	registered, _, err := plan.state(ctx, d)
	if err != nil {
		return false, err
	}
	if !registered && !pathExists(plan.dir) {
		return false, nil
	}
	return true, plan.remove(ctx, d, registered)
}

// pathExists says whether something is at the path.
func pathExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}
