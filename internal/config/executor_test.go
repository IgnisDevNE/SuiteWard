package config

import (
	"bytes"
	"fmt"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// Built at run time so the literal is not a credential for a secret scanner to flag.
var executorToken = strings.Repeat("x", 32) + "-TOPSECRETEXECUTORTOKEN"

var defaultExecutorTimes = ExecutorTimes{JobTimeout: 30 * time.Minute, LeaseWait: 5 * time.Minute, GitHubWait: 30 * time.Minute}

func executorVars(t *testing.T) map[string]string {
	t.Helper()
	vars := required(t)
	vars["SUITEWARD_EXECUTOR_TOKEN"] = executorToken
	return vars
}

func TestLoadExecutorTokenIsAbsentWhenUnset(t *testing.T) {
	for name, set := range map[string]bool{"unset": false, "set but empty": true} {
		vars := required(t)
		if set {
			vars["SUITEWARD_EXECUTOR_TOKEN"] = ""
		}
		cfg, warnings, err := fakeEnv{vars: vars}.load()
		if err != nil || len(warnings) != 0 {
			t.Fatalf("%s: Load: warnings %v, error %v", name, warnings, err)
		}
		if cfg.ExecutorToken != "" || cfg.Executor != defaultExecutorTimes {
			t.Errorf("%s: token %q, times %+v; want no token and the default times", name, cfg.ExecutorToken.Reveal(), cfg.Executor)
		}
	}
}

func TestLoadExecutorDefaults(t *testing.T) {
	cfg, warnings, err := fakeEnv{vars: executorVars(t)}.load()
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Load: warnings %v, error %v", warnings, err)
	}
	if cfg.ExecutorToken.Reveal() != executorToken || cfg.Executor != defaultExecutorTimes {
		t.Fatalf("token %q, times %+v; want the token and the default times", cfg.ExecutorToken.Reveal(), cfg.Executor)
	}
}

func TestLoadExecutorReadsEverySetting(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_EXECUTOR_TOKEN_FILE"] = "/t"
	vars["SUITEWARD_EXECUTOR_JOB_TIMEOUT"] = "45m"
	vars["SUITEWARD_EXECUTOR_LEASE_WAIT"] = "90s"
	vars["SUITEWARD_EXECUTOR_GITHUB_WAIT"] = "2h"
	cfg, warnings, err := fakeEnv{vars: vars, files: map[string]string{"/t": executorToken + "\n"}}.load()
	if err != nil || len(warnings) != 0 {
		t.Fatalf("Load: warnings %v, error %v", warnings, err)
	}
	want := ExecutorTimes{JobTimeout: 45 * time.Minute, LeaseWait: 90 * time.Second, GitHubWait: 2 * time.Hour}
	if cfg.ExecutorToken.Reveal() != executorToken || cfg.Executor != want {
		t.Fatalf("token %q, times %+v; want the token and %+v", cfg.ExecutorToken.Reveal(), cfg.Executor, want)
	}
}

func TestLoadExecutorToken(t *testing.T) {
	for name, test := range map[string]struct {
		token   string
		wantErr bool
	}{
		"31 bytes":             {strings.Repeat("x", 31), true},
		"32 bytes":             {strings.Repeat("x", 32), false},
		"30 bytes of 15 runes": {strings.Repeat("é", 15), true},
		"32 bytes of 16 runes": {strings.Repeat("é", 16), false},
	} {
		t.Run(name, func(t *testing.T) {
			vars := required(t)
			vars["SUITEWARD_EXECUTOR_TOKEN"] = test.token
			_, _, err := fakeEnv{vars: vars}.load()
			if (err != nil) != test.wantErr {
				t.Fatalf("Load = %v, want error %v", err, test.wantErr)
			}
			if err != nil && (!strings.Contains(err.Error(), "SUITEWARD_EXECUTOR_TOKEN") || strings.Contains(err.Error(), test.token)) {
				t.Fatalf("error = %v, want it to name EXECUTOR_TOKEN without the value", err)
			}
		})
	}
}

func TestLoadExecutorTokenFileProblems(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_EXECUTOR_TOKEN_FILE"] = "/missing"
	_, _, err := fakeEnv{vars: vars}.load()
	if err == nil || !strings.Contains(err.Error(), "SUITEWARD_EXECUTOR_TOKEN_FILE") || strings.Count(err.Error(), "SUITEWARD_EXECUTOR_TOKEN") != 1 {
		t.Fatalf("Load = %v, want one error naming EXECUTOR_TOKEN_FILE", err)
	}
	vars["SUITEWARD_EXECUTOR_TOKEN"] = executorToken
	if _, _, err := (fakeEnv{vars: vars, files: map[string]string{"/missing": executorToken}}).load(); err == nil || strings.Contains(err.Error(), executorToken) {
		t.Fatalf("Load = %v, want both forms refused without the token", err)
	}
}

