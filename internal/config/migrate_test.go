package config

import (
	"strings"
	"testing"
)

const ownerURL = "postgres://suiteward:" + secretPassword + "@suiteward-db:5432/suiteward"

func TestLoadReadsTheMigrationSettings(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_MIGRATION_DATABASE_URL"] = ownerURL
	vars["SUITEWARD_DATABASE_APP_ROLE"] = "sw_service"
	cfg, warnings, err := fakeEnv{vars: vars}.load()
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Load = %v, warnings %v", err, warnings)
	}
	if cfg.MigrationDatabaseURL.Reveal() != ownerURL || cfg.DatabaseAppRole != "sw_service" {
		t.Fatalf("migration URL %q, role %q", cfg.MigrationDatabaseURL, cfg.DatabaseAppRole)
	}
	if strings.Contains(cfg.MigrationDatabaseURL.String(), secretPassword) {
		t.Fatal("the migration URL is not a secret")
	}
}

func TestLoadReadsTheMigrationURLFromAFile(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_MIGRATION_DATABASE_URL_FILE"] = "/owner"
	cfg, _, err := fakeEnv{vars: vars, files: map[string]string{"/owner": ownerURL + "\n"}}.load()
	if err != nil || cfg.MigrationDatabaseURL.Reveal() != ownerURL {
		t.Fatalf("Load = %v, URL %q", err, cfg.MigrationDatabaseURL)
	}
}

func TestLoadRejectsABadMigrationURLWithoutLeakingIt(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_MIGRATION_DATABASE_URL"] = "mysql://suiteward:" + secretPassword + "@db/suiteward"
	_, _, err := fakeEnv{vars: vars}.load()
	if err == nil || !strings.Contains(err.Error(), "SUITEWARD_MIGRATION_DATABASE_URL") || strings.Contains(err.Error(), secretPassword) {
		t.Fatalf("error = %v, want one naming the setting and no secret", err)
	}
}

func TestLoadRejectsAnApplicationRoleThatIsNotAnIdentifier(t *testing.T) {
	for _, bad := range []string{"has space", "semi;colon", `quo"te`, "1starts_with_digit", strings.Repeat("a", 64)} {
		vars := required(t)
		vars["SUITEWARD_DATABASE_APP_ROLE"] = bad
		if _, _, err := (fakeEnv{vars: vars}).load(); err == nil || !strings.Contains(err.Error(), "SUITEWARD_DATABASE_APP_ROLE") {
			t.Errorf("%q: error = %v, want one naming SUITEWARD_DATABASE_APP_ROLE", bad, err)
		}
	}
}
