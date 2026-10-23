package installer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The isolated tier refuses, as the co-located one does (settings_guard_test.go), a purge by --yes of an instance that nothing names: the
// service account has no settings file, the service does not answer and the database holds no id, so the id is "unknown", and the database
// volume is still there. Nothing is removed. Without the volume the same purge runs, which shows that the volume is what the refusal rests on.
func TestUninstallIsolatedPurgeOfAnInstanceNothingNamesRefusesWhileItsDatabaseIsThere(t *testing.T) {
	purge := func(t *testing.T, databaseVolume bool) (*isoUpgrade, map[string]string, error) {
		u := newIsoUpgrade(t, nil)
		configDir := filepath.Join(u.home, ".config") // where the account's default settings path is, with no --settings
		t.Setenv("XDG_CONFIG_HOME", configDir)
		t.Setenv("APPDATA", configDir)
		t.Setenv("HOME", u.home) // macOS reads the configuration directory from it: never the settings file of the user running the test
		if err := os.Remove(settingsFile(u.home)); err != nil {
			t.Fatal(err)
		}
		u.f.objects[VolumeDBData] = databaseVolume
		before := u.units()
		env := u.env()
		env.HTTPGet = func(context.Context, string) ([]byte, error) { return nil, errConnectionRefused }
		err := Uninstall(context.Background(), []string{"--tier", "isolated", "--purge", "--yes", "--instance", unknownValue}, env)
		return u, before, err
	}

	t.Run("the database volume is there", func(t *testing.T) {
		u, before, err := purge(t, true)
		if err == nil || !strings.Contains(err.Error(), "terminal") || !strings.Contains(err.Error(), "--settings") {
			t.Fatalf("got %v, want a refusal that names the terminal and the settings file", err)
		}
		u.untouched(before)
	})
	t.Run("no database volume", func(t *testing.T) {
		u, _, err := purge(t, false)
		if err != nil {
			t.Fatal(err)
		}
		if !u.f.has("systemctl --user stop") {
			t.Errorf("the purge did not run:\n%s", strings.Join(u.f.lines(), "\n"))
		}
	})
}
