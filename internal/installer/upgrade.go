package installer

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// ErrNothingInstalled is the refusal of --upgrade, and of uninstall without --purge, when there is no stack to work on.
var ErrNothingInstalled = errors.New("nothing is installed here: install first with `suiteward install`")

// upgradeTarget is the release image the upgrade moves to: --image, else the release this binary carries.
func upgradeTarget(r *Resolver) (string, error) {
	asked, ok := r.optional("image")
	if !ok {
		embedded, have := ReleaseImage()
		if !have {
			return "", errors.New("this is a development build and carries no release image: pass --image " + releaseImageRepo + "@sha256:<signed index digest>")
		}
		return embedded, nil
	}
	if strings.HasPrefix(asked, "sha256:") {
		asked = releaseImageRepo + "@" + asked
	}
	if !digestImage.MatchString(asked) {
		return "", fmt.Errorf("%w: %s", ErrNotDigestImage, asked)
	}
	return asked, nil
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

// confirmUpgrade shows the plan and asks, unless --yes was given.
func confirmUpgrade(r *Resolver, plan upgradePlan, parent string) error {
	r.say("%s", plan.describe())
	r.say("A backup of the database and the artifacts is made first, in a new directory under %s.\n", parent)
	ok, err := r.confirm("yes", "Back up and upgrade now?")
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("nothing was changed")
	}
	return nil
}

func doctorHint(settingsPath string, defaultPath bool) string {
	hint := "suiteward doctor"
	if !defaultPath {
		hint += " --settings " + settingsPath
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
	base, previous, err := existingStackFor(ctx, rt, t.saved, t.haveSaved, true)
	if err != nil {
		return err
	}
	if !previous.Known {
		return ErrNothingInstalled
	}
	dbImage, err := installedDBImage(ctx, rt)
	if err != nil {
		return err
	}
	postgres, err := PostgresImage()
	if err != nil {
		return err
	}
	plan := upgradePlan{oldImage: previous.Image, newImage: image, oldDB: dbImage, newDB: postgres}
	doctor := doctorHint(t.settingsPath, t.defaultPath)
	// A stack without a settings file (made by local.ps1) is converted even when it runs this release: the settings are saved.
	if plan.current() && t.haveSaved {
		r.say("Nothing to upgrade: already on %s. Check the instance with `%s`.\n", digestOf(image), doctor)
		return nil
	}
	s := base
	s.Tier, s.Runtime, s.Connection, s.Image = config.TierCoLocated, rt.Name(), connection, image
	if err := s.Validate(); err != nil {
		return err
	}
	c := &colocatedInstall{rt: rt, env: env, out: env.Term.Out, s: s, image: image, previous: previous, tokenFile: r.preset("token-file", "")}
	if c.tokenFile == "" {
		if c.tokenFile, err = defaultTokenFile(env.Deps); err != nil {
			return err
		}
	}
	if c.secrets, err = c.readSecretState(ctx); err != nil {
		return err
	}
	parent := backupParent(r, filepath.Dir(c.tokenFile))
	if err := confirmUpgrade(r, plan, parent); err != nil {
		return err
	}
	dir, err := takeBackup(ctx, backup{rt: rt, d: env.Deps, parent: parent, status: c.readStatus, now: time.Now})
	if err != nil {
		return err
	}
	c.say("Backup written to %s.\n", dir)
	if err := c.pullImages(ctx); err != nil {
		return fmt.Errorf("%w (nothing was changed; the backup is in %s)", err, dir)
	}
	if c.stack, err = ColocatedStack(c.s, image); err != nil {
		return withBackup(err, dir)
	}
	if err := c.bringUp(ctx, func() error { return SaveSettings(t.settingsPath, c.s) }); err != nil {
		return withBackup(err, dir)
	}
	c.restartAfterReboot(ctx)
	upgraded(c.say, previous.Image, image, dir, doctor)
	return nil
}

// upgraded prints the end of a successful upgrade: where the backup is, how to go back and what to run next.
func upgraded(say func(string, ...any), previous, image, backupDir, doctor string) {
	say("Upgraded to %s.\n", digestOf(image))
	say("The backup is in %s. It holds the TOTP secret: keep it offline and private.\n", backupDir)
	say("To go back, restore that backup with the previous image %s (docs/install.md, section 11.6; migrations only go forward).\n", digestOf(previous))
	say("Check the instance with `%s`.\n", doctor)
}
