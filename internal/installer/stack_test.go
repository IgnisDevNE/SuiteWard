package installer

import (
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

func colocatedSettings() Settings {
	s := DefaultSettings()
	s.HTTPPort, s.MCPPort = 18081, 18091
	return s
}

func TestColocatedStackPorts(t *testing.T) {
	stack, err := ColocatedStack(colocatedSettings(), testImage)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"127.0.0.1:18081:8080", "127.0.0.1:18091:8090"}
	if !slices.Equal(stack.Serve.Publish, want) {
		t.Fatalf("got %v, want %v", stack.Serve.Publish, want)
	}
	if len(stack.DB.Publish) != 0 || len(stack.Migrate.Publish) != 0 {
		t.Fatal("only serve publishes a port")
	}
}

func TestColocatedStackKeepsTheFlagSetsOfLocalPS1(t *testing.T) {
	stack, err := ColocatedStack(colocatedSettings(), testImage)
	if err != nil {
		t.Fatal(err)
	}
	db, migrate, serve := stack.DB, stack.Migrate, stack.Serve

	if db.Name != ContainerDB || db.User != "999:999" || db.Memory != "384m" || db.StopTimeout != 30 || !db.Detach || db.Restart != "always" ||
		db.Network != NetworkName || !slices.Equal(db.Tmpfs, []string{"/run/postgresql", "/tmp"}) ||
		!slices.Equal(db.Volumes, []string{VolumeDBData + ":/var/lib/postgresql"}) || !slices.Equal(db.Secrets, []string{SecretDBPassword}) ||
		db.Health == nil || db.Health.Cmd != "pg_isready -U suiteward -d suiteward" || db.Health.Interval != "5s" || db.Health.Timeout != "3s" || db.Health.Retries != 12 {
		t.Errorf("database spec: %+v", db)
	}
	if pg, _ := PostgresImage(); db.Image != pg {
		t.Errorf("the database does not run the pinned image: %s", db.Image)
	}
	if !slices.Equal(db.Env, []string{"POSTGRES_USER=suiteward", "POSTGRES_DB=suiteward", "POSTGRES_PASSWORD_FILE=/run/secrets/postgres-password"}) {
		t.Errorf("database env: %v", db.Env)
	}

	if migrate.Name != ContainerMigrate || migrate.Detach || migrate.Restart != "" || migrate.Memory != "256m" || migrate.Image != testImage ||
		!slices.Equal(migrate.Args, []string{"migrate"}) || len(migrate.Publish) != 0 || len(migrate.Volumes) != 0 {
		t.Errorf("migrate spec: %+v", migrate)
	}
	if !slices.Equal(migrate.Secrets, []string{SecretDatabaseURL, SecretDatabaseOwnerURL}) {
		t.Errorf("migrate secrets: %v", migrate.Secrets)
	}
	if !slices.Equal(migrate.Env, []string{"SUITEWARD_DATABASE_URL_FILE=/run/secrets/database-url", "SUITEWARD_MIGRATION_DATABASE_URL_FILE=/run/secrets/database-owner-url"}) {
		t.Errorf("migrate env: %v", migrate.Env)
	}

	if serve.Name != ContainerServe || !serve.Detach || serve.Restart != "always" || serve.Memory != "256m" || serve.StopTimeout != 45 || serve.Image != testImage ||
		!slices.Equal(serve.Volumes, []string{VolumeData + ":/data"}) || len(serve.Args) != 0 {
		t.Errorf("serve spec: %+v", serve)
	}
	if !slices.Equal(serve.Secrets, []string{SecretDatabaseURL, SecretMCPToken}) {
		t.Errorf("serve secrets: %v", serve.Secrets)
	}
	if slices.Contains(serve.Secrets, SecretDatabaseOwnerURL) || strings.Contains(strings.Join(serve.Env, " "), "MIGRATION") {
		t.Error("serve sees the owner URL")
	}
	wantEnv := []string{
		"SUITEWARD_DATABASE_URL_FILE=/run/secrets/database-url", "SUITEWARD_MCP_TOKEN_FILE=/run/secrets/mcp-token",
		"SUITEWARD_INSTALLATION_TIER=co-located",
	}
	if !slices.Equal(serve.Env, wantEnv) {
		t.Errorf("serve env without GitHub: %v", serve.Env)
	}
}

func TestColocatedStackJobTimeoutReachesServeOnly(t *testing.T) {
	s := colocatedSettings()
	s.JobTimeout = "3m"
	stack, err := ColocatedStack(s, testImage)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(stack.Serve.Env, "SUITEWARD_JOB_TIMEOUT=3m") {
		t.Errorf("serve env: %v", stack.Serve.Env)
	}
	for _, spec := range []RunSpec{stack.DB, stack.Migrate} {
		if strings.Contains(strings.Join(spec.Env, " "), "JOB_TIMEOUT") {
			t.Errorf("%s gets the job timeout", spec.Name)
		}
	}
}

func TestColocatedStackGitHubIsAllOrNothing(t *testing.T) {
	s := colocatedSettings()
	s.GitHub = validGitHub()
	stack, err := ColocatedStack(s, testImage)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"SUITEWARD_GITHUB_APP_ID=1234", "SUITEWARD_GITHUB_INSTALLATION_ID=5678", "SUITEWARD_GITHUB_REPOSITORIES=Acme/api,acme/web",
		"SUITEWARD_GITHUB_APP_KEY_FILE=/run/secrets/github-app-key",
	} {
		if !slices.Contains(stack.Serve.Env, want) {
			t.Errorf("serve env lacks %s: %v", want, stack.Serve.Env)
		}
	}
	if !slices.Contains(stack.Serve.Secrets, SecretGitHubAppKey) {
		t.Errorf("serve secrets: %v", stack.Serve.Secrets)
	}
	if slices.Contains(stack.Migrate.Secrets, SecretGitHubAppKey) {
		t.Error("migrate gets the App key")
	}
}

func TestColocatedStackTierAndValidation(t *testing.T) {
	s := colocatedSettings()
	s.Tier = config.TierIsolated
	if _, err := ColocatedStack(s, testImage); err == nil {
		t.Fatal("a stack was made for the isolated tier")
	}
	s = colocatedSettings()
	s.HTTPPort = s.MCPPort
	if _, err := ColocatedStack(s, testImage); err == nil {
		t.Fatal("a stack was made from invalid settings")
	}
	if _, err := ColocatedStack(colocatedSettings(), ""); err == nil {
		t.Fatal("a stack was made without an image")
	}
}
