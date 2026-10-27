package app

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

const executorToken = "an-executor-token-of-more-than-32-bytes"

// mountedFile is where a test puts the file that holds the value.
const mountedFile = "/run/files/executor"

// executorEnv is the environment of a valid executor, with the changes of a test.
func executorEnv(change map[string]string) Env {
	vars := map[string]string{"SUITEWARD_EXECUTOR_URL": "https://suiteward.example.com", "SUITEWARD_EXECUTOR_TOKEN": executorToken}
	for name, value := range change {
		if value == "" {
			delete(vars, name)
		} else {
			vars[name] = value
		}
	}
	return testEnv(vars)
}

func TestTheExecutorReadsItsThreeSettings(t *testing.T) {
	s, err := readExecutorSettings(executorEnv(nil))
	if err != nil {
		t.Fatalf("readExecutorSettings: %v", err)
	}
	if s.url != "https://suiteward.example.com" || s.token.Reveal() != executorToken || s.runtime != "podman" {
		t.Errorf("the settings are %+v; want the URL, the token and the default runtime podman", s)
	}
	s, err = readExecutorSettings(executorEnv(map[string]string{"SUITEWARD_EXECUTOR_RUNTIME": "docker", "SUITEWARD_EXECUTOR_URL": "http://127.0.0.1:8080"}))
	if err != nil || s.runtime != "docker" || s.url != "http://127.0.0.1:8080" {
		t.Errorf("settings = %+v, %v; want docker on a loopback http URL", s, err)
	}
}

func TestTheExecutorTokenMayComeFromAFile(t *testing.T) {
	env := executorEnv(map[string]string{"SUITEWARD_EXECUTOR_TOKEN": "", "SUITEWARD_EXECUTOR_TOKEN_FILE": mountedFile})
	env.ReadFile = func(path string) ([]byte, error) {
		if path != mountedFile {
			return nil, os.ErrNotExist
		}
		return []byte(executorToken + "\n"), nil
	}
	if s, err := readExecutorSettings(env); err != nil || s.token.Reveal() != executorToken {
		t.Errorf("the token from a file is %q, %v; want the file's content without its newline", s.token.Reveal(), err)
	}
}

func TestBadExecutorSettingsAreEachRefusedByName(t *testing.T) {
	short := strings.Repeat("x", 31)
	for name, tc := range map[string]struct {
		change map[string]string
		named  string
	}{
		"no URL":                  {map[string]string{"SUITEWARD_EXECUTOR_URL": ""}, "SUITEWARD_EXECUTOR_URL"},
		"a URL without a scheme":  {map[string]string{"SUITEWARD_EXECUTOR_URL": "suiteward.example.com"}, "SUITEWARD_EXECUTOR_URL"},
		"plain http off loopback": {map[string]string{"SUITEWARD_EXECUTOR_URL": "http://suiteward.example.com"}, "SUITEWARD_EXECUTOR_URL"},
		"a path":                  {map[string]string{"SUITEWARD_EXECUTOR_URL": "https://suiteward.example.com/executor"}, "SUITEWARD_EXECUTOR_URL"},
		"a query":                 {map[string]string{"SUITEWARD_EXECUTOR_URL": "https://suiteward.example.com?x=1"}, "SUITEWARD_EXECUTOR_URL"},
		"user info":               {map[string]string{"SUITEWARD_EXECUTOR_URL": "https://user:pw-" + leakWord + "@suiteward.example.com"}, "SUITEWARD_EXECUTOR_URL"},
		"no token":                {map[string]string{"SUITEWARD_EXECUTOR_TOKEN": ""}, "SUITEWARD_EXECUTOR_TOKEN"},
		"a short token":           {map[string]string{"SUITEWARD_EXECUTOR_TOKEN": short}, "SUITEWARD_EXECUTOR_TOKEN"},
		"an unknown runtime":      {map[string]string{"SUITEWARD_EXECUTOR_RUNTIME": "rkt"}, "SUITEWARD_EXECUTOR_RUNTIME"},
		"a token twice":           {map[string]string{"SUITEWARD_EXECUTOR_TOKEN_FILE": mountedFile}, "SUITEWARD_EXECUTOR_TOKEN_FILE"},
		"a token file that fails": {map[string]string{"SUITEWARD_EXECUTOR_TOKEN": "", "SUITEWARD_EXECUTOR_TOKEN_FILE": mountedFile + ".none"}, "SUITEWARD_EXECUTOR_TOKEN_FILE"},
	} {
		_, err := readExecutorSettings(executorEnv(tc.change))
		if err == nil || !strings.Contains(err.Error(), tc.named) {
			t.Errorf("%s: readExecutorSettings = %v; want an error that names %s", name, err, tc.named)
			continue
		}
		if strings.Contains(err.Error(), leakWord) || strings.Contains(err.Error(), executorToken) || strings.Contains(err.Error(), short) {
			t.Errorf("%s: the error echoes a value: %v", name, err)
		}
	}
}

