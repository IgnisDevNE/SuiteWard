package installer

import (
	"context"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// upgradeIsolated is `suiteward install --upgrade` for the isolated tier, as the service account (docs/install.md, section 11): adopt the
// installed units (a difference stops the run before any change), show what changes and ask, back up, pull, stop the service,
// render the units again with the new digest (and the database unit with the new pin), restart the database when its pin changed,
// and restart the migration, which migrates and restarts the service; last, wait for the service to be stable. The ports and the
// job timeout stay as installed, and a run that is asked for others stops first.
func upgradeIsolated(ctx context.Context, saved Settings, r *Resolver, env Env) error {
	env.Deps = env.Deps.onIsolated()
	d, out, scr := env.Deps, env.Term.Out, r.screen
	scr.Steps("Plan", "Backup", "Images", "Replace", "Check")
	image, err := upgradeTarget(r)
	if err != nil {
		return err
	}
	digest, ok := strings.CutPrefix(image, releaseImageRepo+"@")
	if !ok {
		return fmt.Errorf("an installation on a separate server runs %s only, not %s. Nothing was changed", releaseImageRepo, image)
	}
	s := saved
	s.Tier, s.Runtime = config.TierIsolated, RuntimePodman
	host, err := openIsolatedHost(ctx, s, d)
	if err != nil {
		return err
	}
	st, err := adoptUnitsDir(host.unitsDir, host.envPath, true)
	if err != nil {
		return err
	}
	if !st.found {
		return nothingInstalled(env.Deps)
	}
	err = refuseUnfinishedInstall(env.Deps, pendingAppPath(host.settingsPath), func() (GitHub, error) {
		if s.GitHub.AppID != 0 {
			return s.GitHub, nil
		}
		return readGitHubEnvFile(host.envPath) // a host installed by hand, or a run that stopped after it wrote the file
	})
	if err != nil {
		return err
	}
	s.Tier, s.HTTPPort, s.MCPPort, s.JobTimeout = st.adopted.Settings.Tier, st.adopted.Settings.HTTPPort, st.adopted.Settings.MCPPort, st.adopted.Settings.JobTimeout
	// An upgrade keeps the installed ports and job timeout: a value asked for that is invalid, or differs, stops the run before any
	// change, so that it is never ignored in silence.
	asked, err := askedSettings(r, s)
	if err != nil {
		return err
	}
	if err := refuseChangedSettings(asked, s, env.Deps); err != nil {
		return err
	}
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(out, format, args...) }
	postgres, err := PostgresImage()
	if err != nil {
		return err
	}
	// The service unit keeps the shape it has: the settings need not name an App (a host installed by hand), and the units do not
	// change whether the service has GitHub.
	params := UnitParamsFrom(s, digest)
	params.NoGitHub = st.adopted.NoGitHub
	executor, err := readExecutorSetup(env.Deps, host.configDir)
	if err != nil {
		return err
	}
	plan := upgradePlan{oldImage: st.adopted.Image, newImage: image, oldDB: st.adopted.DBImage, newDB: postgres}
	if _, statErr := os.Stat(host.settingsPath); plan.current() && statErr == nil {
		listInstallation(scr, env.Deps, host.settingsPath, "", s, "") // no settings are saved here, and an installation that was made before the list joins now
		return upgradeCurrent(ctx, env, scr, host, executor, st.adopted.NoExecutor, image, s.HTTPPort)
	}
	// The key is settled before anything starts, a service that is down included: a key that is refused never leaves one started.
	agreed, err := settleRecipient(r, env, &s)
	if err != nil {
		return err
	}
	var instanceID string // what /status shows, kept from the reads below: the service is replaced
	status := noteInstance(&instanceID, func(ctx context.Context) ([]byte, error) {
		return env.httpGet()(ctx, "http://127.0.0.1:"+strconv.Itoa(s.HTTPPort)+"/status")
	})
	start := func(ctx context.Context) error {
		_, err := userSystemctl(d)(ctx, "start", "suiteward.service")
		return err
	}
	started, err := ensureServiceUp(ctx, r, env, status, start, "systemctl --user start suiteward.service")
	if err != nil {
		return err
	}
	parent := backupParent(r, host.configDir)
	if err := confirmUpgrade(r, plan, parent, s.BackupRecipient, started); err != nil {
		return err
	}
	scr.Step(2)
	// The replaced units go into the archive, and beside it as plain files (they hold only the names of secrets), so that a rollback can
	// copy them back.
	keepUnits := func(path string) error { return keepUnitsBeside(path, st.units) }
	inst := instance{rt: host.rt, s: s, settingsPath: host.settingsPath, haveSettings: settingsExist(host.settingsPath), image: st.adopted.Image, status: status, units: st.units, startCommand: "systemctl --user start suiteward.service", serviceStarted: started}
	dir, err := upgradeBackup(ctx, r, env, &inst, parent, keepUnits, agreed)
	if err != nil {
		return err
	}
	s.BackupRecipient = inst.s.BackupRecipient
	say("Backup written to %s.\n", dir)
	scr.Step(3)
	if err := pullMissing(ctx, scr, host.rt, postgres, image); err != nil {
		return fmt.Errorf("%w (%s; the backup is in %s)", err, inst.changed(), dir)
	}
	scr.Step(4)
	if _, err := executor.settle(ctx, host.rt.Secrets(), scr, &params, "`"+env.Deps.suggest("install")+"`", false); err != nil {
		return withBackup(env.Deps, err, dir, host.unitsDir)
	}
	if err := replaceUnits(ctx, host, d, scr, params, plan.dbChanges()); err != nil {
		return withBackup(env.Deps, err, dir, host.unitsDir)
	}
	// Migrate ran: a release that crash-loops now gets the advice of a failure after the migration, the restore of the backup.
	scr.Step(5)
	if err := waitStable(ctx, env, scr, s.HTTPPort); err != nil {
		return withBackup(env.Deps, migratingError{err}, dir, host.unitsDir)
	}
	s.Image = image
	if err := SaveSettings(host.settingsPath, s); err != nil {
		return withBackup(env.Deps, migratingError{err}, dir, host.unitsDir)
	}
	listInstallation(scr, env.Deps, host.settingsPath, "", s, instanceID)
	upgraded(scr, image, dir, goBackIsolated(env.Deps, dir, host.unitsDir), env.Deps.suggest("doctor"))
	sayOldEnvFile(out, host.envPath, st.oldEnv, false)
	return showSummary(scr, func() []ui.KV {
		return isolatedSummary(ctx, env, host, s, ui.KV{Key: "Image", Value: fmt.Sprintf("%s (was %s)", digestOf(image), digestOf(st.adopted.Image))})
	})
}

