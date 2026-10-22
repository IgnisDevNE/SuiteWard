package app

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/appauth"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/governance"
	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/artifact"
)

func TestReadDetailNamesTheKindOfAFailureAndNeverGitHubsText(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"a malformed answer", appauth.ErrMalformedAnswer, "GitHub's answer was malformed"},
		{"a list that is too long", appauth.ErrTooManyEntries, "more than 1000 entries"},
		{"a status", &rest.StatusError{Status: http.StatusBadGateway}, "(HTTP 502)"},
		{"no answer at all", errors.New("dial tcp: " + setupGitHubBody), "transport"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := readDetail(tc.err)
			if !strings.Contains(got, tc.want) || strings.Contains(got, setupGitHubBody) {
				t.Errorf("readDetail = %q, want it to contain %q and no text of the error", got, tc.want)
			}
		})
	}
}

func TestPermissionRankOrdersTheLevels(t *testing.T) {
	for level, want := range map[string]int{"read": 1, "write": 2, "admin": 3, "": 0, "none": 0} {
		if got := permissionRank(level); got != want {
			t.Errorf("permissionRank(%q) = %d, want %d", level, got, want)
		}
	}
}

func TestSelfCheckGitHubReportsRepositoriesItCannotList(t *testing.T) {
	fake := newSelfCheckGitHub(t)
	fake.repositoriesStatus = http.StatusInternalServerError

	results := selfCheckGitHubResults(t.Context(), fake.settings(), enrolledProjects)

	got := byName(t, results)["github.repositories"]
	if got.OK || !strings.Contains(got.Detail, "could not be listed") || !strings.Contains(got.Detail, "HTTP 500") || got.Fix == "" {
		t.Errorf("github.repositories = %+v; want the listing failure with its status and a fix", got)
	}
	noSecret(t, results)
	for _, name := range []string{protectionName} {
		if r := byName(t, results)[name]; r.OK || !strings.Contains(r.Detail, "not checked") {
			t.Errorf("%s = %+v; want it skipped after the repositories failed", name, r)
		}
	}
}

// A token that cannot be minted for a reason other than a missing permission still names itself and says where to look.
func TestSelfCheckGitHubPermissionsFixPointsAtTheGuideWhenNothingIsMissing(t *testing.T) {
	fake := newSelfCheckGitHub(t)
	fake.denyPermission = "checks" // the installation grants all five, and GitHub still refuses a token that names checks

	results := selfCheckGitHubResults(t.Context(), fake.settings(), enrolledProjects)

	got := byName(t, results)["github.permissions"]
	if got.OK || !strings.Contains(got.Detail, "cannot be minted") || !strings.Contains(got.Fix, "compare the App's repository permissions with the install guide") {
		t.Errorf("github.permissions = %+v; want the token named and the guide as the fix", got)
	}
	noSecret(t, results)
}

func TestNeverWritesRefusesEveryWrite(t *testing.T) {
	if err := (neverWrites{}).Verify(context.Background(), artifact.Digest{}); !errors.Is(err, errNeverWrites) {
		t.Errorf("Verify = %v", err)
	}
	if id, err := (neverWrites{}).Insert(context.Background(), nil, governance.Job{}); id != 0 || !errors.Is(err, errNeverWrites) {
		t.Errorf("Insert = %d, %v", id, err)
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("the pipe is closed") }

// Results that cannot be written are an exit code of 1 and a line on stderr: a check that prints nothing is not a pass.
func TestRunSelfCheckFailsWhenItsOutputCannotBeWritten(t *testing.T) {
	fake := newSelfCheckGitHub(t)
	env := testEnv(selfCheckVars(t, fake))
	cfg, _, err := config.Load(env.Lookup, env.Environ, env.ReadFile)
	if err != nil {
		t.Fatal(err)
	}
	var stderr strings.Builder
	if code := runSelfCheck(t.Context(), cfg, false, failingWriter{}, &stderr); code != 1 || !strings.Contains(stderr.String(), "write output") {
		t.Errorf("runSelfCheck = %d, stderr %q; want 1 and the write failure", code, stderr.String())
	}
}