func TestEveryProblemOfTheExecutorSettingsIsReportedAtOnce(t *testing.T) {
	_, err := readExecutorSettings(executorEnv(map[string]string{"SUITEWARD_EXECUTOR_URL": "ftp://x", "SUITEWARD_EXECUTOR_TOKEN": "short", "SUITEWARD_EXECUTOR_RUNTIME": "rkt"}))
	if err == nil {
		t.Fatal("bad settings were accepted")
	}
	for _, name := range []string{"SUITEWARD_EXECUTOR_URL", "SUITEWARD_EXECUTOR_TOKEN", "SUITEWARD_EXECUTOR_RUNTIME"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("the error does not name %s: %v", name, err)
		}
	}
}

// The executor never holds a credential of the service: any of these, in either form, stops it, and the error names the variable and not
// the value.
func TestTheExecutorRefusesToStartWhereTheServicesCredentialsAre(t *testing.T) {
	for _, name := range []string{
		"SUITEWARD_DATABASE_URL", "SUITEWARD_MIGRATION_DATABASE_URL", "SUITEWARD_GITHUB_APP_KEY", "SUITEWARD_MCP_TOKEN",
		"SUITEWARD_DATABASE_URL_FILE", "SUITEWARD_MIGRATION_DATABASE_URL_FILE", "SUITEWARD_GITHUB_APP_KEY_FILE", "SUITEWARD_MCP_TOKEN_FILE",
	} {
		_, err := readExecutorSettings(executorEnv(map[string]string{name: leakWord}))
		if err == nil || !strings.Contains(err.Error(), name) || strings.Contains(err.Error(), leakWord) {
			t.Errorf("with %s set, readExecutorSettings = %v; want a refusal that names the variable and not its value", name, err)
		}
	}
	if _, err := readExecutorSettings(executorEnv(map[string]string{"SUITEWARD_DATABASE_URL": "", "SUITEWARD_MCP_TOKEN_FILE": ""})); err != nil {
		t.Errorf("variables that are unset or empty are no credential: %v", err)
	}
}

// The executor reads the three settings and, to refuse them, the credentials of the service: nothing else, whatever is in the environment.
func TestTheExecutorReadsNothingElseFromTheEnvironment(t *testing.T) {
	var asked []string
	env := executorEnv(map[string]string{"SUITEWARD_EXECUTOR_RUNTIME": "docker"})
	lookup := env.Lookup
	env.Lookup = func(name string) (string, bool) { asked = append(asked, name); return lookup(name) }
	env.Environ = append(env.Environ, "SUITEWARD_NO_SUCH_SETTING=1", "SUITEWARD_JOB_WORKERS=0", "HTTPS_PROXY=http://proxy.invalid")
	if _, err := readExecutorSettings(env); err != nil {
		t.Fatalf("readExecutorSettings: %v", err)
	}
	allowed := []string{
		"SUITEWARD_EXECUTOR_URL", "SUITEWARD_EXECUTOR_TOKEN", "SUITEWARD_EXECUTOR_TOKEN_FILE", "SUITEWARD_EXECUTOR_RUNTIME",
		"SUITEWARD_DATABASE_URL", "SUITEWARD_MIGRATION_DATABASE_URL", "SUITEWARD_GITHUB_APP_KEY", "SUITEWARD_MCP_TOKEN",
		"SUITEWARD_DATABASE_URL_FILE", "SUITEWARD_MIGRATION_DATABASE_URL_FILE", "SUITEWARD_GITHUB_APP_KEY_FILE", "SUITEWARD_MCP_TOKEN_FILE",
	}
	for _, name := range asked {
		if !slices.Contains(allowed, name) {
			t.Errorf("the executor read %s", name)
		}
	}
	for _, want := range []string{"SUITEWARD_EXECUTOR_URL", "SUITEWARD_EXECUTOR_TOKEN", "SUITEWARD_EXECUTOR_RUNTIME"} {
		if !slices.Contains(asked, want) {
			t.Errorf("the executor did not read %s", want)
		}
	}
}

