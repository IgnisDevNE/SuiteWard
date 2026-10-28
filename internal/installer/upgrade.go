package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/archive"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// ErrNothingInstalled is the refusal of --upgrade, and of uninstall without --purge, when there is no stack to work on. Its text is the fact;
// nothingInstalled adds the command that installs, which is named as the run reaches the program.
var ErrNothingInstalled = errors.New("nothing is installed here")

// nothingInstalled is ErrNothingInstalled with the install to run first.
func nothingInstalled(cmd string) error {
	return fmt.Errorf("%w: install first with `%s install`", ErrNothingInstalled, cmd)
}

// upgradeTarget is the release image the upgrade moves to: --image, else the release this binary carries.
func upgradeTarget(r *Resolver) (string, error) {
	asked, ok := r.given("image")
	if !ok {
		embedded, have := ReleaseImage()
		if !have {
			return "", errors.New("this is a development build, so it does not know which release image to install. Pass `--image " + releaseImageRepo + "@sha256:<signed index digest>`")
		}
		return embedded, nil
	}
	return parseImage(asked)
}

// upgradePlan is what an upgrade changes: the release image and the PostgreSQL pin, as installed and as wanted.
type upgradePlan struct{ oldImage, newImage, oldDB, newDB string }

// dbChanges says whether the database moves to another pin. An installed database whose image could not be read is not
// compared.
func (p upgradePlan) dbChanges() bool { return p.oldDB != "" && !sameImage(p.newDB, p.oldDB) }

// current says whether the stack already runs what the upgrade would install.
func (p upgradePlan) current() bool {
	return p.oldImage != "" && sameImage(p.newImage, p.oldImage) && !p.dbChanges()
}

func digestOf(image string) string {
	_, digest, _ := strings.Cut(image, "@")
	return digest
}

func (p upgradePlan) describe() string {
	db := "unchanged"
	if p.dbChanges() {
		db = p.oldDB + " -> " + p.newDB
	}
	return fmt.Sprintf("Upgrade of this instance:\n  SuiteWard   %s -> %s\n  PostgreSQL  %s\n", digestOf(p.oldImage), digestOf(p.newImage), db)
}

// backupParent is where the backup directory is made: --backup-dir, else a backups directory in secretDir. The dump holds the
// TOTP secret, so secretDir is where the instance already keeps a secret (the token copy co-located, the account's own
// configuration directory isolated), never the settings file's directory, which is the roaming profile on Windows.
func backupParent(r *Resolver, secretDir string) string {
	return r.preset("backup-dir", filepath.Join(secretDir, "backups"))
}

// confirmUpgrade shows the plan and asks, unless --yes was given. recipient is the key the backup is encrypted to, "" when a terminal
// is offered a new one. serviceStarted says that the upgrade started a service that was down.
func confirmUpgrade(r *Resolver, plan upgradePlan, parent, recipient string, serviceStarted bool) error {
	r.say("%s", plan.describe())
	key := "a new recovery key, which you agreed to make first"
	if recipient != "" {
		key = "the recovery key " + archive.Fingerprint(recipient)
	}
	r.say("First, an encrypted backup of the database and the saved files (artifacts) is made as a new file under %s, locked with %s.\n", parent, key)
	ok, err := r.confirm("yes", "Back up and upgrade now? If you answer no, nothing is changed.")
	if err != nil {
		return err
	}
	if !ok {
		if serviceStarted {
			return errors.New("the service was started, and nothing else was changed")
		}
		return errors.New("nothing was changed")
	}
	return nil
}

// askYes asks a question that only the person at the terminal answers (a flag, the answers file and --yes never do) and whose default is yes:
// on a full screen a choice between Yes and No with Yes selected, on a plain one the typed question with [Y/n]. An answer that is not a yes
// is a no, and the end of the input is an error that wraps io.EOF: the caller refuses on both.
func (r *Resolver) askYes(question string) (bool, error) {
	if r.screen.FullScreen() {
		picked, err := r.screen.Select(question, yesNo, 0)
		return picked == 0 && err == nil, err
	}
	answer, err := r.ask(question + " [Y/n]: ")
	if err != nil {
		return false, err
	}
	if answer == "" {
		return true, nil
	}
	yes, err := parseYesNo("answer", answer)
	return err == nil && yes, nil
}

func doctorHint(cmd, settingsPath string, defaultPath bool) string {
	hint := cmd + " doctor"
	if !defaultPath {
		hint += " --settings " + quoteArg(settingsPath)
	}
	return hint
}

// installedDBImage is the image the database container runs now.
func installedDBImage(ctx context.Context, rt Runtime) (string, error) {
	raw, err := rt.Inspect(ctx, ContainerDB)
	if err != nil {
		return "", err
	}
	in, err := parseInspect(ContainerDB, raw)
	if err != nil {
		return "", err
	}
	return in.Config.Image, nil
}

