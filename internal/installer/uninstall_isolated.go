package installer

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// uninstallIsolated is `suiteward uninstall [--purge]` as the service account: stop the units and delete the six unit files, and
// with purge also the secrets, the volumes (the database!), the network, the GitHub env file and the settings file. The units
// must be this release's, and an edit is refused with its diff. It never touches /etc: that file is the administrator's.
func uninstallIsolated(ctx context.Context, saved Settings, r *Resolver, env Env, purge bool) error {
	d, out := env.Deps, env.Term.Out
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
		return errors.New("nothing is installed here, so there are no units to remove. `--purge` removes what an earlier uninstall left")
	}
	question := "Stop SuiteWard and remove its units (its service files)? Your data stays."
	if purge {
		question = "Stop SuiteWard, remove its units, and delete the secrets, the volumes (this deletes the database and the saved files), the network, the GitHub env file and the settings file?"
	}
	port := s.HTTPPort // the units that are gone (a purge after an earlier uninstall) leave the port the settings name
	if st.found {
		port = st.adopted.Settings.HTTPPort
	}
	status := func(ctx context.Context) ([]byte, error) {
		return env.httpGet()(ctx, "http://127.0.0.1:"+strconv.Itoa(port)+"/status")
	}
	// The file a purge removes is the account's own, whatever --settings the settings were read from.
	inst := instance{rt: host.rt, settingsPath: host.settingsPath, haveSettings: settingsExist(host.settingsPath), status: status}
	if err := inst.confirmRemoval(ctx, r, purge, question); err != nil {
		return err
	}
	r.screen.Step(2) // Uninstall set the steps on this screen
	// Also with no unit files (a purge after an earlier uninstall): a service that uninstall left active has no file, and the next
	// install would not run it again.
	stopped, err := stopAndRemoveUnits(ctx, host, d)
	switch {
	case err != nil:
		return err
	case st.found:
		_, _ = fmt.Fprintf(out, "Stopped the services and removed the units from %s.\n", host.unitsDir)
	case len(stopped) > 0:
		_, _ = fmt.Fprintf(out, "Stopped %s: an earlier uninstall left them without their unit files.\n", strings.Join(stopped, ", "))
	}
	if purge {
		if err := purgeState(ctx, host.rt, []string{host.envPath}, host.settingsPath); err != nil {
			return err
		}
		if err := os.Remove(executorHandoverFile(host.configDir)); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("remove %s: %w", executorHandoverFile(host.configDir), err)
		}
		_, _ = fmt.Fprintf(out, "Removed the volumes, the secrets, the network, the copy of the executor's token, %s and %s. Backups under %s are kept.\n", host.envPath, host.settingsPath, filepath.Join(host.configDir, "backups"))
	} else {
		_, _ = fmt.Fprintf(out, "The volumes (your data), the secrets and the settings are kept. Run install again to start SuiteWard, or use `--purge` to remove them too.\n")
	}
	if st.oldEnv != "" {
		_, _ = fmt.Fprintf(out, "The installed units read %s, which an administrator owns and this command never touches. To remove it: sudo rm %s\n", st.oldEnv, st.oldEnv)
	}
	_, _ = fmt.Fprint(out, executorHostNote())
	return nil
}

// stopAndRemoveUnits stops every service the six units generate that is not inactive, deletes the unit files that are there and
// reloads systemd when it did something. It asks systemd, not the directory: a service that an earlier uninstall left active
// stays active (and is not run again by the next install) after its unit file is gone. systemd stops the services that need
// another one first, so the order of the stops does not matter. It returns the services it stopped, each with the state it was
// found in (active, activating, failed ...), for the message.
func stopAndRemoveUnits(ctx context.Context, host isolatedHost, d Deps) (stopped []string, err error) {
	templates, err := embeddedUnits()
	if err != nil {
		return nil, err
	}
	systemctl := userSystemctl(d)
	for _, file := range sortedKeys(templates) {
		unit := unitService(file)
		state, err := systemctl(ctx, "show", "--property=ActiveState", "--value", unit)
		if err != nil {
			return stopped, fmt.Errorf("could not ask for the state of %s: %w", unit, err)
		}
		if state == "inactive" { // also a unit that is not loaded: there is nothing to stop
			continue
		}
		if _, err := systemctl(ctx, "stop", unit); err != nil {
			return stopped, fmt.Errorf("could not stop %s: %w", unit, err)
		}
		stopped = append(stopped, unit+" ("+state+")")
	}
	removed := false
	for _, file := range sortedKeys(templates) {
		err := os.Remove(filepath.Join(host.unitsDir, file))
		switch {
		case err == nil:
			removed = true
		case !errors.Is(err, fs.ErrNotExist):
			return stopped, fmt.Errorf("could not remove the unit file %s: %w", file, err)
		}
	}
	if !removed && len(stopped) == 0 {
		return nil, nil
	}
	if _, err := d.Exec(ctx, DaemonReload[0], DaemonReload[1:], nil); err != nil {
		return stopped, fmt.Errorf("systemctl --user daemon-reload: %w", err)
	}
	return stopped, nil
}
