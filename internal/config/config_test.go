package config

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

const (
	secretPassword = "s3cretPW"
	validURL       = "postgres://suiteward:" + secretPassword + "@suiteward-db:5432/suiteward"
)

// fakeEnv is an injected environment: variables and files.
type fakeEnv struct {
	vars  map[string]string
	files map[string]string
}

func (f fakeEnv) load() (Config, []string, error) {
	environ := make([]string, 0, len(f.vars))
	for k, v := range f.vars {
		environ = append(environ, k+"="+v)
	}
	lookup := func(k string) (string, bool) { v, ok := f.vars[k]; return v, ok }
	readFile := func(p string) ([]byte, error) {
		c, ok := f.files[p]
		if !ok {
			return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
		}
		return []byte(c), nil
	}
	return Load(lookup, environ, readFile)
}

func absDir(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "artifacts")
}

func required(t *testing.T) map[string]string {
	t.Helper()
	return map[string]string{
		"SUITEWARD_DATABASE_URL": validURL,
		"SUITEWARD_ARTIFACT_DIR": absDir(t),
	}
}

// withoutURL returns the required settings with the database URL given as a file.
func withoutURL(t *testing.T) map[string]string {
	t.Helper()
	vars := required(t)
	delete(vars, "SUITEWARD_DATABASE_URL")
	vars["SUITEWARD_DATABASE_URL_FILE"] = "/s"
	return vars
}

func TestLoadAppliesDefaults(t *testing.T) {
	vars := required(t)
	cfg, warnings, err := fakeEnv{vars: vars}.load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %v", warnings)
	}
	want := Config{
		DatabaseURL:        validURL,
		ArtifactDir:        vars["SUITEWARD_ARTIFACT_DIR"],
		HTTPAddr:           "127.0.0.1:8080",
		ShutdownTimeout:    30 * time.Second,
		LogLevel:           slog.LevelInfo,
		JobWorkers:         4,
		JobTimeout:         time.Minute,
		JobMaxAttempts:     5,
		OutboxMaxAttempts:  8,
		OutboxPollInterval: 5 * time.Second,
		OutboxLease:        time.Minute,
	}
	if cfg != want {
		t.Fatalf("Config = %+v, want %+v", cfg, want)
	}
}

func TestLoadReadsEverySetting(t *testing.T) {
	vars := required(t)
	for k, v := range map[string]string{
		"SUITEWARD_HTTP_ADDR":            "0.0.0.0:9090",
		"SUITEWARD_SHUTDOWN_TIMEOUT":     "45s",
		"SUITEWARD_LOG_LEVEL":            "debug",
		"SUITEWARD_JOB_WORKERS":          "8",
		"SUITEWARD_JOB_TIMEOUT":          "2m",
		"SUITEWARD_JOB_MAX_ATTEMPTS":     "3",
		"SUITEWARD_OUTBOX_MAX_ATTEMPTS":  "12",
		"SUITEWARD_OUTBOX_POLL_INTERVAL": "10s",
		"SUITEWARD_OUTBOX_LEASE":         "90s",
	} {
		vars[k] = v
	}
	cfg, _, err := fakeEnv{vars: vars}.load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		DatabaseURL:        validURL,
		ArtifactDir:        vars["SUITEWARD_ARTIFACT_DIR"],
		HTTPAddr:           "0.0.0.0:9090",
		ShutdownTimeout:    45 * time.Second,
		LogLevel:           slog.LevelDebug,
		JobWorkers:         8,
		JobTimeout:         2 * time.Minute,
		JobMaxAttempts:     3,
		OutboxMaxAttempts:  12,
		OutboxPollInterval: 10 * time.Second,
		OutboxLease:        90 * time.Second,
	}
	if cfg != want {
		t.Fatalf("Config = %+v, want %+v", cfg, want)
	}
}

func TestLoadLogLevels(t *testing.T) {
	for name, want := range map[string]slog.Level{
		"debug": slog.LevelDebug, "info": slog.LevelInfo, "warn": slog.LevelWarn, "error": slog.LevelError,
	} {
		vars := required(t)
		vars["SUITEWARD_LOG_LEVEL"] = name
		cfg, _, err := fakeEnv{vars: vars}.load()
		if err != nil || cfg.LogLevel != want {
			t.Errorf("LOG_LEVEL=%s: level %v err %v, want %v", name, cfg.LogLevel, err, want)
		}
	}
}

