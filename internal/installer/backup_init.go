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
const kitWarning = "The recovery kit %s holds the private key that opens every backup of this instance. Copy it off this computer now, for example into a password manager, and delete this copy. Without it no backup can be restored, and anyone who has it can read every backup."

// BackupInit is `suiteward backup init [--kit PATH] [--yes] [--replace] [flags]`: it makes the key pair of this instance, writes the
// private half into a recovery kit that the owner keeps off the machine, and stores only the public half in the settings.
func BackupInit(ctx context.Context, args []string, env Env) (err error) {
	if err := env.validate(); err != nil {
		return err
	}
	set := newFlagSet("backup init")
	instanceFlags(set)
	set.String("kit", "", "where the recovery kit is written (default: suiteward-recovery-kit-<instance>.txt in the current directory); a file is never overwritten")
	set.Bool("yes", false, "confirm that the kit is copied off this computer, when no one can type an answer (on a terminal you type "+storedWord+" instead)")
	set.Bool("replace", false, "replace the recovery key that is set (backups made with it still need its kit)")
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
	if err := inst.needSettings(env.Deps.command()); err != nil {
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
			return fmt.Errorf("this instance already has a recovery key (%s), and backups made with it need its kit. To replace it with a new key, run `%s backup init --replace`", archive.Fingerprint(old), k.env.Deps.command())
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
			return fmt.Errorf("the recovery kit file %s already exists and is never overwritten. Name another file with `--kit`, or move that one away", k.kit)
		}
		return err
	}
	if k.r.term.IsTerminal { // the typed word below is what the page explains; with --yes nobody is asked
		k.r.screen.Page("Keep the recovery kit",
			ui.Para("This is a confirmation: SuiteWard cannot see whether you copied the kit, so type "+storedWord+" only once it is off this computer."))
	}
	k.r.screen.Critical(kitWarning+"\n", k.kit) // the most important text of this command: a red box on a full screen, the Warn it was in plain mode
	if err := k.confirmStored(); err != nil {
		return errors.Join(err, os.Remove(k.kit))
	}
	return nil
}

// confirmStored is the owner saying that the kit is off this machine: the word on a terminal, --yes where nobody can type it.
func (k *keyInit) confirmStored() error {
	if !k.r.term.IsTerminal {
		if !k.yes {
			return fmt.Errorf("nothing can ask you to type %q here, because the input is not a terminal. Copy the kit off this computer, then run the command again with `--yes`", storedWord)
		}
		return nil
	}
	prompt := fmt.Sprintf("Type %q once the kit is copied off this computer: ", storedWord)
	if k.r.screen.FullScreen() { // the page above says that the word is a confirmation
		prompt = fmt.Sprintf("Type %q to confirm that the kit is copied off this computer: ", storedWord)
	}
	line, err := k.r.ask(prompt)
	switch {
	case errors.Is(err, io.EOF):
		line = ""
	case err != nil:
		return fmt.Errorf("read the confirmation: %w", err)
	}
	if line != storedWord {
		return fmt.Errorf("you did not type %q, so the key was not saved and the kit file was removed: %s", storedWord, k.inst.changed())
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
	scr.Keep("The recovery kit is in %s. Keep it off this computer: without it no backup can be restored.\n", k.kit)
	scr.Keep("Recovery key: %s\n", archive.Fingerprint(k.recipient))
	if k.replaced != "" {
		scr.Keep("The backups made with the key %s still need its kit: keep that one too.\n", k.replaced)
	}
	scr.Say("From now on, `%s backup` and `%s install --upgrade` lock their backups with this key.\n", k.env.Deps.command(), k.env.Deps.command())
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
