package config

import (
	"strings"
	"testing"
)

const ownerURL = "postgres://suiteward:" + secretPassword + "@suiteward-db:5432/suiteward"

func TestLoadReadsTheMigrationURL(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_MIGRATION_DATABASE_URL"] = ownerURL
	cfg, warnings, err := fakeEnv{vars: vars}.load()
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Load = %v, warnings %v", err, warnings)
	}
	if cfg.MigrationDatabaseURL.Reveal() != ownerURL {
		t.Fatalf("migration URL %q", cfg.MigrationDatabaseURL)
	}
	if strings.Contains(cfg.MigrationDatabaseURL.String(), secretPassword) {
		t.Fatal("the migration URL is a secret")
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

// The role the service runs as is the user of DATABASE_URL: there is no second setting that could disagree with it.
func TestThereIsNoApplicationRoleSetting(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_DATABASE_APP_ROLE"] = "other"
	_, warnings, err := fakeEnv{vars: vars}.load()
	if err != nil || len(warnings) != 1 || warnings[0] != "SUITEWARD_DATABASE_APP_ROLE" {
		t.Fatalf("Load = %v, warnings %v; want the setting reported as unrecognized", err, warnings)
	}
}
