package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"filippo.io/age"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/archive"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// noKeyError is the refusal of a command that has no key to encrypt a backup to, where nobody can be asked or the answer was no. It names
// what applies to the command that prints it: `suiteward backup` has no --backup-recipient and no answers file, an upgrade has both.
func noKeyError(cmd string, upgrade bool) error {
	refusal := "this instance has no recovery key, so a backup cannot be encrypted (and a backup is never written unencrypted). Run `" + cmd + " backup init` first"
	if upgrade {
		return errors.New(refusal + ", or give the public key (it starts with `age1pq1`) with `--backup-recipient` or the answers-file key `backup_recipient`")
	}
	return errors.New(refusal)
}

// Backup is `suiteward backup [--to DIR] [flags]`: an encrypted backup of the installed instance, which keeps running.
func Backup(ctx context.Context, args []string, env Env) (err error) {
	if err := env.validate(); err != nil {
		return err
	}
	set := newFlagSet("backup")
	instanceFlags(set)
	set.String("to", "", "the folder the backup file is written to (default: a backups folder beside the saved access token file; on a separate server, in ~/.config/suiteward of the service account)")
	flags, err := parse(set, args, env.Term.Out)
	if err != nil {
		return err
	}
	plain, err := wantsPlain(flags, nil)
	if err != nil {
		return err
	}
	scr, env, err := env.openConsole(ctx, plain)
	if err != nil {
		return err
	}
	defer endConsole(scr, &err)
	scr.Steps("Status", "Dump", "Artifacts", "Encrypt")
	r, err := NewResolver(flags, nil, env.Term)
	if err != nil {
		return err
	}
	inst, err := locateInstance(ctx, r, env, flags)
	if err != nil {
		return err
	}
	if err := inst.needSettings(env.Deps.command()); err != nil {
		return err
	}
	// A service that does not answer is refused before a key is offered or anything is written: its status belongs in the backup.
	if _, _, err := inst.readStatus(ctx); err != nil {
		return err
	}
	recipient, err := recipientOf(ctx, r, env, &inst, false)
	if err != nil {
		return err
	}
	dir := r.preset("to", inst.parent)
	if err := prepareBackupDir(dir, scr.Warn); err != nil {
		return backupFailed(err, false)
	}
	run, err := inst.backup(env.Deps, dir, recipient).start(ctx)
	if err != nil {
		return backupFailed(err, false)
	}
	for i, stage := range run.stages() {
		scr.Step(i + 1)
		if err := working(scr, stage.label, func() error { return stage.run(ctx) }); err != nil {
			return backupFailed(run.abort(err), false)
		}
	}
	path, err := run.finish()
	if err != nil {
		return backupFailed(run.abort(err), false)
	}
	scr.Keep("The backup is in %s. It holds the authenticator secret (the TOTP secret), locked with the recovery key %s. Keep the recovery kit off this computer.\n", path, archive.Fingerprint(inst.s.BackupRecipient))
	scr.Keep("To restore it, run `%s restore %s` with that kit.\n", env.Deps.command(), quoteArg(path))
	return showSummary(scr, func() []ui.KV { return backupSummary(path, run.doc, inst.s.BackupRecipient) })
}

func backupSummary(path string, doc statusDoc, recipient string) []ui.KV {
	size := "unknown"
	if info, err := os.Stat(path); err == nil {
		size = fmt.Sprintf("%d bytes", info.Size())
	}
	return []ui.KV{
		{Key: "Backup", Value: path},
		{Key: "Size", Value: size},
		{Key: "Instance ID", Value: doc.Instance},
		{Key: "Version", Value: doc.Version},
		{Key: "Recovery key", Value: archive.Fingerprint(recipient)},
	}
}

// backup is the backup of this instance into dir, encrypted to recipient, as the commands of the backup family and the upgrade take it.
func (i instance) backup(d Deps, dir string, recipient *age.HybridRecipient) backup {
	return backup{
		rt: i.rt, d: d, dir: dir, recipient: recipient, status: i.status, image: i.image, tier: i.s.Tier,
		settings: i.settingsPath, units: i.units, serviceStarted: i.serviceStarted, now: time.Now,
	}
}

// recipientOf is the key a backup is encrypted to: the one in the settings. Without one a terminal is offered a new key, as
// `suiteward backup init` makes it; anything else refuses and names the command. agreed says that the question was asked already, as an
// upgrade asks it before it starts anything: the key is made now, and not asked for again.
func recipientOf(ctx context.Context, r *Resolver, env Env, inst *instance, agreed bool) (*age.HybridRecipient, error) {
	if inst.s.BackupRecipient == "" {
		if !agreed {
			if err := askForKey(env.Deps.command(), r, false); err != nil {
				return nil, err
			}
		}
		if err := (&keyInit{r: r, env: env, inst: inst}).run(ctx); err != nil {
			return nil, err
		}
	}
	return archive.ParseRecipient(inst.s.BackupRecipient)
}

// askForKey asks a terminal whether to make the key. A no, an input that ends and the lack of a terminal are the refusal of noKeyError(upgrade).
func askForKey(cmd string, r *Resolver, upgrade bool) error {
	if !r.term.IsTerminal {
		return noKeyError(cmd, upgrade)
	}
	yes, err := r.askYes("This instance has no recovery key, so a backup cannot be encrypted. Make one now (the same as `" + cmd + " backup init`)?")
	switch {
	case errors.Is(err, io.EOF): // the input ended: no answer is not a yes
		return noKeyError(cmd, upgrade)
	case err != nil:
		return fmt.Errorf("read the answer: %w", err)
	case !yes:
		return noKeyError(cmd, upgrade)
	}
	return nil
}

// askedRecipient applies --backup-recipient, or the answers-file key backup_recipient, to the settings of an upgrade: where nobody can
// be asked, the key may be given, and it is stored with the rest of the settings when the upgrade is done. A key other than the one
// in use is refused: replacing it is `suiteward backup init --replace`, because the backups made with the old one still need its kit.
func askedRecipient(cmd string, r *Resolver, s *Settings) error {
	given := strings.TrimSpace(r.preset("backup-recipient", ""))
	if given == "" {
		return nil
	}
	recipient, err := archive.ParseRecipient(given)
	if err != nil {
		return fmt.Errorf("--backup-recipient (or backup_recipient): %w", err)
	}
	if s.BackupRecipient != "" && s.BackupRecipient != recipient.String() {
		return fmt.Errorf("the settings already hold the recovery key %s, and `--backup-recipient` (or `backup_recipient`) names a different one, %s. To replace the key, run `%s backup init --replace`. Backups made with the old key still need the old kit",
			archive.Fingerprint(s.BackupRecipient), archive.Fingerprint(recipient.String()), cmd)
	}
	s.BackupRecipient = recipient.String()
	return nil
}

// aliasBackupKey lets the answers file spell the recovery key with an underscore, as the GitHub keys can be spelled.
func aliasBackupKey(m map[string]string) {
	if v, ok := m["backup_recipient"]; ok {
		if _, set := m["backup-recipient"]; !set {
			m["backup-recipient"] = v
		}
	}
}