func TestLoadRequiredSettingsMissing(t *testing.T) {
	_, _, err := fakeEnv{}.load()
	if err == nil {
		t.Fatal("Load succeeded without required settings")
	}
	for _, name := range []string{"SUITEWARD_DATABASE_URL", "SUITEWARD_ARTIFACT_DIR"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error %q does not name %s", err, name)
		}
	}
}

func TestLoadEmptyRequiredSettingIsMissing(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_DATABASE_URL"] = ""
	_, _, err := fakeEnv{vars: vars}.load()
	if err == nil || !strings.Contains(err.Error(), "SUITEWARD_DATABASE_URL") {
		t.Fatalf("error = %v, want one naming SUITEWARD_DATABASE_URL", err)
	}
}

func TestLoadReportsAllInvalidValuesAndNeverTheSecret(t *testing.T) {
	cases := map[string]string{
		"SUITEWARD_DATABASE_URL":         "postgres://suiteward:" + secretPassword + "@host:notaport/db",
		"SUITEWARD_ARTIFACT_DIR":         "relative/dir",
		"SUITEWARD_HTTP_ADDR":            "no-port",
		"SUITEWARD_SHUTDOWN_TIMEOUT":     "soon",
		"SUITEWARD_LOG_LEVEL":            "verbose",
		"SUITEWARD_JOB_WORKERS":          "0",
		"SUITEWARD_JOB_TIMEOUT":          "0s",
		"SUITEWARD_JOB_MAX_ATTEMPTS":     "26",
		"SUITEWARD_OUTBOX_MAX_ATTEMPTS":  "many",
		"SUITEWARD_OUTBOX_POLL_INTERVAL": "2h",
		"SUITEWARD_OUTBOX_LEASE":         "1s",
	}
	_, _, err := fakeEnv{vars: cases}.load()
	if err == nil {
		t.Fatal("Load accepted invalid settings")
	}
	for name := range cases {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
	if strings.Contains(err.Error(), secretPassword) {
		t.Errorf("error leaks the database password: %v", err)
	}
}

func TestLoadRangesAreInclusive(t *testing.T) {
	vars := required(t)
	for k, v := range map[string]string{
		"SUITEWARD_SHUTDOWN_TIMEOUT":     "10m",
		"SUITEWARD_JOB_WORKERS":          "64",
		"SUITEWARD_JOB_MAX_ATTEMPTS":     "25",
		"SUITEWARD_OUTBOX_MAX_ATTEMPTS":  "1",
		"SUITEWARD_OUTBOX_POLL_INTERVAL": "1h",
		"SUITEWARD_OUTBOX_LEASE":         "5s",
	} {
		vars[k] = v
	}
	if _, _, err := (fakeEnv{vars: vars}).load(); err != nil {
		t.Fatalf("boundary values rejected: %v", err)
	}
	vars["SUITEWARD_SHUTDOWN_TIMEOUT"] = "999ms"
	vars["SUITEWARD_OUTBOX_POLL_INTERVAL"] = "999ms"
	vars["SUITEWARD_OUTBOX_LEASE"] = "4s"
	vars["SUITEWARD_JOB_WORKERS"] = "65"
	_, _, err := fakeEnv{vars: vars}.load()
	if err == nil {
		t.Fatal("values just outside the ranges were accepted")
	}
	for _, name := range []string{"SHUTDOWN_TIMEOUT", "OUTBOX_POLL_INTERVAL", "OUTBOX_LEASE", "JOB_WORKERS"} {
		if !strings.Contains(err.Error(), "SUITEWARD_"+name) {
			t.Errorf("error does not name SUITEWARD_%s: %v", name, err)
		}
	}
}

func TestLoadDatabaseURLMustBeAPostgresURL(t *testing.T) {
	for _, bad := range []string{"not a url", "http://host/db", "host=db user=x password=" + secretPassword} {
		vars := required(t)
		vars["SUITEWARD_DATABASE_URL"] = bad
		_, _, err := fakeEnv{vars: vars}.load()
		if err == nil || !strings.Contains(err.Error(), "SUITEWARD_DATABASE_URL") {
			t.Errorf("%q: error = %v, want one naming SUITEWARD_DATABASE_URL", bad, err)
		}
		if err != nil && strings.Contains(err.Error(), secretPassword) {
			t.Errorf("%q: error leaks the password: %v", bad, err)
		}
	}
	for _, good := range []string{"postgres://u:p@h/db", "postgresql://u@h:5432/db?sslmode=disable"} {
		vars := required(t)
		vars["SUITEWARD_DATABASE_URL"] = good
		if _, _, err := (fakeEnv{vars: vars}).load(); err != nil {
			t.Errorf("%q rejected: %v", good, err)
		}
	}
}

func TestLoadSecretFromFile(t *testing.T) {
	for name, content := range map[string]string{"plain": validURL, "newline": validURL + "\n", "crlf": validURL + "\r\n"} {
		cfg, _, err := fakeEnv{vars: withoutURL(t), files: map[string]string{"/s": content}}.load()
		if err != nil {
			t.Fatalf("%s: Load: %v", name, err)
		}
		if cfg.DatabaseURL.Reveal() != validURL {
			t.Errorf("%s: DatabaseURL = %q, want the trimmed file content", name, cfg.DatabaseURL.Reveal())
		}
	}
}

func TestLoadTrimsOnlyOneTrailingNewline(t *testing.T) {
	cfg, _, err := fakeEnv{vars: withoutURL(t), files: map[string]string{"/s": validURL + "\n\n"}}.load()
	if err == nil {
		t.Fatalf("a second trailing newline was trimmed: %q", cfg.DatabaseURL.Reveal())
	}
}

func TestLoadSecretFileProblemsNameTheVariableNotTheContent(t *testing.T) {
	for name, files := range map[string]map[string]string{
		"unreadable": {},
		"empty":      {"/s": "\n"},
	} {
		_, _, err := fakeEnv{vars: withoutURL(t), files: files}.load()
		if err == nil || !strings.Contains(err.Error(), "SUITEWARD_DATABASE_URL_FILE") {
			t.Errorf("%s: error = %v, want one naming SUITEWARD_DATABASE_URL_FILE", name, err)
		}
	}
	files := map[string]string{"/s": "postgres://u:" + secretPassword + "@h:bad/db"}
	_, _, err := fakeEnv{vars: withoutURL(t), files: files}.load()
	if err == nil || strings.Contains(err.Error(), secretPassword) {
		t.Fatalf("invalid file content: error = %v, want one without the password", err)
	}
}

func TestLoadReadFileErrorIsWrapped(t *testing.T) {
	_, _, err := fakeEnv{vars: withoutURL(t)}.load()
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("error = %v, want one wrapping fs.ErrNotExist", err)
	}
}