// readStatus is GET /status of the running service, from inside the Podman machine when its port is published there.
func (c *colocatedInstall) readStatus(ctx context.Context) ([]byte, error) {
	address := "http://127.0.0.1:" + strconv.Itoa(c.s.HTTPPort) + "/status"
	if !c.inMachine() {
		return c.env.httpGet()(ctx, address)
	}
	user, err := c.lookUpMachineUser(ctx)
	if err != nil {
		return nil, err
	}
	return c.env.Deps.Exec(ctx, "podman", []string{"machine", "ssh", "--username", user, c.s.Connection, "curl -fsS -m 5 " + address}, nil)
}

// upgradeColocated is `suiteward install --upgrade` for the co-located tier: adopt the stack (a difference stops the run before
// any change), show what changes and ask, back up, pull, then the same bring-up as the install: migrate must exit 0 before the
// service starts. The volumes, the secrets and the settings the stack has are kept.
func upgradeColocated(ctx context.Context, r *Resolver, env Env, t installTarget) error {
	scr := r.screen
	scr.Steps("Plan", "Backup", "Images", "Replace", "Check")
	image, err := upgradeTarget(r)
	if err != nil {
		return err
	}
	rt, connection, err := openRuntime(ctx, r, env, t.saved, false)
	if err != nil {
		return err
	}
	if err := guardDefaultSettings(t, rt.Name(), connection); err != nil {
		return err
	}
	tokenFile, err := instanceTokenFile(r, env, t) // before any change: a second instance names its own, or the executor of the main one is touched
	if err != nil {
		return err
	}
	base, previous, err := existingStackFor(ctx, rt, t.saved, t.haveSaved, true)
	if err != nil {
		return err
	}
	if !previous.Known {
		return nothingInstalled(env.Deps.command())
	}
	if err := refuseUnfinishedInstall(env.Deps.command(), pendingAppPath(t.settingsPath), func() (GitHub, error) { return base.GitHub, nil }); err != nil {
		return err
	}
	s := base
	s.Tier, s.Runtime, s.Connection, s.Image = config.TierCoLocated, rt.Name(), connection, image
	if err := s.Validate(); err != nil {
		return err
	}
	// An upgrade keeps the installed ports and job timeout: a value asked for that is invalid, or differs, stops the run before any
	// change (even a run with nothing to upgrade), so that it is never ignored in silence. A re-run of the install applies it.
	asked, err := askedSettings(r, s)
	if err != nil {
		return err
	}
	if err := refuseChangedSettings(asked, s, installCommand(env.Deps.command(), r, false)); err != nil {
		return err
	}
	dbImage, err := installedDBImage(ctx, rt)
	if err != nil {
		return err
	}
	postgres, err := PostgresImage()
	if err != nil {
		return err
	}
	pin, err := releasePin(r, env.Deps) // before anything is changed, a run with nothing to upgrade included
	if err != nil {
		return err
	}
	plan := upgradePlan{oldImage: previous.Image, newImage: image, oldDB: dbImage, newDB: postgres}
	// A stack without a settings file (made by local.ps1) is converted even when it runs this release: the settings are saved.
	if plan.current() && t.haveSaved {
		return upgradeColocatedCurrent(ctx, r, env, rt, s, t, tokenFile, image, pin)
	}
	// The key is settled before anything starts, a service that is down included: a key that is refused never leaves one started.
	agreed, err := settleRecipient(r, env, &s)
	if err != nil {
		return err
	}
	c := &colocatedInstall{githubInstall: githubInstall{rt: rt, env: env, out: env.Term.Out, s: s}, image: image, previous: previous, tokenFile: tokenFile, executorPin: pin}
	if c.secrets, err = c.readSecretState(ctx); err != nil {
		return err
	}
	engine, err := engineCommand(rt, env.Deps.GOOS)
	if err != nil {
		return err
	}
	startCommand := fmt.Sprintf("`%s start %s`, then, once it is healthy, `%s start %s`", engine, ContainerDB, engine, ContainerServe)
	started, err := ensureServiceUp(ctx, r, env, c.readStatus, c.startInstalled, startCommand)
	if err != nil {
		return err
	}
	parent := backupParent(r, filepath.Dir(c.tokenFile))
	if err := confirmUpgrade(r, plan, parent, s.BackupRecipient, started); err != nil {
		return err
	}
	scr.Step(2)
	stored := s
	stored.Image = previous.Image // what the settings file says until the upgrade is done: a key made now is saved beside it
	inst := instance{rt: rt, s: stored, settingsPath: t.settingsPath, haveSettings: t.haveSaved, image: previous.Image, status: c.readStatus, startCommand: startCommand, serviceStarted: started}
	dir, err := upgradeBackup(ctx, r, env, &inst, parent, nil, agreed)
	if err != nil {
		return err
	}
	c.s.BackupRecipient = inst.s.BackupRecipient
	c.say("Backup written to %s.\n", dir)
	scr.Step(3)
	if err := c.pullImages(ctx); err != nil {
		return fmt.Errorf("%w (%s; the backup is in %s)", err, inst.changed(), dir)
	}
	if c.stack, err = ColocatedStack(c.s, image); err != nil {
		return withBackup(env.Deps.command(), err, dir, "", "")
	}
	scr.Step(4)
	if err := c.bringUp(ctx, func() error { return SaveSettings(t.settingsPath, c.s) }); err != nil {
		return withBackup(env.Deps.command(), err, dir, "", "")
	}
	scr.Step(5)
	c.restartAfterReboot(ctx)
	c.env.Deps = refreshCopy(ctx, c.env) // before the hints below, which name the program as the refreshed copy answers
	c.setUpExecutor(ctx)                 // after it: what it says names the program as the refreshed copy answers
	cmd := c.env.Deps.command()
	upgraded(scr, image, dir, goBackColocated(cmd, engine, previous.Image, dir, scopeFlags(r, false)), c.doctorAgain(t))
	c.installSelf(ctx)
	return showSummary(scr, func() []ui.KV { return c.upgradeSummary(ctx, t) })
}

