package app

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

func TestOSEnvReadsTheProcessEnvironment(t *testing.T) {
	t.Setenv("SUITEWARD_APP_TEST_PROBE", "present")
	env := OSEnv()
	if value, ok := env.Lookup("SUITEWARD_APP_TEST_PROBE"); !ok || value != "present" {
		t.Fatalf("OSEnv().Lookup = %q, %v; want the process variable", value, ok)
	}
	if env.ReadFile == nil || len(env.Environ) == 0 {
		t.Fatal("OSEnv must provide the file reader and the variable list")
	}
}

func TestConnectRejectsUnusableSettings(t *testing.T) {
	valid := config.Config{DatabaseURL: "postgres://nobody@127.0.0.1:1/none", ArtifactDir: t.TempDir(), JobMaxAttempts: 5}
	// Each case must be refused by its own step, before the unreachable database is tried.
	for name, test := range map[string]struct {
		mutate func(*config.Config)
		step   string
	}{
		"blank artifact directory": {func(c *config.Config) { c.ArtifactDir = "" }, "open the artifact store"},
		"negative job attempts":    {func(c *config.Config) { c.JobMaxAttempts = -1 }, "create the River insert client"},
		"malformed database URL":   {func(c *config.Config) { c.DatabaseURL = "postgres://%" }, "open the connection pool"},
	} {
		t.Run(name, func(t *testing.T) {
			cfg := valid
			test.mutate(&cfg)
			if _, _, err := connect(context.Background(), cfg); err == nil || !strings.Contains(err.Error(), test.step) {
				t.Fatalf("connect = %v; want a refusal at %q", err, test.step)
			}
		})
	}
}

func TestListenAndServeReportsAServerThatStopsOnItsOwn(t *testing.T) {
	srv := &http.Server{}
	if err := srv.Close(); err != nil { // a closed server refuses to serve
		t.Fatal(err)
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := listenAndServe(context.Background(), "127.0.0.1:0", srv, logger); err == nil {
		t.Fatal("listenAndServe returned nil although the server stopped by itself")
	}
}