func TestLoadRejectsBothSecretForms(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_DATABASE_URL_FILE"] = "/s"
	_, _, err := fakeEnv{vars: vars, files: map[string]string{"/s": validURL}}.load()
	if err == nil {
		t.Fatal("setting both forms was accepted")
	}
	for _, name := range []string{"SUITEWARD_DATABASE_URL", "SUITEWARD_DATABASE_URL_FILE"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
	}
}

func TestLoadRejectsFileFormForNonSecretSettings(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_LOG_LEVEL_FILE"] = "/level"
	vars["SUITEWARD_ARTIFACT_DIR_FILE"] = "/dir"
	_, warnings, err := fakeEnv{vars: vars, files: map[string]string{"/dir": absDir(t)}}.load()
	if err == nil {
		t.Fatal("_FILE form of a non-secret setting was accepted")
	}
	for _, name := range []string{"SUITEWARD_ARTIFACT_DIR_FILE", "SUITEWARD_LOG_LEVEL_FILE"} {
		if !strings.Contains(err.Error(), name) {
			t.Errorf("error does not name %s: %v", name, err)
		}
		if slices.ContainsFunc(warnings, func(w string) bool { return strings.Contains(w, name) }) {
			t.Errorf("%s also produced an unrecognized-name warning: %v", name, warnings)
		}
	}
}

