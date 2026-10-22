package installer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// Uninstall is `suiteward uninstall [--purge] [--yes]` for the co-located tier: it removes the three containers and keeps
// the state, or with --purge also the volumes, the secrets, the network, the MCP token copy and the settings file. It
// asks first unless --yes is given, and it never touches an instance that is not this one: the guards of the install apply.
func Uninstall(ctx context.Context, args []string, env Env) error {
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
	fs.Bool("purge", false, "also remove the volumes (the database!), the secrets, the network, the MCP token copy and the settings file")
	fs.Bool("yes", false, "do not ask before removing")
	flags, err := parse(fs, args, env.Term.Out)
	if err != nil {
		return err
	}
	r, err := NewResolver(flags, nil, env.Term)
	if err != nil {
		return err
	}
	settingsPath, defaultPath, err := settingsPathFrom(flags)
	if err != nil {
		return err
	}
	saved, haveSaved, err := loadSaved(settingsPath)
	if err != nil {
		return err
	}
	purge := flags["purge"] == "true"
	if config.Tier(r.preset("tier", string(saved.Tier))) == config.TierIsolated {
		isolated := saved
		isolated.Tier, isolated.Runtime = config.TierIsolated, RuntimePodman
		return uninstallIsolated(ctx, isolated, r, env.Deps, env.Term.Out, purge)
	}
	rt, connection, err := openRuntime(ctx, r, env, saved, false)
	if err != nil {
		return err
	}
	if err := guardDefaultSettings(installTarget{settingsPath: settingsPath, defaultPath: defaultPath, saved: saved, haveSaved: haveSaved}, rt.Name(), connection); err != nil {
		return err
	}
	// The same guard as the install: a stack that is not this installer's, or that was edited, is left alone.
	if _, _, err := existingStack(ctx, rt, saved, haveSaved); err != nil {
		return err
	}
	question := "Remove the containers " + ContainerServe + ", " + ContainerMigrate + " and " + ContainerDB + "? The data stays."
	if purge {
		question = "Remove the containers, the volumes (this deletes the database and the artifacts), the secrets, the network, the MCP token copy and the settings file?"
	}
	ok, err := r.confirm("yes", question)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("nothing was removed")
	}
	for _, name := range []string{ContainerServe, ContainerMigrate, ContainerDB} {
		if err := rt.Remove(ctx, name); err != nil {
			return err
		}
	}
	r.say("Removed the containers %s, %s and %s.\n", ContainerServe, ContainerMigrate, ContainerDB)
	if !purge {
		r.say("The volumes, the secrets and the settings are kept: run the install again to start the stack, or use --purge to remove them too.\n")
		return nil
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
	r.say("Removed the volumes, the secrets, the network, %s and %s. Backups under %s are kept.\n", tokenFile, settingsPath, backupParent(r, filepath.Dir(tokenFile)))
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
