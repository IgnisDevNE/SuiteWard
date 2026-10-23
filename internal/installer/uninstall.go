package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// Uninstall is `suiteward uninstall [--purge] [--yes] [--instance ID]` for the co-located tier: it removes the three containers and keeps
// the state, or with --purge also the volumes, the secrets, the network, the MCP token copy and the settings file. It says which instance
// it is about to remove and asks first (a purge, which deletes the database, asks for the instance id, as a restore does), and it never
// touches an instance that is not this one: the guards of the install apply, and so does the settings file: one that is named must exist,
// and with none the engine must be named, so that a purge never runs on defaults that were not read from a settings file.
func Uninstall(ctx context.Context, args []string, env Env) (err error) {
	if err := env.validate(); err != nil {
		return err
	}
	fs := newFlagSet("uninstall")
	for _, f := range installFlags {
		switch f.name {
		case "settings", "token-file", "tier", "runtime", "connection":
			fs.String(f.name, "", f.usage)
		}
	}
	fs.Bool("purge", false, "also remove the volumes (the database!), the secrets, the network, the MCP token copy and the settings file; it needs --instance, or asks you to type the instance id")
	fs.Bool("yes", false, "do not ask before removing (with --purge, give --instance too)")
	fs.String("instance", "", "with --purge --yes: the instance id that /status shows (the first 8 characters of the instance id; unknown when nothing names it)")
	fs.Bool("plain", false, plainUsage)
	flags, err := parse(fs, args, env.Term.Out)
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
	scr.Steps("Confirm", "Remove")
	r, err := NewResolver(flags, nil, env.Term)
	if err != nil {
		return err
	}
	settingsPath, defaultPath, err := settingsPathFrom(flags)
	if err != nil {
		return err
	}
	saved, haveSaved, err := loadSaved(settingsPath, !defaultPath)
	if err != nil {
		return err
	}
	purge := flags["purge"] == "true"
	if config.Tier(r.preset("tier", string(saved.Tier))) == config.TierIsolated {
		isolated := saved
		isolated.Tier, isolated.Runtime = config.TierIsolated, RuntimePodman
		if err := uninstallIsolated(ctx, isolated, r, env, purge); err != nil {
			return err
		}
		return holdPage(scr)
	}
	if !haveSaved {
		if err := engineNamed(flags, settingsPath); err != nil {
			return err
		}
	}
	rt, connection, err := openRuntime(ctx, r, env, saved, false)
	if err != nil {
		return err
	}
	if err := guardDefaultSettings(installTarget{settingsPath: settingsPath, defaultPath: defaultPath, saved: saved, haveSaved: haveSaved}, rt.Name(), connection); err != nil {
		return err
	}
	// The same guard as the install: a stack that is not this installer's, or that was edited, is left alone.
	base, _, err := existingStack(ctx, rt, saved, haveSaved)
	if err != nil {
		return err
	}
	base.Connection = connection
	service := &colocatedInstall{githubInstall: githubInstall{rt: rt, env: env, out: env.Term.Out, s: base}}
	question := "Remove the containers " + ContainerServe + ", " + ContainerMigrate + " and " + ContainerDB + "? The data stays."
	if purge {
		question = "Remove the containers, the volumes (this deletes the database and the artifacts), the secrets, the network, the MCP token copy and the settings file?"
	}
	inst := instance{rt: rt, settingsPath: settingsPath, haveSettings: haveSaved, status: service.readStatus}
	if err := inst.confirmRemoval(ctx, r, purge, question); err != nil {
		return err
	}
	scr.Step(2)
	for _, name := range []string{ContainerServe, ContainerMigrate, ContainerDB} {
		if err := working(scr, "Stopping "+name, func() error { return rt.Remove(ctx, name) }); err != nil {
			return err
		}
	}
	r.say("Removed the containers %s, %s and %s.\n", ContainerServe, ContainerMigrate, ContainerDB)
	if !purge {
		r.say("The volumes, the secrets and the settings are kept: run the install again to start the stack, or use --purge to remove them too.\n")
		return holdPage(scr)
	}
	tokenFile := r.preset("token-file", "")
	if tokenFile == "" {
		if tokenFile, err = defaultTokenFile(env.Deps); err != nil {
			return err
		}
	}
	if err := purgeState(ctx, rt, tokenFile, settingsPath); err != nil {
		return err
	}
	r.say("Removed the volumes, the secrets, the network, %s and %s. Backups under %s (or the --backup-dir an upgrade was given) are kept.\n", tokenFile, settingsPath, backupParent(r, filepath.Dir(tokenFile)))
	return holdPage(scr)
}

// engineNamed refuses a run that has no settings file and does not say which engine it works on: the runtime and the Podman connection
// would be defaults, and the default of this PC may be an instance that this command was never meant for. Naming both is the explicit way
// to remove a stack that has no settings file (a first install that failed before it saved them).
func engineNamed(flags map[string]string, settingsPath string) error {
	_, runtime := flags["runtime"]
	_, connection := flags["connection"]
	if runtime && connection {
		return nil
	}
	return fmt.Errorf("there is no settings file at %s, so nothing says which instance this is: pass --settings with the file of the instance, "+
		"or name its engine with --runtime and --connection (--connection= for the default one), or --tier isolated as the service account; nothing was changed", settingsPath)
}

// confirmRemoval says which instance is about to be removed (its id, its engine and its settings file) and asks. A plain uninstall asks yes
// or no; a purge, which deletes the database, asks for the instance id: typed, or --yes with --instance where nobody can type.
func (i instance) confirmRemoval(ctx context.Context, r *Resolver, purge bool, question string) error {
	id := i.currentID(ctx)
	shown, settings := id, i.settingsPath
	if id == unknownValue {
		shown += " (neither /status nor the database names it)"
	}
	if !i.haveSettings {
		settings = "none (there is no file at " + i.settingsPath + ")"
	}
	r.say("This is the instance %s on %s. Its settings file: %s.\n", shown, engineLine(i.rt.Engine()), settings)
	if purge {
		r.say("%s\n", question)
		return r.confirmInstance(instanceConfirm{
			id: id, action: "purge", ofWhat: "of the instance to remove", yesAlone: true,
			prompt: fmt.Sprintf("Type the instance id %s to remove it: ", id),
		})
	}
	ok, err := r.confirm("yes", question)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("nothing was removed")
	}
	return nil
}

// purgeState removes what the containers leave behind: the volumes, the secrets, the network, one more file (the MCP token copy, or the GitHub env file) and
// the settings file. The containers are gone by now.
func purgeState(ctx context.Context, rt Runtime, file, settingsPath string) error {
	for _, volume := range []string{VolumeData, VolumeDBData} {
		if err := rt.Delete(ctx, KindVolume, volume); err != nil {
			return err
		}
	}
	if err := rt.PurgeSecrets(ctx); err != nil {
		return err
	}
	if err := rt.Delete(ctx, KindNetwork, NetworkName); err != nil {
		return err
	}
	for _, path := range []string{file, settingsPath, pendingAppPath(settingsPath)} {
		if err := os.Remove(filepath.Clean(path)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", path, err)
		}
	}
	return nil
}