func TestLoadWarnsAboutUnrecognizedNames(t *testing.T) {
	vars := required(t)
	for _, k := range []string{
		"SUITEWARD_TYPO", "SUITEWARD_JOB_WORKER", "SUITEWARD_TEST_DATABASE_URL", "SUITEWARD_TOOLS_DIR",
		"SUITEWARD_HOOK_X", "SUITEWARD_DATABASE_URL_FILE_X", "OTHER_VAR",
	} {
		vars[k] = "x"
	}
	_, warnings, err := fakeEnv{vars: vars}.load()
	if err != nil {
		t.Fatalf("unrecognized names must not be errors: %v", err)
	}
	want := []string{
		"SUITEWARD_DATABASE_URL_FILE_X",
		"SUITEWARD_JOB_WORKER",
		"SUITEWARD_TYPO",
	}
	if !slices.Equal(warnings, want) {
		t.Fatalf("warnings = %q, want %q", warnings, want)
	}
}

func TestLoadWarningsNeverCarryValues(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_TYPO"] = secretPassword
	_, warnings, err := fakeEnv{vars: vars}.load()
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 1 || strings.Contains(warnings[0], secretPassword) {
		t.Fatalf("warnings = %v, want one without the value", warnings)
	}
}

func TestLoadIgnoresEnvironEntriesOutsideTheNamespace(t *testing.T) {
	vars := required(t)
	lookup := func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
	_, warnings, err := Load(lookup, []string{"NOEQUALS", "=C:=x", "PATH=/bin"}, func(string) ([]byte, error) { return nil, fs.ErrNotExist })
	if err != nil || len(warnings) != 0 {
		t.Fatalf("err %v warnings %v", err, warnings)
	}
}

func TestConfigNeverRendersTheDatabaseURL(t *testing.T) {
	cfg, _, err := fakeEnv{vars: required(t)}.load()
	if err != nil {
		t.Fatal(err)
	}
	renderings := map[string]string{
		"%v":  fmt.Sprintf("%v", cfg),
		"%+v": fmt.Sprintf("%+v", cfg),
		"%#v": fmt.Sprintf("%#v", cfg),
		"%s":  cfg.DatabaseURL.String(),
	}
	var jsonOut, textOut bytes.Buffer
	slog.New(slog.NewJSONHandler(&jsonOut, nil)).Info("config", "config", cfg)
	slog.New(slog.NewTextHandler(&textOut, nil)).Info("config", "config", cfg, "url", cfg.DatabaseURL)
	renderings["slog json"] = jsonOut.String()
	renderings["slog text"] = textOut.String()
	for how, out := range renderings {
		if strings.Contains(out, secretPassword) || strings.Contains(out, "suiteward-db") {
			t.Errorf("%s leaks the database URL: %s", how, out)
		}
	}
	if cfg.DatabaseURL.Reveal() != validURL {
		t.Errorf("Reveal = %q", cfg.DatabaseURL.Reveal())
	}
}

func TestOSWiresTheProcessEnvironment(t *testing.T) {
	t.Setenv("SUITEWARD_TEST_CONFIG_OS", "wired")
	lookup, environ, readFile := OS()
	if v, ok := lookup("SUITEWARD_TEST_CONFIG_OS"); !ok || v != "wired" {
		t.Errorf("lookup = %q, %v", v, ok)
	}
	if !slices.Contains(environ, "SUITEWARD_TEST_CONFIG_OS=wired") {
		t.Error("environ lacks the variable")
	}
	if _, err := readFile(filepath.Join(t.TempDir(), "absent")); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("readFile error = %v", err)
	}
}

func TestLoadRejectsMissingDependencies(t *testing.T) {
	lookup := func(string) (string, bool) { return "", false }
	readFile := func(string) ([]byte, error) { return nil, fs.ErrNotExist }
	if _, _, err := Load(nil, nil, readFile); err == nil || !strings.Contains(err.Error(), "lookup") {
		t.Errorf("nil lookup: error = %v, want one naming lookup", err)
	}
	if _, _, err := Load(lookup, nil, nil); err == nil || !strings.Contains(err.Error(), "readFile") {
		t.Errorf("nil readFile: error = %v, want one naming readFile", err)
	}
}