// settleRecipient settles the key the backup of an upgrade is encrypted to, from the settings and what the command was given, before
// the upgrade starts anything, a service that is down included. Without one, an upgrade that has no terminal is refused, and a terminal
// is asked whether to make one: a no ends the run here, and a yes (agreed) is acted on once the service answers, because the instance id of
// /status names the kit. The key is stored with the settings when the upgrade is done.
func settleRecipient(r *Resolver, env Env, s *Settings) (agreed bool, err error) {
	if err := askedRecipient(env.Deps.command(), r, s); err != nil {
		return false, err
	}
	if s.BackupRecipient != "" {
		return false, nil
	}
	if !env.Term.IsTerminal {
		return false, noKeyError(env.Deps.command(), true)
	}
	if err := askForKey(env.Deps.command(), r, true); err != nil {
		return false, err
	}
	return true, nil
}

// upgradeBackup is the Backup step of an upgrade: the key (the settings', or the new one that the terminal agreed to), then the encrypted
// backup under one spinner. It returns the archive.
func upgradeBackup(ctx context.Context, r *Resolver, env Env, inst *instance, dir string, beside func(archive string) error, agreed bool) (string, error) {
	recipient, err := recipientOf(ctx, r, env, inst, agreed)
	if err != nil {
		return "", err
	}
	if err := prepareBackupDir(dir, r.screen.Warn); err != nil {
		return "", backupFailed(err, inst.serviceStarted)
	}
	b := inst.backup(env.Deps, dir, recipient)
	b.beside = beside
	var path string
	err = working(r.screen, "Backing up the database and the artifacts", func() (err error) {
		path, err = takeBackup(ctx, b)
		return err
	})
	return path, err
}

// upgraded prints the end of a successful upgrade: where the backup is, how to go back and what to run next. The two lines about the
// backup are kept, so that they are still there when the screen has gone.
func upgraded(scr *ui.Screen, image, backupPath, goBack, doctor string) {
	scr.Say("Upgraded to %s.\n", digestOf(image))
	scr.Keep("The backup is in %s. It holds the TOTP secret (behind your authenticator codes), locked with your recovery key. Keep the recovery kit off this computer.\n", backupPath)
	scr.Keep("%s\n", goBack)
	scr.Say("Check the instance with `%s`.\n", doctor)
}

// goingBack is where the steps of the way back after an upgrade are written down.
const goingBack = "The steps are in docs/backup.md, under Going back after an upgrade."

// goBackColocated is the way back after the upgrade of a co-located stack: a restore alone cannot undo an upgrade that migrated, so the stack
// and its data volumes go first, and the previous release is installed before the backup is restored.
func goBackColocated(cmd, engine, previousImage, backupPath, scope string) string {
	return fmt.Sprintf("To go back, remove the installation and its data volumes (`%[8]s uninstall --yes%[1]s`, then `%[2]s volume rm %[3]s %[4]s`), install the previous release (`%[8]s install --image %[5]s%[1]s`), then run `%[8]s restore %[6]s%[1]s` with that kit. It must be the previous release, because database updates only go forward. %[7]s",
		scope, engine, VolumeData, VolumeDBData, previousImage, quoteArg(backupPath), goingBack, cmd)
}