func TestExecuteIsRoutedBeforeAnyServiceSettingIsLoaded(t *testing.T) {
	// The service's settings need a database URL: the executor has none, and its own problems are the only ones it reports.
	code, stdout, stderr := run([]string{"execute"}, executorEnv(map[string]string{"SUITEWARD_EXECUTOR_URL": ""}))
	if code != 1 || stdout != "" || !strings.Contains(stderr, "SUITEWARD_EXECUTOR_URL") || strings.Contains(stderr, "DATABASE_URL") {
		t.Errorf("Run(execute) = %d, stdout %q, stderr %q; want 1 and the executor's own settings error alone", code, stdout, stderr)
	}
	code, _, stderr = run([]string{"execute"}, executorEnv(map[string]string{"SUITEWARD_DATABASE_URL": "postgres://u:" + leakWord + "@db/x"}))
	if code != 1 || !strings.Contains(stderr, "SUITEWARD_DATABASE_URL") || strings.Contains(stderr, leakWord) {
		t.Errorf("Run(execute) with the service's database URL = %d, stderr %q; want 1 and the variable named", code, stderr)
	}
}

func TestExecuteTakesNoArgumentAndTheUsageListsIt(t *testing.T) {
	code, stdout, stderr := run([]string{"execute", "extra"}, executorEnv(nil))
	if code != 2 || stdout != "" || !strings.Contains(stderr, "suiteward execute\n") {
		t.Errorf("Run(execute extra) = %d, stdout %q, stderr %q; want 2 and the usage with `suiteward execute`", code, stdout, stderr)
	}
}

// fakeRuntime is a container runtime for the composition of the command.
type fakeRuntime struct{ infoErr error }

func (f fakeRuntime) Info(context.Context) (string, string, error) {
	return "docker", "29.1.0", f.infoErr
}
func (fakeRuntime) Run(context.Context, sandbox.Spec) (sandbox.Outcome, error) {
	return sandbox.Outcome{}, errors.New("no container runs here")
}

func fakeDeps(rt fakeRuntime, sum string, sumErr error, swept *[]string) executorDeps {
	return executorDeps{
		runtime: func(string) sandbox.Runtime { return rt },
		sweep: func(binary string) func(context.Context) error {
			return func(context.Context) error { *swept = append(*swept, binary); return nil }
		},
		binarySum: func() (string, error) { return sum, sumErr },
	}
}

func TestTheExecutorStopsCleanlyAtTheSignalAndSweepsWithTheConfiguredRuntime(t *testing.T) {
	var swept []string
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // the signal has come: Run starts, sweeps, and leases nothing
	var out, errOut bytes.Buffer
	env := executorEnv(map[string]string{"SUITEWARD_EXECUTOR_URL": "http://127.0.0.1:1", "SUITEWARD_EXECUTOR_RUNTIME": "docker"})
	code := executeWith(ctx, nil, env, &out, &errOut, fakeDeps(fakeRuntime{}, strings.Repeat("ab", 32), nil, &swept))
	if code != 0 || errOut.Len() != 0 {
		t.Errorf("executeWith = %d, stderr %q; want a clean stop", code, errOut.String())
	}
	if !slices.Equal(swept, []string{"docker"}) {
		t.Errorf("the sweep ran with %v; want the configured runtime once", swept)
	}
	if strings.Contains(out.String(), executorToken) {
		t.Errorf("the log holds the token: %s", out.String())
	}
}

func TestWithoutARuntimeOrABinaryTheExecutorExitsWithOne(t *testing.T) {
	env := executorEnv(map[string]string{"SUITEWARD_EXECUTOR_URL": "http://127.0.0.1:1"})
	var swept []string
	var out, errOut bytes.Buffer
	if code := executeWith(context.Background(), nil, env, &out, &errOut, fakeDeps(fakeRuntime{infoErr: errors.New("no engine")}, strings.Repeat("ab", 32), nil, &swept)); code != 1 || !strings.Contains(errOut.String(), "container runtime") {
		t.Errorf("without a runtime: %d, stderr %q; want 1 and the runtime named", code, errOut.String())
	}
	errOut.Reset()
	if code := executeWith(context.Background(), nil, env, &out, &errOut, fakeDeps(fakeRuntime{}, "", errors.New("no binary"), &swept)); code != 1 || !strings.Contains(errOut.String(), "binary") {
		t.Errorf("without a binary: %d, stderr %q; want 1 and the binary named", code, errOut.String())
	}
}

func TestTheDigestOfAFileIsItsSHA256(t *testing.T) {
	path := filepath.Join(t.TempDir(), "binary")
	if err := os.WriteFile(path, []byte("an executable"), 0o600); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte("an executable"))
	if got, err := fileSum(path); err != nil || got != hex.EncodeToString(sum[:]) {
		t.Errorf("fileSum = %q, %v; want %x", got, err, sum)
	}
	if _, err := fileSum(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Error("fileSum of a missing file succeeded")
	}
}