func TestLoadExecutorDurationRanges(t *testing.T) {
	for _, test := range []struct {
		name, lo, hi, below, above string
	}{
		{"EXECUTOR_JOB_TIMEOUT", "11m", "6h", "10m59s", "6h0m1s"},
		{"EXECUTOR_LEASE_WAIT", "10s", "1h", "9s", "1h0m1s"},
		{"EXECUTOR_GITHUB_WAIT", "1m", "6h", "59s", "6h0m1s"},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, ok := range []string{test.lo, test.hi} { // both ends are inclusive
				vars := executorVars(t)
				vars["SUITEWARD_"+test.name] = ok
				if _, _, err := (fakeEnv{vars: vars}).load(); err != nil {
					t.Errorf("%s=%s refused: %v", test.name, ok, err)
				}
			}
			for _, bad := range []string{test.below, test.above, "0s", "-1m", "soon", "30"} {
				vars := executorVars(t)
				vars["SUITEWARD_"+test.name] = bad
				_, _, err := fakeEnv{vars: vars}.load()
				if err == nil || !strings.Contains(err.Error(), "SUITEWARD_"+test.name) {
					t.Errorf("%s=%s: error = %v, want one naming the variable", test.name, bad, err)
				}
			}
		})
	}
}

func TestLoadExecutorDurationsAreValidatedWithoutAToken(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_EXECUTOR_JOB_TIMEOUT"] = "10m"
	_, _, err := fakeEnv{vars: vars}.load()
	if err == nil || !strings.Contains(err.Error(), "SUITEWARD_EXECUTOR_JOB_TIMEOUT") || strings.Contains(err.Error(), "SUITEWARD_EXECUTOR_TOKEN") {
		t.Fatalf("Load = %v, want a timeout error that does not blame the missing token", err)
	}
}

func TestLoadExecutorRejectsFileFormForDurations(t *testing.T) {
	for _, name := range []string{"JOB_TIMEOUT", "LEASE_WAIT", "GITHUB_WAIT"} {
		vars := executorVars(t)
		vars["SUITEWARD_EXECUTOR_"+name+"_FILE"] = "/f"
		_, _, err := fakeEnv{vars: vars}.load()
		if err == nil || !strings.Contains(err.Error(), "SUITEWARD_EXECUTOR_"+name+"_FILE") {
			t.Errorf("%s_FILE: error = %v, want it rejected", name, err)
		}
	}
}

func TestLoadExecutorReportsEveryProblemAtOnce(t *testing.T) {
	vars := required(t)
	vars["SUITEWARD_EXECUTOR_TOKEN"] = "short"
	vars["SUITEWARD_EXECUTOR_JOB_TIMEOUT"] = "1m"
	vars["SUITEWARD_EXECUTOR_LEASE_WAIT"] = "1s"
	vars["SUITEWARD_EXECUTOR_GITHUB_WAIT"] = "7h"
	_, _, err := fakeEnv{vars: vars}.load()
	if err == nil {
		t.Fatal("Load accepted invalid settings")
	}
	for _, name := range []string{"TOKEN", "JOB_TIMEOUT", "LEASE_WAIT", "GITHUB_WAIT"} {
		if !strings.Contains(err.Error(), "SUITEWARD_EXECUTOR_"+name) {
			t.Errorf("error does not name EXECUTOR_%s: %v", name, err)
		}
	}
	if strings.Contains(err.Error(), "short") {
		t.Errorf("error echoes the token: %v", err)
	}
}

func TestLoadExecutorTokenIsNeverRendered(t *testing.T) {
	cfg, _, err := fakeEnv{vars: executorVars(t)}.load()
	if err != nil || cfg.ExecutorToken == "" {
		t.Fatalf("Load = token set %v, %v", cfg.ExecutorToken != "", err)
	}
	renderings := map[string]string{"%v": fmt.Sprintf("%v", cfg), "%+v": fmt.Sprintf("%+v", cfg), "%#v": fmt.Sprintf("%#v", cfg)}
	var jsonOut, textOut bytes.Buffer
	slog.New(slog.NewJSONHandler(&jsonOut, nil)).Info("config", "config", cfg)
	slog.New(slog.NewTextHandler(&textOut, nil)).Info("config", "config", cfg)
	renderings["slog json"], renderings["slog text"] = jsonOut.String(), textOut.String()
	for how, out := range renderings {
		if strings.Contains(out, "TOPSECRETEXECUTORTOKEN") {
			t.Errorf("%s leaks the token: %s", how, out)
		}
	}
}
