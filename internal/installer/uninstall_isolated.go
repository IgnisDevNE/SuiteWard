package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
)

// uninstallIsolated is `suiteward uninstall [--purge]` as the service account: stop the units and delete the six unit files, and
// with purge also the secrets, the volumes (the database!), the network, the GitHub env file and the settings file. The units
// must be this release's, and an edit is refused with its diff. It never touches /etc: that file is the administrator's.
func uninstallIsolated(ctx context.Context, saved Settings, r *Resolver, d Deps, out io.Writer, purge bool) error {
	s := saved
	s.Tier, s.Runtime = "isolated", RuntimePodman
	host, err := openIsolatedHost(ctx, s, d)
	if err != nil {
		return err
	}
	st, err := adoptUnitsDir(host.unitsDir, host.envPath, true)
	if err != nil {
		return err
	}
	if !st.found && !purge {
		return errors.New("nothing is installed here: there are no units to remove (--purge removes what an earlier uninstall left)")
	}
	question := "Stop and remove the units of SuiteWard? The data stays."
	if purge {
		question = "Stop and remove the units, and delete the secrets, the volumes (this deletes the database and the artifacts), the network, the GitHub env file and the settings file?"
	}
	ok, err := r.confirm("yes", question)
	if err != nil {
		return err
	}
	if !ok {
		return errors.New("nothing was removed")
	}
	if st.found {
		if err := stopAndRemoveUnits(ctx, host, d); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "Stopped the services and removed the units from %s.\n", host.unitsDir)
	}
	if purge {
		if err := purgeState(ctx, host.rt, host.envPath, host.settingsPath); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(out, "Removed the volumes, the secrets, the network, %s and %s. Backups under %s are kept.\n", host.envPath, host.settingsPath, filepath.Join(host.configDir, "backups"))
	} else {
		_, _ = fmt.Fprintf(out, "The volumes, the secrets and the settings are kept: install again to start the stack, or use --purge to remove them too.\n")
	}
	if st.oldEnv != "" {
		_, _ = fmt.Fprintf(out, "The installed units read %s, which an administrator owns and this command never touches. To remove it: sudo rm %s\n", st.oldEnv, st.oldEnv)
	}
	return nil
}

// stopAndRemoveUnits stops the services (the one that needs the others first), deletes the six unit files and reloads systemd.
func stopAndRemoveUnits(ctx context.Context, host isolatedHost, d Deps) error {
	templates, err := embeddedUnits()
	if err != nil {
		return err
	}
	systemctl := userSystemctl(d)
	// Stop only what systemd has loaded: a unit file that was never reloaded has no service to stop.
	for _, file := range []string{"suiteward.container", "suiteward-migrate.container", "suiteward-db.container", "suiteward-data.volume", "suiteward-db-data.volume", "suiteward.network"} {
		unit := unitService(file)
		loaded, err := systemctl(ctx, "show", "--property=LoadState", "--value", unit)
		if err != nil {
			return fmt.Errorf("ask for the state of %s: %w", unit, err)
		}
		if loaded != "loaded" {
			continue
		}
		if _, err := systemctl(ctx, "stop", unit); err != nil {
			return fmt.Errorf("stop %s: %w", unit, err)
		}
	}
	for _, file := range sortedKeys(templates) {
		if err := os.Remove(filepath.Join(host.unitsDir, file)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove the unit %s: %w", file, err)
		}
	}
	if _, err := d.Exec(ctx, DaemonReload[0], DaemonReload[1:], nil); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %w", err)
	}
	return nil
}