// goBackIsolated is the way back after the upgrade of an isolated host: the unit files that the upgrade replaced were kept beside the backup;
// they go back, systemd reloads them and the service stops, and then the backup is restored.
func goBackIsolated(cmd, backupPath, unitsDir, scope string) string {
	return fmt.Sprintf("To go back, copy the unit files that this upgrade replaced from %s to %s, then run `systemctl --user daemon-reload` and `systemctl --user stop suiteward.service`, and then `%s restore %s%s` with that kit. The units must be the previous release's, because database updates only go forward. %s",
		unitsKeptBeside(backupPath), unitsDir, cmd, quoteArg(backupPath), scope, goingBack)
}

// scopeFlags names the tier and the settings file of this run when they are not the defaults, for a command that a message tells the person
// to run next: copied on a PC with a second instance, it must reach this one (the command line of this run, as enrollCommand takes it).
func scopeFlags(r *Resolver, isolated bool) string {
	flags := ""
	if isolated {
		flags += " --tier isolated"
	}
	if path, ok := r.flags["settings"]; ok {
		flags += " --settings " + quoteArg(path)
	}
	return flags
}

// The wait for /status of a service the upgrade started: at most startWaitTries probes, and a deadline on the whole wait, because a
// probe through the Podman machine can take seconds itself.
const (
	startWaitTries = 60
	startWait      = 90 * time.Second
)

// startServicePrompt is what an upgrade asks of a service that does not answer /status, which the backup records.
const startServicePrompt = "The service is not running, so the backup cannot record its status. Start the installed service now? If you answer no, the upgrade stops and nothing is changed."

// ensureServiceUp makes sure /status answers before the backup. A service that does not is started as it is installed, on a terminal
// and after a yes; otherwise, or when the start does not help, the upgrade refuses. --yes confirms the upgrade plan only: nothing
// here reads it. started says that this call started the service.
func ensureServiceUp(ctx context.Context, r *Resolver, env Env, status func(context.Context) ([]byte, error), start func(context.Context) error, startCommand string) (started bool, err error) {
	if _, err := status(ctx); err == nil {
		return false, nil
	}
	refuse := fmt.Errorf("the service is not running, so the backup cannot record its status. Start it with %s, then run the upgrade again. `--yes` only confirms the upgrade plan: it does not start the service. Nothing was changed", startCommand)
	if !r.term.IsTerminal {
		return false, refuse
	}
	yes, err := r.askYes(startServicePrompt)
	switch {
	case errors.Is(err, io.EOF): // the input ended: no answer is not a yes
		return false, refuse
	case err != nil:
		return false, fmt.Errorf("could not read your answer: %w", err)
	case !yes:
		return false, refuse
	}
	const manual = "take the backup by hand instead (dump the database and copy the artifacts volume, the saved files, yourself; details: docs/backup.md, Appendix A), then upgrade"
	if err := start(ctx); err != nil {
		return false, fmt.Errorf("could not start the installed service: %w; %s", err, manual)
	}
	waitCtx, cancel := context.WithTimeout(ctx, startWait)
	defer cancel()
	bar := barOn(r.screen, "Service")
	var lastErr error
	for i := range startWaitTries {
		if _, lastErr = status(waitCtx); lastErr == nil || waitCtx.Err() != nil {
			break
		}
		bar.set(float64(i)/startWaitTries, "waiting for /status")
		if i < startWaitTries-1 {
			env.Sleep(statusEvery)
		}
	}
	if lastErr != nil {
		return true, fmt.Errorf("the service was started, but it still does not answer on /status (%w). Read its logs; or %s", lastErr, manual)
	}
	bar.set(1, "answers")
	return true, nil
}

// startInstalled starts the containers the stack has, the database first and only once it is healthy: nothing is created.
func (c *colocatedInstall) startInstalled(ctx context.Context) error {
	if err := c.rt.Start(ctx, ContainerDB); err != nil {
		return err
	}
	if err := c.waitHealthy(ctx); err != nil {
		return err
	}
	return c.rt.Start(ctx, ContainerServe)
}

// refuseUnfinishedInstall refuses an upgrade while an earlier install is unfinished: the App it recorded holds the key secret, and the
// upgrade would start the stack again with the App of the settings, which GitHub refuses with that key. settings is called only when
// a record exists.
func refuseUnfinishedInstall(cmd, pendingPath string, settings func() (GitHub, error)) error {
	pending, found, err := loadPendingApp(pendingPath)
	if err != nil || !found {
		return err
	}
	gh, err := settings()
	if err != nil {
		return err
	}
	if !recordWins(&gh, &pending) {
		return nil
	}
	return fmt.Errorf("an earlier install did not finish: it created the GitHub App %s and recorded it in %s, and that App's key is stored as the secret %s. Finish that install first by running `%s install` (it picks up the recorded App), then upgrade. Nothing was changed",
		pending.Slug, pendingPath, SecretGitHubAppKey, cmd)
}
