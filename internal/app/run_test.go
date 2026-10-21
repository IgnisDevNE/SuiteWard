package app

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"
)

// testEnv is an environment of exactly the given variables and no files.
func testEnv(vars map[string]string) Env {
	var environ []string
	for name, value := range vars {
		environ = append(environ, name+"="+value)
	}
	return Env{
		Lookup:   func(name string) (string, bool) { value, ok := vars[name]; return value, ok },
		Environ:  environ,
		ReadFile: func(string) ([]byte, error) { return nil, errors.New("no files in this environment") },
	}
}

func run(args []string, env Env) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = Run(context.Background(), args, env, &out, &errOut)
	return code, out.String(), errOut.String()
}

// leakWord stands for a database password; the tests prove it never appears in an output.
const leakWord = "s3cret-pw"

func TestUsageErrorsExitWithTwo(t *testing.T) {
	for _, args := range [][]string{
		nil, {"unknown"}, {"serve", "extra"},
		{"probe", "--no-such-flag"}, {"probe", "stray"}, {"probe", "--timeout", "soon"}, {"probe", "--delay", "-1s"},
		{"probe", "--wait", "not-an-id"}, {"probe", "--wait", "1:probe-a", "--delay", "1s"}, {"probe", "--wait", "1:probe-a", "--no-wait"},
	} {
		code, stdout, stderr := run(args, testEnv(nil))
		if code != 2 || stdout != "" || !strings.Contains(stderr, "usage") {
			t.Errorf("Run(%q) = %d, stdout %q, stderr %q; want 2, nothing on stdout and a usage message", args, code, stdout, stderr)
		}
	}
}

func TestBadSettingsListEveryProblemAndNoSecret(t *testing.T) {
	env := testEnv(map[string]string{
		"SUITEWARD_DATABASE_URL": "mysql://user:" + leakWord + "@db/suiteward",
		"SUITEWARD_JOB_WORKERS":  "0",
		"SUITEWARD_DATABSE_URL":  "typo",
	})
	for _, command := range []string{"serve", "probe"} {
		code, stdout, stderr := run([]string{command}, env)
		if code != 1 {
			t.Fatalf("%s with bad settings exited %d; want 1", command, code)
		}
		for _, problem := range []string{"SUITEWARD_DATABASE_URL", "SUITEWARD_ARTIFACT_DIR", "SUITEWARD_JOB_WORKERS"} {
			if !strings.Contains(stderr, problem) {
				t.Errorf("%s: stderr does not name %s: %q", command, problem, stderr)
			}
		}
		if strings.Contains(stdout+stderr, leakWord) {
			t.Errorf("%s printed the secret: %q %q", command, stdout, stderr)
		}
		if warned := strings.Contains(stdout, "SUITEWARD_DATABSE_URL"); warned != (command == "serve") {
			t.Errorf("%s logged the unrecognized setting = %v; only serve logs it: %q", command, warned, stdout)
		}
	}
}

// An unencoded @ in the password defeats the redaction of pgx's own errors,
// which would echo the password tail: the URL must be refused with a fixed message.
func TestAMalformedDatabaseURLIsRefusedWithoutEchoingIt(t *testing.T) {
	env := testEnv(map[string]string{
		"SUITEWARD_DATABASE_URL": "postgres://u:p@S3cr3tPW@127.0.0.1:1/db?sslmode=bogus",
		"SUITEWARD_ARTIFACT_DIR": t.TempDir(),
	})
	for _, args := range [][]string{{"serve"}, {"probe"}, {"probe", "--wait", "1:probe-a"}} {
		code, stdout, stderr := run(args, env)
		if code != 1 {
			t.Errorf("%q exited %d; want 1", args, code)
		}
		if !strings.Contains(stderr, "SUITEWARD_DATABASE_URL is not a valid PostgreSQL connection string") {
			t.Errorf("%q did not report the fixed message on stderr: %q", args, stderr)
		}
		if strings.Contains(stdout+stderr, "S3cr3tPW") {
			t.Errorf("%q printed the password: %q %q", args, stdout, stderr)
		}
	}
}

func TestUnreachableDatabaseIsFatalAndLogged(t *testing.T) {
	env := testEnv(map[string]string{
		"SUITEWARD_DATABASE_URL": "postgres://suiteward:" + leakWord + "@127.0.0.1:1/suiteward?sslmode=disable",
		"SUITEWARD_ARTIFACT_DIR": t.TempDir(),
		"SUITEWARD_HTTP_ADDR":    "127.0.0.1:0",
	})
	for _, args := range [][]string{{"serve"}, {"probe"}, {"probe", "--wait", "1:probe-a"}} {
		code, stdout, stderr := run(args, env)
		if code != 1 {
			t.Fatalf("%q exited %d; want 1", args, code)
		}
		if args[0] == "serve" && !strings.Contains(stdout, `"level":"ERROR"`) {
			t.Errorf("serve did not log its fatal error: %q", stdout)
		}
		if args[0] == "probe" && !strings.Contains(stderr, "suiteward:") {
			t.Errorf("probe did not report its error on stderr: %q", stderr)
		}
		if strings.Contains(stdout+stderr, leakWord) {
			t.Errorf("%q printed the secret: %q %q", args, stdout, stderr)
		}
	}
}

// The owner credential is for `suiteward migrate` only: every other command refuses to start with it in its environment.
func TestServeSetupAndProbeRefuseTheMigrationCredential(t *testing.T) {
	env := testEnv(map[string]string{
		"SUITEWARD_DATABASE_URL":           "postgres://app@127.0.0.1:1/db?sslmode=disable",
		"SUITEWARD_MIGRATION_DATABASE_URL": "postgres://owner@127.0.0.1:1/db?sslmode=disable",
		"SUITEWARD_ARTIFACT_DIR":           t.TempDir(),
	})
	for _, args := range [][]string{{"serve"}, {"probe"}, {"setup", "--owner", "owner", "--test-check", "acme/widgets=ci / test"}} {
		code, _, stderr := run(args, env)
		if code != 1 || !strings.Contains(stderr, "the migration credential is for `suiteward migrate` only; remove it from this service's environment") {
			t.Errorf("Run(%q) = %d, stderr %q; want 1 and the refusal", args, code, stderr)
		}
	}
}

func TestServeRefusesTheMigrationCredentialFromAFile(t *testing.T) {
	env := testEnv(map[string]string{
		"SUITEWARD_DATABASE_URL":                "postgres://app@127.0.0.1:1/db?sslmode=disable",
		"SUITEWARD_MIGRATION_DATABASE_URL_FILE": "/run/secrets/database-owner-url",
		"SUITEWARD_ARTIFACT_DIR":                t.TempDir(),
	})
	env.ReadFile = func(string) ([]byte, error) { return []byte("postgres://owner@127.0.0.1:1/db?sslmode=disable"), nil }
	if code, _, stderr := run([]string{"serve"}, env); code != 1 || !strings.Contains(stderr, "the migration credential is for `suiteward migrate` only") {
		t.Fatalf("serve with the owner URL in a file exited %d, stderr %q; want 1 and the refusal", code, stderr)
	}
}
