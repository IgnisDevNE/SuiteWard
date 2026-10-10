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
		"SUITEWARD_DATABASE_URL": "mysql://user:s3cret-pw@db/suiteward",
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
		if strings.Contains(stdout+stderr, "s3cret-pw") {
			t.Errorf("%s printed the secret: %q %q", command, stdout, stderr)
		}
		if warned := strings.Contains(stdout, "SUITEWARD_DATABSE_URL"); warned != (command == "serve") {
			t.Errorf("%s logged the unrecognized setting = %v; only serve logs it: %q", command, warned, stdout)
		}
	}
}

func TestUnreachableDatabaseIsFatalAndLogged(t *testing.T) {
	env := testEnv(map[string]string{
		"SUITEWARD_DATABASE_URL": "postgres://suiteward:s3cret-pw@127.0.0.1:1/suiteward?sslmode=disable",
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
		if strings.Contains(stdout+stderr, "s3cret-pw") {
			t.Errorf("%q printed the secret: %q %q", args, stdout, stderr)
		}
	}
}