func TestLoadFileReadErrorNeverCarriesThePath(t *testing.T) {
	// A misconfigured operator may put the secret itself in the _FILE variable.
	vars := withoutURL(t)
	vars["SUITEWARD_DATABASE_URL_FILE"] = validURL
	_, _, err := fakeEnv{vars: vars}.load()
	if err == nil || strings.Contains(err.Error(), secretPassword) {
		t.Fatalf("error = %v, want one without the password", err)
	}
	if !errors.Is(err, fs.ErrNotExist) || !strings.Contains(err.Error(), "SUITEWARD_DATABASE_URL_FILE") {
		t.Fatalf("error = %v, want one naming the variable and wrapping fs.ErrNotExist", err)
	}
}

func TestLoadEmptyVariablesCountAsUnset(t *testing.T) {
	files := map[string]string{"/s": validURL}

	vars := required(t)
	vars["SUITEWARD_DATABASE_URL"] = ""
	vars["SUITEWARD_DATABASE_URL_FILE"] = "/s"
	if cfg, _, err := (fakeEnv{vars: vars, files: files}).load(); err != nil || cfg.DatabaseURL.Reveal() != validURL {
		t.Errorf("empty plain form next to a file: url %q err %v", cfg.DatabaseURL.Reveal(), err)
	}

	vars = required(t)
	vars["SUITEWARD_DATABASE_URL_FILE"] = ""
	if cfg, _, err := (fakeEnv{vars: vars, files: files}).load(); err != nil || cfg.DatabaseURL.Reveal() != validURL {
		t.Errorf("empty file form next to a value: url %q err %v", cfg.DatabaseURL.Reveal(), err)
	}

	vars = withoutURL(t)
	vars["SUITEWARD_DATABASE_URL_FILE"] = ""
	_, _, err := fakeEnv{vars: vars, files: files}.load()
	if err == nil || !strings.Contains(err.Error(), "SUITEWARD_DATABASE_URL: is required") {
		t.Errorf("empty file form alone: error = %v, want the setting reported as required", err)
	}

	vars = required(t)
	vars["SUITEWARD_LOG_LEVEL_FILE"] = ""
	if _, _, err := (fakeEnv{vars: vars}).load(); err != nil {
		t.Errorf("empty _FILE of a non-secret setting: %v", err)
	}

	vars = required(t)
	vars["SUITEWARD_LOG_LEVEL"] = ""
	vars["SUITEWARD_JOB_WORKERS"] = ""
	vars["SUITEWARD_JOB_TIMEOUT"] = ""
	cfg, _, err := fakeEnv{vars: vars}.load()
	if err != nil || cfg.LogLevel != slog.LevelInfo || cfg.JobWorkers != 4 || cfg.JobTimeout != time.Minute {
		t.Errorf("empty optional settings: %+v err %v, want the defaults", cfg, err)
	}
}

func TestLoadJobTimeoutRange(t *testing.T) {
	for value, ok := range map[string]bool{"1s": true, "1h": true, "999ms": false, "0s": false, "-1m": false, "61m": false} {
		vars := required(t)
		vars["SUITEWARD_JOB_TIMEOUT"] = value
		_, _, err := fakeEnv{vars: vars}.load()
		if (err == nil) != ok {
			t.Errorf("JOB_TIMEOUT=%s: error = %v, want accepted=%v", value, err, ok)
			continue
		}
		if err != nil && !strings.Contains(err.Error(), "SUITEWARD_JOB_TIMEOUT") {
			t.Errorf("JOB_TIMEOUT=%s: error does not name the variable: %v", value, err)
		}
	}
}

func TestLoadReturnsWarningsTogetherWithAnError(t *testing.T) {
	vars := map[string]string{"SUITEWARD_TYPO": "x"}
	cfg, warnings, err := fakeEnv{vars: vars}.load()
	if err == nil {
		t.Fatal("Load succeeded without required settings")
	}
	if !slices.Equal(warnings, []string{"SUITEWARD_TYPO"}) {
		t.Errorf("warnings = %q, want the unrecognized name alongside the error", warnings)
	}
	if cfg != (Config{}) {
		t.Errorf("Config = %+v, want the zero value on error", cfg)
	}
}
