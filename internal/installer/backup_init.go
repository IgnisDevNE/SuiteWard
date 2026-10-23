package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/archive"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// storedWord is what the owner types to say that the recovery kit is copied off this machine.
const storedWord = "stored"

// kitWarning is said whenever a kit is written: it is the only way to read a backup.
const kitWarning = "The recovery kit %s holds the private key that opens every backup of this instance. Copy it off this machine now, for example into a password manager, and delete this copy. Without it no backup can be restored, and anyone who has it can read every backup."

// BackupInit is `suiteward backup init [--kit PATH] [--yes] [--replace] [flags]`: it makes the key pair of this instance, writes the
// private half into a recovery kit that the owner keeps off the machine, and stores only the public half in the settings.
func BackupInit(ctx context.Context, args []string, env Env) (err error) {
	if err := env.validate(); err != nil {
		return err
	}
	set := newFlagSet("backup init")
	instanceFlags(set)
	set.String("kit", "", "where the recovery kit is written (default: suiteward-recovery-kit-<instance>.txt in the current directory); a file is never overwritten")
	set.Bool("yes", false, "without a terminal: say that the kit is copied off this machine (a terminal asks you to type "+storedWord+")")
	set.Bool("replace", false, "replace the recovery key that is set: the backups made with it still need its kit")
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
	scr.Steps("Keys", "Kit", "Settings")
	r, err := NewResolver(flags, nil, env.Term)
	if err != nil {
		return err
	}
	inst, err := locateInstance(ctx, r, env, flags)
	if err != nil {
		return err
	}
	if err := inst.needSettings(); err != nil {
		return err
	}
	k := &keyInit{r: r, env: env, inst: &inst, kit: flags["kit"], replace: flags["replace"] == "true", yes: flags["yes"] == "true"}
	if err := k.keys(ctx); err != nil {
		return err
	}
	scr.Step(2)
	if err := k.writeKit(ctx); err != nil {
		return err
	}
	scr.Step(3)
	if err := k.store(); err != nil {
		return err
	}
	k.kept()
	return showSummary(scr, k.summary)
}

// keyInit is the making of one recovery key, in its three steps. The private half is held here until it is in the kit file: it is
// never printed, logged or put in an error.
type keyInit struct {
	r       *Resolver
	env     Env
	inst    *instance
	kit     string // --kit, "" for the default
	replace bool
	yes     bool

	instance  string // the id of the instance, from /status
	recipient string
	identity  string
	replaced  string // the fingerprint of the recipient this one replaces, "" when there was none
}

// keys finds out which instance the key is for and makes the pair.
func (k *keyInit) keys(ctx context.Context) error {
	if old := k.inst.s.BackupRecipient; old != "" {
		if !k.replace {
			return fmt.Errorf("a recovery key is set already (%s): the backups made with it need its kit. To replace it with a new key, run `suiteward backup init --replace`", archive.Fingerprint(old))
		}
		k.replaced = archive.Fingerprint(old)
	}
	_, status, err := k.inst.readStatus(ctx)
	if err != nil {
		return err
	}
	k.instance = status.Instance
	if k.recipient, k.identity, err = archive.NewKey(); err != nil {
		return err
	}
	if k.kit == "" {
		k.kit = "suiteward-recovery-kit-" + k.instance + ".txt"
	}
	if k.kit, err = filepath.Abs(k.kit); err != nil {
		return fmt.Errorf("find the path of the kit: %w", err)
	}
	return nil
}

// writeKit writes the kit and asks the owner to say it is safe. A kit that was not confirmed is removed: nothing opens the backups
// of a key that is stored nowhere, and a re-run must be able to write the file again.
func (k *keyInit) writeKit(ctx context.Context) error {
	text := archive.RecoveryKit(k.identity, k.recipient, k.instance)
	if err := writeRestrictedFile(ctx, k.env.Deps, k.kit, []byte(text)); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return fmt.Errorf("the recovery kit %s exists already and is never overwritten: name another file with --kit, or move that one away", k.kit)
		}
		return err
	}
	k.r.screen.Warn(kitWarning+"\n", k.kit)
	if err := k.confirmStored(); err != nil {
		return errors.Join(err, os.Remove(k.kit))
	}
	return nil
}

// confirmStored is the owner saying that the kit is off this machine: the word on a terminal, --yes where nobody can type it.
func (k *keyInit) confirmStored() error {
	if !k.r.term.IsTerminal {
		if !k.yes {
			return fmt.Errorf("nothing can ask you to type %q here (the input is not a terminal): copy the kit off this machine and run the command again with --yes", storedWord)
		}
		return nil
	}
	line, err := k.r.ask(fmt.Sprintf("Type %q once the kit is copied off this machine: ", storedWord))
	switch {
	case errors.Is(err, io.EOF):
		line = ""
	case err != nil:
		return fmt.Errorf("read the confirmation: %w", err)
	}
	if line != storedWord {
		return fmt.Errorf("you did not type %q, so the key was not stored and the kit was removed: nothing was changed", storedWord)
	}
	return nil
}

// store puts the public half in the settings, which is all the instance keeps. An upgrade that converts a stack with no settings
// file keeps it in memory: the upgrade saves the settings when it is done.
func (k *keyInit) store() error {
	s := k.inst.s
	s.BackupRecipient = k.recipient
	if k.inst.haveSettings {
		if err := SaveSettings(k.inst.settingsPath, s); err != nil {
			return errors.Join(fmt.Errorf("store the recovery key in the settings: %w", err), os.Remove(k.kit))
		}
	}
	k.inst.s = s
	return nil
}

// kept says what the owner must not lose, in lines that outlast the screen.
func (k *keyInit) kept() {
	scr := k.r.screen
	scr.Keep("The recovery kit is in %s. Keep it off this machine: it is the only way to restore a backup.\n", k.kit)
	scr.Keep("Recovery key: %s\n", archive.Fingerprint(k.recipient))
	if k.replaced != "" {
		scr.Keep("The backups made with the key %s still need its kit: keep that one too.\n", k.replaced)
	}
	scr.Say("Backups are now encrypted to this key: `suiteward backup`, and `suiteward install --upgrade`.\n")
}

func (k *keyInit) summary() []ui.KV {
	return []ui.KV{
		{Key: "Instance", Value: k.instance},
		{Key: "Recovery key", Value: archive.Fingerprint(k.recipient)},
		{Key: "Recovery kit", Value: k.kit},
		{Key: "Settings", Value: k.inst.settingsPath},
	}
}

// run is the three steps in a row, for a command that makes the key in the middle of its own work.
func (k *keyInit) run(ctx context.Context) error {
	if err := k.keys(ctx); err != nil {
		return err
	}
	if err := k.writeKit(ctx); err != nil {
		return err
	}
	if err := k.store(); err != nil {
		return err
	}
	k.kept()
	return nil
}
