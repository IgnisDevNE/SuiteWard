package installer

import (
	"errors"
	"testing"
)

var errInjected = errors.New("injected engine failure")

// failFirst makes the first call whose argument line contains match fail, ahead of every rule the harness has.
func (h *harness) failFirst(match string) {
	h.f.rules = append([]rule{{match: match, err: errInjected}}, h.f.rules...)
}

// A failing engine call stops a Podman install at that step: the error is returned, the service is never started, and no
// secret value reaches an argument list. Calls before the first change leave the engine as it was.
func TestInstallPodmanStopsAtTheFirstFailingEngineCall(t *testing.T) {
	cases := []struct {
		name, match     string
		beforeAnyChange bool
	}{
		{"look for the service", "container ls --all --filter name=suiteward --format", true},
		{"look for the database password", "secret ls --filter name=suiteward-db-password", true},
		{"look for the owner URL", "secret ls --filter name=suiteward-database-owner-url", true},
		{"look for the application URL", "secret ls --filter name=suiteward-database-url", true},
		{"look for the token", "secret ls --filter name=suiteward-mcp-token", true},
		{"pull the release image", "pull " + testImage, true},
		{"pull the database image", "pull docker.io/library/postgres", true},
		{"look for the network", "network ls --filter name=suiteward", false},
		{"create the network", "network create suiteward", false},
		{"look for the data volume", "volume ls --filter name=suiteward-data", false},
		{"create the data volume", "volume create suiteward-data", false},
		{"create the database volume", "volume create suiteward-db-data", false},
		{"write the database password", "secret create suiteward-db-password", false},
		{"write the owner URL", "secret create suiteward-database-owner-url", false},
		{"write the token", "secret create suiteward-mcp-token", false},
		{"remove the old service", "rm --force --ignore --time 45 suiteward", false},
		{"remove the old migrate container", "rm --force --ignore --time 45 suiteward-migrate", false},
		{"start the database", "run --detach --replace --name suiteward-db ", false},
		{"read the database health", "--format {{json .State}} suiteward-db", false},
		{"look for the role", "exec -i suiteward-db psql", false},
		{"write the application URL", "secret create --replace suiteward-database-url", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(t, "linux")
			h.failFirst(tc.match)
			err := h.podman()
			if !errors.Is(err, errInjected) {
				t.Fatalf("install = %v, want the injected failure", err)
			}
			if tc.beforeAnyChange {
				h.mustNotMutate()
			}
			if len(h.f.find("--name suiteward --network")) != 0 {
				t.Errorf("the service was started after a failure: %v", h.f.lines())
			}
			h.f.assertNoSecret(t, h.secretValues()...)
		})
	}
}

// Migrate that fails, and a service that does not start, are told apart from a plain failure: the settings were saved only
// once migrate had succeeded.
func TestInstallPodmanNamesTheStepThatFailedAfterMigrate(t *testing.T) {
	h := newHarness(t, "linux")
	h.failFirst("run --detach --replace --name suiteward --network")
	err := h.podman()
	if !errors.Is(err, errInjected) {
		t.Fatalf("install = %v, want the injected failure", err)
	}
	if _, statErr := LoadSettings(h.settingsPath()); statErr != nil {
		t.Errorf("the settings were not saved before the service started: %v", statErr)
	}
	var migrating migratingError
	if !errors.As(err, &migrating) {
		t.Errorf("a failed service start is not marked as a failure after migrate: %T", err)
	}
	if got := migrating.Unwrap(); !errors.Is(got, errInjected) {
		t.Errorf("Unwrap = %v", got)
	}
}

func TestInstallPodmanStopsWhenTheSettingsCannotBeSaved(t *testing.T) {
	h := newHarness(t, "linux")
	err := h.podman("--settings", h.dir) // a directory is not a settings file
	if err == nil {
		t.Fatal("install succeeded with a directory as the settings file")
	}
	if len(h.f.find("--name suiteward --network")) != 0 {
		t.Errorf("the service was started although the settings could not be saved: %v", h.f.lines())
	}
}
