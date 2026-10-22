package installer

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// upgradeIsolated is `suiteward install --upgrade` for the isolated tier, as the service account (docs/install.md 11.3): adopt the
// installed units (a difference stops the run before any change), show what changes and ask, back up, pull, stop the service,
// render the units again with the new digest (and the database unit with the new pin), restart the database when its pin changed,
// and restart the migration, which migrates and restarts the service.
func upgradeIsolated(ctx context.Context, saved Settings, r *Resolver, d Deps, out io.Writer) error {
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
	s.Tier, s.HTTPPort, s.MCPPort, s.JobTimeout = st.adopted.Settings.Tier, st.adopted.Settings.HTTPPort, st.adopted.Settings.MCPPort, st.adopted.Settings.JobTimeout
	postgres, err := PostgresImage()
	if err != nil {
		return err
	}
	say := func(format string, args ...any) { _, _ = fmt.Fprintf(out, format, args...) }
	plan := upgradePlan{oldImage: st.adopted.Image, newImage: image, oldDB: st.adopted.DBImage, newDB: postgres}
	if _, statErr := os.Stat(host.settingsPath); plan.current() && statErr == nil {
		say("Nothing to upgrade: already on %s. Check the instance with `suiteward doctor`.\n", digestOf(image))
		return nil
	}
	parent := backupParent(r, host.settingsPath)
	if err := confirmUpgrade(r, plan, parent); err != nil {
		return err
	}
	// The replaced units go into the backup: install.md 11.3 keeps them for a rollback.
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
	status := func(ctx context.Context) ([]byte, error) {
		return httpGet(ctx, "http://127.0.0.1:"+strconv.Itoa(s.HTTPPort)+"/status")
	}
	dir, err := takeBackup(ctx, backup{rt: host.rt, d: d, parent: parent, status: status, extra: keepUnits, say: say, now: time.Now})
	if err != nil {
		return err
	}
	say("Backup written to %s.\n", dir)
	for _, img := range []string{postgres, image} {
		if host.rt.ImageExists(ctx, img) {
			continue
		}
		if err := host.rt.Pull(ctx, img); err != nil {
			return fmt.Errorf("%w (nothing was changed; the backup is in %s)", err, dir)
		}
	}
	if err := replaceUnits(ctx, host, d, s, digest, plan.dbChanges()); err != nil {
		return withBackup(err, dir)
	}
	s.Image = image
	if err := SaveSettings(host.settingsPath, s); err != nil {
		return withBackup(err, dir)
	}
	upgraded(say, image, dir, "suiteward doctor")
	sayOldEnvFile(out, host.envPath, st.oldEnv, false)
	return nil
}

// replaceUnits does the steps after the backup: stop the service, write the units, check them, reload, refresh the database when
// its pin changed, then migrate and start.
func replaceUnits(ctx context.Context, host isolatedHost, d Deps, s Settings, digest string, dbChanges bool) error {
	systemctl := userSystemctl(d)
	if _, err := systemctl(ctx, "stop", "suiteward.service"); err != nil {
		return fmt.Errorf("stop the service: %w", err)
	}
	units, err := RenderUnits(UnitParamsFrom(s, digest))
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
		if _, err := systemctl(ctx, "restart", "suiteward-db.service"); err != nil {
			return fmt.Errorf("restart the database: %w", err)
		}
		if err := waitHealthy(ctx, host.rt); err != nil {
			return err
		}
	}
	startCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	// The migration unit migrates, and the service requires it; the service was stopped above, so it is started explicitly.
	if _, err := systemctl(startCtx, "restart", "suiteward-migrate.service"); err != nil {
		return fmt.Errorf("the migration failed, so the service was not started (read `journalctl --user -u suiteward-migrate.service -n 50 --no-pager`): %w", err)
	}
	if _, err := systemctl(startCtx, "start", "suiteward.service"); err != nil {
		return fmt.Errorf("start the service (read `journalctl --user -u suiteward.service -n 50 --no-pager`): %w", err)
	}
	return checkUnitsActive(ctx, systemctl)
}
