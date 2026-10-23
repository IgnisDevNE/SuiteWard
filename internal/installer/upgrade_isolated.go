package installer

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// upgradeIsolated is `suiteward install --upgrade` for the isolated tier, as the service account (docs/install.md, section 11): adopt the
// installed units (a difference stops the run before any change), show what changes and ask, back up, pull, stop the service,
// render the units again with the new digest (and the database unit with the new pin), restart the database when its pin changed,
// and restart the migration, which migrates and restarts the service.
func upgradeIsolated(ctx context.Context, saved Settings, r *Resolver, env Env) error {
	d, out := env.Deps, env.Term.Out
	image, err := upgradeTarget(r)
	if err != nil {
		return err
	}
	digest, ok := strings.CutPrefix(image, releaseImageRepo+"@")
	if !ok {
		return fmt.Errorf("the isolated tier runs %s only, not %s", releaseImageRepo, image)
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
		return ErrNothingInstalled
	}
	err = refuseUnfinishedInstall(pendingAppPath(host.settingsPath), func() (GitHub, error) {
		if s.GitHub.AppID != 0 {
			return s.GitHub, nil
		}
		return readGitHubEnvFile(host.envPath) // a host installed by hand, or a run that stopped after it wrote the file
	})
	if err != nil {
		return err
	}
	s.Tier, s.HTTPPort, s.MCPPort, s.JobTimeout = st.adopted.Settings.Tier, st.adopted.Settings.HTTPPort, st.adopted.Settings.MCPPort, st.adopted.Settings.JobTimeout
	// An upgrade keeps the installed ports and job timeout, as an install does: a value asked for that differs is refused when it
	// is invalid and reported when it is not applied, never ignored in silence.
	asked, err := chooseIsolatedSettings(r, s)
	if err != nil {
		return err
	}
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(out, format, args...) }
	reportAdopted(say, asked, s)
	postgres, err := PostgresImage()
	if err != nil {
		return err
	}
	// The service unit keeps the shape it has: the settings need not name an App (a host installed by hand), and the units do not
	// change whether the service has GitHub.
	params := UnitParamsFrom(s, digest)
	params.NoGitHub = st.adopted.NoGitHub
	plan := upgradePlan{oldImage: st.adopted.Image, newImage: image, oldDB: st.adopted.DBImage, newDB: postgres}
	if _, statErr := os.Stat(host.settingsPath); plan.current() && statErr == nil {
		say("Nothing to upgrade: already on %s. Check the instance with `suiteward doctor`.\n", digestOf(image))
		return nil
	}
	status := func(ctx context.Context) ([]byte, error) {
		return env.httpGet()(ctx, "http://127.0.0.1:"+strconv.Itoa(s.HTTPPort)+"/status")
	}
	start := func(ctx context.Context) error {
		_, err := userSystemctl(d)(ctx, "start", "suiteward.service")
		return err
	}
	started, err := ensureServiceUp(ctx, r, env, status, start, "systemctl --user start suiteward.service")
	if err != nil {
		return err
	}
	parent := backupParent(r, host.configDir)
	if err := confirmUpgrade(r, plan, parent, started); err != nil {
		return err
	}
	// The replaced units go into the backup, so a rollback can put them back.
	keepUnits := func(dir string) error {
		units := filepath.Join(dir, backupUnits)
		if err := os.Mkdir(units, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", units, err)
		}
		for _, name := range sortedKeys(st.units) {
			if err := writePrivateFile(filepath.Join(units, name), st.units[name]); err != nil {
				return err
			}
		}
		return nil
	}
	dir, err := takeBackup(ctx, backup{rt: host.rt, d: d, parent: parent, status: status, extra: keepUnits, now: time.Now})
	if err != nil {
		return err
	}
	say("Backup written to %s.\n", dir)
	if err := pullMissing(ctx, host.rt, postgres, image); err != nil {
		return fmt.Errorf("%w (nothing was changed; the backup is in %s)", err, dir)
	}
	if err := replaceUnits(ctx, host, d, params, plan.dbChanges()); err != nil {
		return withBackup(err, dir, host.unitsDir)
	}
	s.Image = image
	if err := SaveSettings(host.settingsPath, s); err != nil {
		return withBackup(migratingError{err}, dir, host.unitsDir)
	}
	upgraded(say, st.adopted.Image, image, dir, "suiteward doctor")
	sayOldEnvFile(out, host.envPath, st.oldEnv, false)
	return nil
}

// replaceUnits does the steps after the backup: stop the service, then prepare the new units, then migrate and start. An error
// says how far it got (stoppedError, migratingError) for the advice of withBackup.
func replaceUnits(ctx context.Context, host isolatedHost, d Deps, params UnitParams, dbChanges bool) error {
	systemctl := userSystemctl(d)
	if _, err := systemctl(ctx, "stop", "suiteward.service"); err != nil {
		return fmt.Errorf("stop the service: %w", err)
	}
	if err := prepareUnits(ctx, host, d, params, dbChanges); err != nil {
		return stoppedError{err}
	}
	if err := migrateAndStart(ctx, d); err != nil {
		return migratingError{err}
	}
	return nil
}

// prepareUnits writes the units, checks them, reloads and refreshes the database when its pin changed.
func prepareUnits(ctx context.Context, host isolatedHost, d Deps, params UnitParams, dbChanges bool) error {
	units, err := RenderUnits(params)
	if err != nil {
		return err
	}
	if _, err := WriteUnits(host.unitsDir, units); err != nil {
		return err
	}
	if err := dryRun(ctx, d, units); err != nil {
		return err
	}
	if _, err := d.Exec(ctx, DaemonReload[0], DaemonReload[1:], nil); err != nil {
		return fmt.Errorf("systemctl --user daemon-reload: %w", err)
	}
	if dbChanges {
		// The running database container has the old image: restart it, so it is recreated from the new pin on the same volume.
		if _, err := userSystemctl(d)(ctx, "restart", "suiteward-db.service"); err != nil {
			return fmt.Errorf("restart the database: %w", err)
		}
		return waitHealthy(ctx, host.rt)
	}
	return nil
}

// migrateAndStart migrates and starts the service.
func migrateAndStart(ctx context.Context, d Deps) error {
	systemctl := userSystemctl(d)
	startCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	// The migration unit migrates, and the service requires it; the service was stopped before, so it is started explicitly.
	if _, err := systemctl(startCtx, "restart", "suiteward-migrate.service"); err != nil {
		return fmt.Errorf("the migration failed, so the service was not started (read `journalctl --user -u suiteward-migrate.service -n 50 --no-pager`): %w", err)
	}
	if _, err := systemctl(startCtx, "start", "suiteward.service"); err != nil {
		return fmt.Errorf("start the service (read `journalctl --user -u suiteward.service -n 50 --no-pager`): %w", err)
	}
	return checkUnitsActive(ctx, systemctl)
}