// refuseChangedSettings refuses an upgrade that is asked for other ports or another job timeout than the installed ones: the
// upgrade keeps them, and the re-run of the install, which takes the values it is given, is how to change them.
func refuseChangedSettings(asked, installed Settings, d Deps) error {
	var flags []string
	if asked.HTTPPort != installed.HTTPPort {
		flags = append(flags, "--http-port "+strconv.Itoa(asked.HTTPPort))
	}
	if asked.MCPPort != installed.MCPPort {
		flags = append(flags, "--mcp-port "+strconv.Itoa(asked.MCPPort))
	}
	if asked.JobTimeout != installed.JobTimeout {
		flags = append(flags, "--job-timeout "+quoteArg(asked.JobTimeout))
	}
	if len(flags) == 0 {
		return nil
	}
	return fmt.Errorf("an upgrade keeps the installed ports and job timeout (HTTP port %d, MCP port %d, job timeout %q), but this run asks for others (%s). Nothing was changed. To change them, run `%s` first, then upgrade without those flags",
		installed.HTTPPort, installed.MCPPort, installed.JobTimeout, strings.Join(flags, " "), d.suggest("install", strings.Join(flags, " ")))
}

// replaceUnits does the steps after the backup: stop the service, then prepare the new units, then migrate and start. An error
// says how far it got (stoppedError, migratingError) for the advice of withBackup.
func replaceUnits(ctx context.Context, host isolatedHost, d Deps, scr *ui.Screen, params UnitParams, dbChanges bool) error {
	systemctl := userSystemctl(d)
	stop := func() error { _, err := systemctl(ctx, "stop", "suiteward.service"); return err } // the service gets up to 45 s to stop
	if err := working(scr, "Stopping the service", stop); err != nil {
		return fmt.Errorf("could not stop the service: %w", err)
	}
	if _, err := prepareUnits(ctx, host, d, scr, params, dbChanges); err != nil {
		return stoppedError{err}
	}
	if err := migrateAndStart(ctx, d, scr); err != nil {
		return migratingError{err}
	}
	return nil
}

// prepareUnits writes the units, checks them, reloads and refreshes the database when its pin changed. It returns the units it
// wrote.
func prepareUnits(ctx context.Context, host isolatedHost, d Deps, scr *ui.Screen, params UnitParams, dbChanges bool) (map[string][]byte, error) {
	units, err := RenderUnits(params)
	if err != nil {
		return nil, err
	}
	if _, err := WriteUnits(host.unitsDir, units); err != nil {
		return nil, err
	}
	if err := dryRun(ctx, d, units); err != nil {
		return nil, err
	}
	if _, err := d.Exec(ctx, DaemonReload[0], DaemonReload[1:], nil); err != nil {
		return nil, fmt.Errorf("systemctl --user daemon-reload: %w", err)
	}
	if dbChanges {
		// The running database container has the old image: restart it, so it is recreated from the new pin on the same volume.
		if _, err := userSystemctl(d)(ctx, "restart", "suiteward-db.service"); err != nil {
			return nil, fmt.Errorf("could not restart the database: %w", err)
		}
		return units, waitHealthy(ctx, host.rt, barOn(scr, "Database"))
	}
	return units, nil
}

// migrateAndStart migrates and starts the service, under a spinner on a full screen.
func migrateAndStart(ctx context.Context, d Deps, scr *ui.Screen) error {
	systemctl := userSystemctl(d)
	startCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	// The migration unit migrates, and the service requires it; the service was stopped before, so it is started explicitly.
	err := working(scr, "Updating the database and starting the service", func() error {
		if _, err := systemctl(startCtx, "restart", "suiteward-migrate.service"); err != nil {
			return fmt.Errorf("the database update (migration) failed, so the service was not started (to see why, run `journalctl --user -u suiteward-migrate.service -n 50 --no-pager`): %w", err)
		}
		if _, err := systemctl(startCtx, "start", "suiteward.service"); err != nil {
			return fmt.Errorf("could not start the service (to see why, run `journalctl --user -u suiteward.service -n 50 --no-pager`): %w", err)
		}
		return nil
	})
	if err != nil {
		return err
	}
	return checkUnitsActive(ctx, systemctl)
}
