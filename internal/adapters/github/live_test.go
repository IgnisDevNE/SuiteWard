//go:build live

package github_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/appauth"
	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/rest"
	"github.com/IgnisDevNE/SuiteWard/internal/application/protection"
)

// readPermissions is the map internal/app (composeGitHub) requests; keep them equal.
// A successful Token call proves GitHub granted exactly this map (appauth fails with
// ErrPermissionsMismatch otherwise).
var readPermissions = map[string]string{"metadata": "read", "pull_requests": "read", "contents": "read"}

func liveEnv(t *testing.T, name string) string {
	t.Helper()
	v := os.Getenv(name)
	if v == "" {
		t.Fatalf("%s is required by -tags=live", name)
	}
	return v
}

func stepCtx(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

// TestLive runs the production GitHub adapter against the real App on a private
// repository. It logs only statuses, counts and the token identity, never a token,
// key or response body.
func TestLive(t *testing.T) {
	appID := liveInt(t, "SUITEWARD_TEST_GITHUB_APP_ID")
	installationID := liveInt(t, "SUITEWARD_TEST_GITHUB_INSTALLATION_ID")
	keyFile := liveEnv(t, "SUITEWARD_TEST_GITHUB_APP_KEY_FILE")
	repo := liveEnv(t, "SUITEWARD_TEST_GITHUB_REPO")
	apiURL := os.Getenv("SUITEWARD_TEST_GITHUB_API_URL")
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	_, name, ok := strings.Cut(repo, "/")
	if !ok {
		t.Fatal("SUITEWARD_TEST_GITHUB_REPO must be owner/name")
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("read SUITEWARD_TEST_GITHUB_APP_KEY_FILE: %v", err)
	}
	app, err := appauth.NewApp(appID, key, time.Now)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	httpClient := &http.Client{Timeout: 20 * time.Second}
	budget, err := rest.NewBudget(0.5, time.Now)
	if err != nil {
		t.Fatalf("NewBudget: %v", err)
	}
	newClient := func(t *testing.T, permissions map[string]string) (*appauth.Installation, *rest.Client) {
		inst, err := app.Installation(installationID, []string{name}, permissions, httpClient, apiURL)
		if err != nil {
			t.Fatalf("Installation: %v", err)
		}
		client, err := rest.NewClient(func(ctx context.Context) (string, string, error) {
			tok, err := inst.Token(ctx)
			if err != nil {
				return "", "", err
			}
			return tok.Reveal(), tok.Identity(), nil
		}, httpClient, apiURL, budget)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		return inst, client
	}
	inst, client := newClient(t, readPermissions)
	reader, err := github.NewReader(client)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}

	var number int
	var sha string

	t.Run("token", func(t *testing.T) {
		tok, err := inst.Token(stepCtx(t))
		if err != nil {
			t.Fatalf("Token (also fails when granted permissions differ from %v): %s", readPermissions, failure(err))
		}
		t.Logf("token identity %s", tok.Identity())
	})

	t.Run("open pulls", func(t *testing.T) {
		pulls, _, err := reader.OpenPulls(stepCtx(t), repo)
		if err != nil {
			t.Fatalf("OpenPulls: %s", failure(err))
		}
		t.Logf("%d open pulls", len(pulls))
	})

	t.Run("pull by number", func(t *testing.T) {
		ctx := stepCtx(t)
		list := getJSON[[]struct {
			Number int `json:"number"`
		}](t, ctx, client, "/repos/"+repo+"/pulls?state=all&per_page=1")
		if len(list) == 0 {
			t.Fatalf("%s has no pull request; open one so the pull and comment reads can be checked", repo)
		}
		number = list[0].Number
		resp, err := client.Get(ctx, "/repos/"+repo+"/pulls/"+strconv.Itoa(number))
		if err != nil {
			t.Fatalf("GET pull %d: %s", number, failure(err))
		}
		if resp.Status != http.StatusOK {
			t.Fatalf("GET pull %d answered %d, want 200", number, resp.Status)
		}
		t.Logf("GET pull %d answered %d", number, resp.Status)
	})

	t.Run("commit read", func(t *testing.T) {
		ctx := stepCtx(t)
		info := getJSON[struct {
			DefaultBranch string `json:"default_branch"`
		}](t, ctx, client, "/repos/"+repo)
		if info.DefaultBranch == "" {
			t.Fatal("the repository has no default_branch")
		}
		head := getJSON[struct {
			SHA string `json:"sha"`
		}](t, ctx, client, "/repos/"+repo+"/commits/"+info.DefaultBranch)
		if head.SHA == "" {
			t.Fatal("the default branch has no head commit sha")
		}
		sha = head.SHA
		resp, err := client.Get(ctx, "/repos/"+repo+"/commits/"+sha)
		if err != nil {
			t.Fatalf("GET commit %s: %s", sha, failure(err))
		}
		if resp.Status != http.StatusOK {
			t.Fatalf("GET commit %s answered %d, want 200", sha, resp.Status)
		}
		t.Logf("GET commit %s answered %d", sha, resp.Status)
	})

	t.Run("tree", func(t *testing.T) {
		if sha == "" {
			t.Fatal("no commit sha: the commit read step failed")
		}
		ctx := stepCtx(t)
		tree, err := reader.Tree(ctx, repo, sha)
		if err != nil {
			t.Fatalf("Tree: %s", failure(err))
		}
		entries, err := tree.Entries(ctx)
		if err != nil {
			t.Fatalf("Entries: %v", err)
		}
		if len(entries) == 0 {
			t.Fatal("the tree of the default branch head is empty")
		}
		t.Logf("tree has %d entries", len(entries))
	})

	t.Run("comments", func(t *testing.T) {
		if number == 0 {
			t.Fatal("no pull number: the pull by number step failed")
		}
		comments, _, err := reader.PullComments(stepCtx(t), repo, number, time.Time{})
		if err != nil {
			t.Fatalf("PullComments: %s", failure(err))
		}
		t.Logf("pull %d has %d comments", number, len(comments))
	})

	t.Run("narrowed negative check", func(t *testing.T) {
		if number == 0 || sha == "" {
			t.Fatal("no pull number or commit sha: an earlier step failed")
		}
		_, narrow := newClient(t, map[string]string{"metadata": "read"})
		for _, path := range []string{"/repos/" + repo + "/pulls/" + strconv.Itoa(number), "/repos/" + repo + "/commits/" + sha} {
			resp, err := narrow.Get(stepCtx(t), path)
			if err == nil {
				t.Errorf("metadata-only token on %s answered %d, want a 403 or 404", path, resp.Status)
				continue
			}
			var se *rest.StatusError
			if !errors.As(err, &se) || (se.Status != http.StatusForbidden && se.Status != http.StatusNotFound) {
				t.Errorf("metadata-only token on %s: want a 403 or 404 StatusError, got %s", path, failure(err))
				continue
			}
			t.Logf("metadata-only token on %s answered %d", path, se.Status)
		}
	})
}

// TestLiveProtectionReads measures which protection endpoints the production read
// permissions can reach, for each repository in SUITEWARD_TEST_GITHUB_REPOS
// (owner/name, comma separated). It is a measurement, not a contract: 401, 403 and
// 404 are recorded outcomes, only other statuses and transport errors fail a step.
// It logs statuses, counts and shapes, never a token, key or response body.
func TestLiveProtectionReads(t *testing.T) {
	appID := liveInt(t, "SUITEWARD_TEST_GITHUB_APP_ID")
	installationID := liveInt(t, "SUITEWARD_TEST_GITHUB_INSTALLATION_ID")
	keyFile := liveEnv(t, "SUITEWARD_TEST_GITHUB_APP_KEY_FILE")
	repos := strings.Split(liveEnv(t, "SUITEWARD_TEST_GITHUB_REPOS"), ",")
	ownerLogin := os.Getenv("SUITEWARD_TEST_GITHUB_OWNER_LOGIN")
	apiURL := os.Getenv("SUITEWARD_TEST_GITHUB_API_URL")
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("read SUITEWARD_TEST_GITHUB_APP_KEY_FILE: %v", err)
	}
	app, err := appauth.NewApp(appID, key, time.Now)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	httpClient := &http.Client{Timeout: 20 * time.Second}
	budget, err := rest.NewBudget(0.5, time.Now)
	if err != nil {
		t.Fatalf("NewBudget: %v", err)
	}
	newClient := func(t *testing.T, name string, permissions map[string]string) (*appauth.Installation, *rest.Client) {
		inst, err := app.Installation(installationID, []string{name}, permissions, httpClient, apiURL)
		if err != nil {
			t.Fatalf("Installation: %v", err)
		}
		client, err := rest.NewClient(func(ctx context.Context) (string, string, error) {
			tok, err := inst.Token(ctx)
			if err != nil {
				return "", "", err
			}
			return tok.Reveal(), tok.Identity(), nil
		}, httpClient, apiURL, budget)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		return inst, client
	}

	for i, repo := range repos {
		repos[i] = strings.TrimSpace(repo)
		if _, _, ok := strings.Cut(repos[i], "/"); !ok {
			t.Fatal("SUITEWARD_TEST_GITHUB_REPOS must be a comma-separated owner/name list")
		}
	}
	// The second map is a copy: readPermissions must stay equal to production.
	withAdministration := map[string]string{"administration": "read"}
	for k, v := range readPermissions {
		withAdministration[k] = v
	}
	maps := []struct {
		label       string
		permissions map[string]string
	}{{"read", readPermissions}, {"read+administration", withAdministration}}
	for _, repo := range repos {
		_, name, _ := strings.Cut(repo, "/")
		t.Run(repo, func(t *testing.T) {
			for _, m := range maps {
				t.Run(m.label, func(t *testing.T) {
					inst, client := newClient(t, name, m.permissions)
					if _, err := inst.Token(stepCtx(t)); err != nil {
						t.Fatalf("Token (also fails when granted permissions differ from %v): %s", m.permissions, failure(err))
					}

					var info struct {
						Private       bool   `json:"private"`
						Visibility    string `json:"visibility"`
						DefaultBranch string `json:"default_branch"`
					}
					if !probe(t, client, "/repos/"+repo, &info) || info.DefaultBranch == "" {
						t.Fatal("no default branch: the other steps are skipped")
					}
					t.Logf("private %t, visibility %q, default branch %q", info.Private, info.Visibility, info.DefaultBranch)
					branch := info.DefaultBranch

					var rules []struct {
						Type              string `json:"type"`
						RulesetSourceType string `json:"ruleset_source_type"`
						Parameters        struct {
							Strict *bool `json:"strict_required_status_checks_policy"`
							Checks []struct {
								Context       string `json:"context"`
								IntegrationID *int64 `json:"integration_id"`
							} `json:"required_status_checks"`
						} `json:"parameters"`
					}
					if probe(t, client, "/repos/"+repo+"/rules/branches/"+branch, &rules) {
						t.Logf("%d effective rules", len(rules))
						for _, r := range rules {
							t.Logf("rule type %q, ruleset source type %q", r.Type, r.RulesetSourceType)
							if r.Type != "required_status_checks" {
								continue
							}
							t.Logf("  strict policy %s", optionalBool(r.Parameters.Strict))
							for _, c := range r.Parameters.Checks {
								t.Logf("  required check %q, integration id %s", c.Context, optionalID(c.IntegrationID))
							}
						}
					}

					var rulesets []struct {
						ID int64 `json:"id"`
					}
					if probe(t, client, "/repos/"+repo+"/rulesets?includes_parents=true", &rulesets) {
						t.Logf("%d rulesets", len(rulesets))
						for _, rs := range rulesets {
							var detail struct {
								Enforcement          string  `json:"enforcement"`
								Target               string  `json:"target"`
								CurrentUserCanBypass *string `json:"current_user_can_bypass"`
								BypassActors         []struct {
									ActorID    *int64 `json:"actor_id"`
									ActorType  string `json:"actor_type"`
									BypassMode string `json:"bypass_mode"`
								} `json:"bypass_actors"`
							}
							if !probe(t, client, "/repos/"+repo+"/rulesets/"+strconv.FormatInt(rs.ID, 10), &detail) {
								continue
							}
							t.Logf("ruleset %d: enforcement %q, target %q, %d bypass actors", rs.ID, detail.Enforcement, detail.Target, len(detail.BypassActors))
							t.Logf("  current_user_can_bypass %s", bypassEnum(detail.CurrentUserCanBypass))
							for _, b := range detail.BypassActors {
								t.Logf("  bypass actor id %s, type %q, mode %q", optionalID(b.ActorID), b.ActorType, b.BypassMode)
							}
						}
					}

					var protection struct {
						RequiredStatusChecks *struct {
							Strict bool `json:"strict"`
							Checks []struct {
								Context string `json:"context"`
								AppID   *int64 `json:"app_id"`
							} `json:"checks"`
						} `json:"required_status_checks"`
						EnforceAdmins *struct {
							Enabled bool `json:"enabled"`
						} `json:"enforce_admins"`
						RequiredPullRequestReviews *struct{} `json:"required_pull_request_reviews"`
						AllowForcePushes           *struct {
							Enabled bool `json:"enabled"`
						} `json:"allow_force_pushes"`
						Restrictions *struct {
							Users []json.RawMessage `json:"users"`
							Teams []json.RawMessage `json:"teams"`
							Apps  []json.RawMessage `json:"apps"`
						} `json:"restrictions"`
					}
					if probe(t, client, "/repos/"+repo+"/branches/"+branch+"/protection", &protection) {
						if rsc := protection.RequiredStatusChecks; rsc == nil {
							t.Log("classic protection has no required_status_checks")
						} else {
							t.Logf("classic protection strict %t", rsc.Strict)
							for _, c := range rsc.Checks {
								t.Logf("  required check %q, app id %s", c.Context, optionalID(c.AppID))
							}
						}
						if protection.EnforceAdmins == nil {
							t.Log("classic protection has no enforce_admins")
						} else {
							t.Logf("classic protection enforce_admins %t", protection.EnforceAdmins.Enabled)
						}
						t.Logf("classic protection required_pull_request_reviews present %t", protection.RequiredPullRequestReviews != nil)
						if protection.AllowForcePushes == nil {
							t.Log("classic protection has no allow_force_pushes")
						} else {
							t.Logf("classic protection allow_force_pushes %t", protection.AllowForcePushes.Enabled)
						}
						if r := protection.Restrictions; r == nil {
							t.Log("classic protection has no restrictions")
						} else {
							t.Logf("classic protection restrictions: %d users, %d teams, %d apps", len(r.Users), len(r.Teams), len(r.Apps))
						}
					}

					var summary struct {
						Protected  bool `json:"protected"`
						Protection *struct {
							RequiredStatusChecks *struct {
								Contexts []string `json:"contexts"`
								Checks   []struct {
									Context string `json:"context"`
									AppID   *int64 `json:"app_id"`
								} `json:"checks"`
							} `json:"required_status_checks"`
						} `json:"protection"`
					}
					if probe(t, client, "/repos/"+repo+"/branches/"+branch, &summary) {
						t.Logf("branch protected %t", summary.Protected)
						if p := summary.Protection; p != nil && p.RequiredStatusChecks != nil {
							t.Logf("  branch summary lists %d contexts", len(p.RequiredStatusChecks.Contexts))
							for _, c := range p.RequiredStatusChecks.Checks {
								t.Logf("  required check %q, app id %s", c.Context, optionalID(c.AppID))
							}
						}
					}
				})
			}
		})
	}

	if ownerLogin != "" {
		t.Run("owner", func(t *testing.T) {
			_, name, _ := strings.Cut(repos[0], "/")
			_, client := newClient(t, name, readPermissions)
			var user struct {
				ID   int64  `json:"id"`
				Type string `json:"type"`
			}
			if probe(t, client, "/users/"+ownerLogin, &user) {
				t.Logf("owner id %d, type %q", user.ID, user.Type)
			}
		})
	}
}

// TestLiveProtectionResults runs the production protection path against the real
// App: the {metadata: read, administration: read} token, Reader.Repository,
// Reader.Protection on the default branch, the mapping into protection.Facts and
// protection.Evaluate, then asserts the kind and the exact findings.
// SUITEWARD_TEST_GITHUB_EXPECT is a semicolon-separated list of
// owner/name=<test check>=<kind>[:<finding>,<finding>], for example
// IgnisDevNE/SuiteWardQ=CI=findings:missing-contract-check,missing-test-check.
// It logs the result kind, findings, caveats and instructions (fixed sentences),
// never GitHub text, a token, a key or a body. It needs no pull request.
func TestLiveProtectionResults(t *testing.T) {
	appID := liveInt(t, "SUITEWARD_TEST_GITHUB_APP_ID")
	installationID := liveInt(t, "SUITEWARD_TEST_GITHUB_INSTALLATION_ID")
	keyFile := liveEnv(t, "SUITEWARD_TEST_GITHUB_APP_KEY_FILE")
	apiURL := os.Getenv("SUITEWARD_TEST_GITHUB_API_URL")
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	type expectation struct {
		repo, name, testCheck string
		kind                  protection.Kind
		findings              []string
	}
	kinds := []protection.Kind{protection.KindEnforced, protection.KindFindings, protection.KindEnforcementUnavailable, protection.KindUnknown}
	var expected []expectation
	for _, entry := range strings.Split(liveEnv(t, "SUITEWARD_TEST_GITHUB_EXPECT"), ";") {
		parts := strings.SplitN(strings.TrimSpace(entry), "=", 3)
		if len(parts) != 3 {
			t.Fatal("SUITEWARD_TEST_GITHUB_EXPECT must be a semicolon-separated list of owner/name=<test check>=<kind>[:<finding>,<finding>]")
		}
		owner, name, ok := strings.Cut(parts[0], "/")
		kind, findings, _ := strings.Cut(parts[2], ":")
		e := expectation{repo: parts[0], name: name, testCheck: parts[1], kind: protection.Kind(kind)}
		if findings != "" {
			e.findings = strings.Split(findings, ",")
			slices.Sort(e.findings)
		}
		if !ok || owner == "" || name == "" || e.testCheck == "" || !slices.Contains(kinds, e.kind) {
			t.Fatal("SUITEWARD_TEST_GITHUB_EXPECT must be a semicolon-separated list of owner/name=<test check>=<kind>[:<finding>,<finding>]")
		}
		expected = append(expected, e)
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("read SUITEWARD_TEST_GITHUB_APP_KEY_FILE: %v", err)
	}
	app, err := appauth.NewApp(appID, key, time.Now)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	httpClient := &http.Client{Timeout: 20 * time.Second}
	budget, err := rest.NewBudget(0.5, time.Now)
	if err != nil {
		t.Fatalf("NewBudget: %v", err)
	}
	// The map internal/app (protectionPermissions) requests; keep them equal.
	protectionPermissions := map[string]string{"metadata": "read", "administration": "read"}

	for _, e := range expected {
		t.Run(e.repo, func(t *testing.T) {
			inst, err := app.Installation(installationID, []string{e.name}, protectionPermissions, httpClient, apiURL)
			if err != nil {
				t.Fatalf("Installation: %v", err)
			}
			client, err := rest.NewClient(func(ctx context.Context) (string, string, error) {
				tok, err := inst.Token(ctx)
				if err != nil {
					return "", "", err
				}
				return tok.Reveal(), tok.Identity(), nil
			}, httpClient, apiURL, budget)
			if err != nil {
				t.Fatalf("NewClient: %v", err)
			}
			if _, err := inst.Token(stepCtx(t)); err != nil {
				t.Fatalf("Token (also fails when granted permissions differ from %v): %s", protectionPermissions, failure(err))
			}
			reader, err := github.NewReader(client)
			if err != nil {
				t.Fatalf("NewReader: %v", err)
			}
			repo, err := reader.Repository(stepCtx(t), e.repo)
			if err != nil {
				t.Fatalf("Repository: %s", failure(err))
			}
			read, err := reader.Protection(stepCtx(t), e.repo, repo.DefaultBranch)
			if err != nil {
				t.Fatalf("Protection: %s", failure(err))
			}

			// Field by field, as protectionFacts in internal/app/setup.go (unexported).
			checks := func(in []github.Check) []protection.Check {
				var out []protection.Check
				for _, c := range in {
					out = append(out, protection.Check{Context: c.Context, SourceID: c.SourceID})
				}
				return out
			}
			var state protection.ClassicState
			switch read.Classic.State {
			case github.ClassicAbsent:
				state = protection.ClassicAbsent
			case github.ClassicUnreadable:
				state = protection.ClassicUnreadable
			case github.ClassicPresent:
				state = protection.ClassicPresent
			default:
				t.Fatalf("unknown classic protection state %d", read.Classic.State)
			}
			facts := protection.Facts{
				Private: repo.Private, RulesForbidden: read.RulesForbidden, FromRuleset: read.FromRuleset,
				Classic: protection.Classic{State: state, Checks: checks(read.Classic.Checks), Strict: read.Classic.Strict, EnforceAdmins: read.Classic.EnforceAdmins},
			}
			for _, rule := range read.Rules {
				facts.Rules = append(facts.Rules, protection.Rule{RulesetID: rule.RulesetID, Checks: checks(rule.Checks), Strict: rule.Strict})
			}

			result := protection.Evaluate(facts, appID, e.testCheck)
			t.Logf("result kind %s", result.Kind)
			var got []string
			for _, f := range result.Findings {
				t.Logf("finding %s", f)
				got = append(got, string(f))
			}
			for _, c := range result.Caveats {
				t.Logf("caveat %s", c)
			}
			for _, in := range result.Instructions {
				t.Logf("instruction: %s", in)
			}
			slices.Sort(got)
			if result.Kind != e.kind || !slices.Equal(got, e.findings) {
				t.Errorf("got kind %s with findings %v, want kind %s with findings %v", result.Kind, got, e.kind, e.findings)
			}
		})
	}
}

// TestLiveWrites measures the M1.6a writes with the real App on a throwaway test
// pull request (docs/contracts/github.md, "M1.6a: comment writes"): the bot
// identity, the read map from an App that holds write, a comment write and its
// read-back, the same write refused with the read map, and a check run write.
// It needs the first three TestLive variables plus SUITEWARD_TEST_GITHUB_WRITE_REPO
// (owner/name), SUITEWARD_TEST_GITHUB_WRITE_PULL (the pull number) and
// SUITEWARD_TEST_GITHUB_WRITE_MARKER (a random token per run). It leaves one
// comment and one check run on the pull request. It logs statuses and the ids
// named in each step, never a token, the JWT, a key or a response body.
func TestLiveWrites(t *testing.T) {
	appID := liveInt(t, "SUITEWARD_TEST_GITHUB_APP_ID")
	installationID := liveInt(t, "SUITEWARD_TEST_GITHUB_INSTALLATION_ID")
	keyFile := liveEnv(t, "SUITEWARD_TEST_GITHUB_APP_KEY_FILE")
	repo := liveEnv(t, "SUITEWARD_TEST_GITHUB_WRITE_REPO")
	number := liveInt(t, "SUITEWARD_TEST_GITHUB_WRITE_PULL")
	marker := liveEnv(t, "SUITEWARD_TEST_GITHUB_WRITE_MARKER")
	apiURL := os.Getenv("SUITEWARD_TEST_GITHUB_API_URL")
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	_, name, ok := strings.Cut(repo, "/")
	if !ok {
		t.Fatal("SUITEWARD_TEST_GITHUB_WRITE_REPO must be owner/name")
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("read SUITEWARD_TEST_GITHUB_APP_KEY_FILE: %v", err)
	}
	app, err := appauth.NewApp(appID, key, time.Now)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	httpClient := &http.Client{Timeout: 20 * time.Second}
	budget, err := rest.NewBudget(0.5, time.Now)
	if err != nil {
		t.Fatalf("NewBudget: %v", err)
	}
	// token mints an installation token narrowed to the test repository; Token
	// fails unless GitHub grants exactly the requested map.
	token := func(t *testing.T, permissions map[string]string) string {
		t.Helper()
		inst, err := app.Installation(installationID, []string{name}, permissions, httpClient, apiURL)
		if err != nil {
			t.Fatalf("Installation: %v", err)
		}
		tok, err := inst.Token(stepCtx(t))
		if err != nil {
			t.Fatalf("Token (also fails when granted permissions differ from %v): %s", permissions, failure(err))
		}
		return tok.Reveal()
	}
	readClient := func(t *testing.T) *rest.Client {
		t.Helper()
		inst, err := app.Installation(installationID, []string{name}, readPermissions, httpClient, apiURL)
		if err != nil {
			t.Fatalf("Installation: %v", err)
		}
		client, err := rest.NewClient(func(ctx context.Context) (string, string, error) {
			tok, err := inst.Token(ctx)
			if err != nil {
				return "", "", err
			}
			return tok.Reveal(), tok.Identity(), nil
		}, httpClient, apiURL, budget)
		if err != nil {
			t.Fatalf("NewClient: %v", err)
		}
		return client
	}
	// call sends one request with a bearer credential and returns the status and
	// the body, which callers decode but never log.
	call := func(t *testing.T, method, path, bearer string, body any) (int, []byte) {
		t.Helper()
		var in io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("encode %s %s: %v", method, path, err)
			}
			in = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(stepCtx(t), method, apiURL+path, in)
		if err != nil {
			t.Fatalf("build %s %s: %v", method, path, err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: transport error", method, path)
		}
		defer func() { _ = resp.Body.Close() }()
		out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			t.Fatalf("%s %s: read the answer: %v", method, path, err)
		}
		return resp.StatusCode, out
	}
	decode := func(t *testing.T, path string, raw []byte, out any) {
		t.Helper()
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s: the answer is not the expected JSON", path)
		}
	}

	pull := strconv.FormatInt(number, 10)
	commentsPath := "/repos/" + repo + "/issues/" + pull + "/comments"
	writeComment := map[string]string{"metadata": "read", "pull_requests": "write"}
	commentBody := map[string]string{"body": "SuiteWard M1.6a-P0 measurement. <!-- suiteward:p0:" + marker + " -->"}
	var botID int64

	t.Run("bot identity", func(t *testing.T) {
		status, raw := call(t, http.MethodGet, "/app", appJWT(t, appID, key), nil)
		if status != http.StatusOK {
			t.Fatalf("GET /app answered %d, want 200", status)
		}
		var self struct {
			ID   int64  `json:"id"`
			Slug string `json:"slug"`
		}
		decode(t, "/app", raw, &self)
		if self.Slug == "" {
			t.Fatal("GET /app answered no slug")
		}
		t.Logf("GET /app answered %d: slug %q, id %d", status, self.Slug, self.ID)
		path := "/users/" + self.Slug + "%5Bbot%5D"
		resp, err := readClient(t).Get(stepCtx(t), path)
		if err != nil {
			t.Fatalf("GET %s: %s", path, failure(err))
		}
		var user struct {
			ID    int64  `json:"id"`
			Login string `json:"login"`
			Type  string `json:"type"`
		}
		decode(t, path, resp.Body, &user)
		if user.ID == 0 {
			t.Fatalf("GET %s answered no id", path)
		}
		botID = user.ID
		t.Logf("GET %s answered %d: id %d, login %q, type %q", path, resp.Status, user.ID, user.Login, user.Type)
	})

	t.Run("read map from an App that holds write", func(t *testing.T) {
		// A grant of exactly readPermissions proves that requesting read from an
		// App holding write is not widened.
		token(t, readPermissions)
		t.Logf("a token requested with %v was granted exactly that map", readPermissions)
	})

	t.Run("comment write", func(t *testing.T) {
		if botID == 0 {
			t.Fatal("no bot account id: the bot identity step failed")
		}
		status, raw := call(t, http.MethodPost, commentsPath, token(t, writeComment), commentBody)
		if status != http.StatusCreated {
			t.Fatalf("POST %s answered %d, want 201", commentsPath, status)
		}
		var created struct {
			ID   int64 `json:"id"`
			User struct {
				ID int64 `json:"id"`
			} `json:"user"`
		}
		decode(t, commentsPath, raw, &created)
		t.Logf("POST %s answered %d: comment id %d, author id %d", commentsPath, status, created.ID, created.User.ID)

		reader := readClient(t)
		count, found := 0, false
		for page := 1; page <= 20 && !found; page++ {
			path := commentsPath + "?per_page=100&page=" + strconv.Itoa(page)
			resp, err := reader.Get(stepCtx(t), path)
			if err != nil {
				t.Fatalf("GET %s: %s", path, failure(err))
			}
			var list []struct {
				Body string `json:"body"`
				User struct {
					ID int64 `json:"id"`
				} `json:"user"`
			}
			decode(t, path, resp.Body, &list)
			count += len(list)
			for _, c := range list {
				if c.User.ID == botID && strings.Contains(c.Body, marker) {
					found = true
				}
			}
			if len(list) < 100 {
				break
			}
		}
		t.Logf("listed %d comments up to the marker comment or the end", count)
		if !found {
			t.Errorf("no comment by the bot account id %d carries the marker", botID)
		}
	})

	t.Run("comment write with the read token", func(t *testing.T) {
		status, _ := call(t, http.MethodPost, commentsPath, token(t, readPermissions), commentBody)
		t.Logf("POST %s with the read map answered %d", commentsPath, status)
		if status != http.StatusForbidden {
			t.Errorf("POST %s with the read map answered %d, want 403", commentsPath, status)
		}
	})

	t.Run("check-run write", func(t *testing.T) {
		pullPath := "/repos/" + repo + "/pulls/" + pull
		head := getJSON[struct {
			Head struct {
				SHA string `json:"sha"`
			} `json:"head"`
		}](t, stepCtx(t), readClient(t), pullPath)
		if head.Head.SHA == "" {
			t.Fatalf("GET %s answered no head sha", pullPath)
		}
		path := "/repos/" + repo + "/check-runs"
		status, raw := call(t, http.MethodPost, path, token(t, map[string]string{"metadata": "read", "checks": "write"}), map[string]any{
			"name":       "SuiteWard / P0 measurement",
			"head_sha":   head.Head.SHA,
			"status":     "completed",
			"conclusion": "neutral",
			"output":     map[string]string{"title": "SuiteWard P0 measurement", "summary": "M1.6a-P0 live measurement of check-run writes."},
		})
		if status != http.StatusCreated {
			t.Fatalf("POST %s answered %d, want 201", path, status)
		}
		var run struct {
			ID  int64 `json:"id"`
			App struct {
				ID int64 `json:"id"`
			} `json:"app"`
		}
		decode(t, path, raw, &run)
		t.Logf("POST %s answered %d: check run id %d, app id %d", path, status, run.ID, run.App.ID)
		if run.App.ID != appID {
			t.Errorf("the check run carries app id %d, want %d", run.App.ID, appID)
		}
	})
}

// TestLiveChecks measures check-run behavior with the real App on the head of a
// throwaway test pull request (M1.6b-P0): the create answer, the read-back with
// filter=latest and which token reads it, whether an in_progress run after a
// success becomes the latest run, the combined status and check-suite views, and
// the refusal of an unknown head sha. It is a measurement: only a failed create
// of a run it needs or a broken round trip fails a step. It needs the first three
// TestLive variables plus SUITEWARD_TEST_GITHUB_WRITE_REPO, _WRITE_PULL and
// _WRITE_MARKER as TestLiveWrites does. It writes check runs named
// "SuiteWard / P0 checks" on the pull request head, ending on a completed success
// run, and never deletes anything. It logs statuses, ids, counts and the named
// fields, never a token, the JWT, a key or a response body.
func TestLiveChecks(t *testing.T) {
	appID := liveInt(t, "SUITEWARD_TEST_GITHUB_APP_ID")
	installationID := liveInt(t, "SUITEWARD_TEST_GITHUB_INSTALLATION_ID")
	keyFile := liveEnv(t, "SUITEWARD_TEST_GITHUB_APP_KEY_FILE")
	repo := liveEnv(t, "SUITEWARD_TEST_GITHUB_WRITE_REPO")
	pull := strconv.FormatInt(liveInt(t, "SUITEWARD_TEST_GITHUB_WRITE_PULL"), 10)
	marker := liveEnv(t, "SUITEWARD_TEST_GITHUB_WRITE_MARKER")
	apiURL := os.Getenv("SUITEWARD_TEST_GITHUB_API_URL")
	if apiURL == "" {
		apiURL = "https://api.github.com"
	}
	_, name, ok := strings.Cut(repo, "/")
	if !ok {
		t.Fatal("SUITEWARD_TEST_GITHUB_WRITE_REPO must be owner/name")
	}
	key, err := os.ReadFile(keyFile)
	if err != nil {
		t.Fatalf("read SUITEWARD_TEST_GITHUB_APP_KEY_FILE: %v", err)
	}
	app, err := appauth.NewApp(appID, key, time.Now)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	httpClient := &http.Client{Timeout: 20 * time.Second}
	token := func(t *testing.T, permissions map[string]string) string {
		t.Helper()
		inst, err := app.Installation(installationID, []string{name}, permissions, httpClient, apiURL)
		if err != nil {
			t.Fatalf("Installation: %v", err)
		}
		tok, err := inst.Token(stepCtx(t))
		if err != nil {
			t.Fatalf("Token (also fails when granted permissions differ from %v): %s", permissions, failure(err))
		}
		return tok.Reveal()
	}
	// call sends one request and returns the status and the body, which callers
	// decode but never log.
	call := func(t *testing.T, method, path, bearer string, body any) (int, []byte) {
		t.Helper()
		var in io.Reader
		if body != nil {
			raw, err := json.Marshal(body)
			if err != nil {
				t.Fatalf("encode %s %s: %v", method, path, err)
			}
			in = bytes.NewReader(raw)
		}
		req, err := http.NewRequestWithContext(stepCtx(t), method, apiURL+path, in)
		if err != nil {
			t.Fatalf("build %s %s: %v", method, path, err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := httpClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: transport error", method, path)
		}
		defer func() { _ = resp.Body.Close() }()
		out, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			t.Fatalf("%s %s: read the answer: %v", method, path, err)
		}
		return resp.StatusCode, out
	}
	decode := func(t *testing.T, path string, raw []byte, out any) {
		t.Helper()
		if err := json.Unmarshal(raw, out); err != nil {
			t.Fatalf("%s: the answer is not the expected JSON", path)
		}
	}

	checksWrite := map[string]string{"metadata": "read", "checks": "write"}
	checksRead := map[string]string{"metadata": "read", "checks": "read"}
	const checkName = "SuiteWard / P0 checks"
	checkRunsPath := "/repos/" + repo + "/check-runs"
	appQuery := strconv.FormatInt(appID, 10)
	output := map[string]string{"title": "SuiteWard P0 checks", "summary": "M1.6b-P0 live measurement of check-run readback and state transitions."}
	type run struct {
		ID         int64  `json:"id"`
		Status     string `json:"status"`
		Conclusion string `json:"conclusion"`
		ExternalID string `json:"external_id"`
	}
	externalID := func(n int) string { return "suiteward:p0:" + marker + ":" + strconv.Itoa(n) }
	// post creates one check run on sha and returns the status and the run.
	post := func(t *testing.T, sha, status, conclusion string, n int) (int, run) {
		t.Helper()
		body := map[string]any{"name": checkName, "head_sha": sha, "status": status, "output": output, "external_id": externalID(n)}
		if conclusion != "" {
			body["conclusion"] = conclusion
		}
		code, raw := call(t, http.MethodPost, checkRunsPath, token(t, checksWrite), body)
		var created run
		if code == http.StatusCreated {
			decode(t, checkRunsPath, raw, &created)
		}
		return code, created
	}
	// get reads path with each permission map in turn until one is not refused with
	// a 403, and logs which one read. It returns the status and the body.
	get := func(t *testing.T, path string, labels []string, maps ...map[string]string) (int, []byte) {
		t.Helper()
		var code int
		var raw []byte
		for i, m := range maps {
			code, raw = call(t, http.MethodGet, path, token(t, m), nil)
			t.Logf("GET %s with the %s token answered %d", path, labels[i], code)
			if code != http.StatusForbidden {
				break
			}
		}
		return code, raw
	}
	checkLabels := []string{"checks write", "checks read"}
	checkMaps := []map[string]string{checksWrite, checksRead}
	runsOf := func(t *testing.T, sha, query string) (int, []run) {
		t.Helper()
		path := "/repos/" + repo + "/commits/" + sha + "/check-runs?check_name=SuiteWard%20%2F%20P0%20checks&app_id=" + appQuery + query
		code, raw := get(t, path, checkLabels, checkMaps...)
		if code != http.StatusOK {
			return code, nil
		}
		var list struct {
			TotalCount int   `json:"total_count"`
			CheckRuns  []run `json:"check_runs"`
		}
		decode(t, path, raw, &list)
		t.Logf("total_count %d, %d runs listed", list.TotalCount, len(list.CheckRuns))
		for _, r := range list.CheckRuns {
			t.Logf("  run id %d, status %q, conclusion %q, external_id %q", r.ID, r.Status, r.Conclusion, r.ExternalID)
		}
		return code, list.CheckRuns
	}

	pullPath := "/repos/" + repo + "/pulls/" + pull
	status, raw := call(t, http.MethodGet, pullPath, token(t, readPermissions), nil)
	if status != http.StatusOK {
		t.Fatalf("GET %s answered %d, want 200", pullPath, status)
	}
	var pr struct {
		Head struct {
			SHA string `json:"sha"`
		} `json:"head"`
	}
	decode(t, pullPath, raw, &pr)
	sha := pr.Head.SHA
	if sha == "" {
		t.Fatalf("GET %s answered no head sha", pullPath)
	}
	var first, second run

	t.Run("POST success", func(t *testing.T) {
		code, created := post(t, sha, "completed", "success", 1)
		t.Logf("POST %s answered %d: check run id %d", checkRunsPath, code, created.ID)
		if code != http.StatusCreated {
			t.Fatalf("POST %s answered %d, want 201", checkRunsPath, code)
		}
		first = created
	})

	t.Run("readback", func(t *testing.T) {
		if first.ID == 0 {
			t.Fatal("no check run id: the POST success step failed")
		}
		code, runs := runsOf(t, sha, "&filter=latest")
		if code != http.StatusOK {
			t.Fatalf("the readback answered %d with every token, want 200", code)
		}
		for _, r := range runs {
			if r.ID == first.ID {
				if r.ExternalID != externalID(1) {
					t.Errorf("run %d round-trips external_id %q, want %q", r.ID, r.ExternalID, externalID(1))
				}
				return
			}
		}
		t.Errorf("run %d is not among the %d runs read back", first.ID, len(runs))
	})

	t.Run("back to pending", func(t *testing.T) {
		code, created := post(t, sha, "in_progress", "", 2)
		t.Logf("POST %s answered %d: check run id %d", checkRunsPath, code, created.ID)
		if code != http.StatusCreated {
			t.Fatalf("POST %s answered %d, want 201", checkRunsPath, code)
		}
		second = created
		_, runs := runsOf(t, sha, "&filter=latest")
		for _, r := range runs {
			which := "neither run"
			switch r.ID {
			case first.ID:
				which = "the success run"
			case second.ID:
				which = "the in_progress run"
			}
			t.Logf("latest run %d is %s: status %q, conclusion %q", r.ID, which, r.Status, r.Conclusion)
		}
		_, all := runsOf(t, sha, "")
		t.Logf("without filter=latest, %d runs are listed", len(all))
	})

	t.Run("required-status view", func(t *testing.T) {
		statusPath := "/repos/" + repo + "/commits/" + sha + "/status"
		code, raw := get(t, statusPath, []string{"checks write", "read"}, checksWrite, readPermissions)
		if code == http.StatusOK {
			var combined struct {
				State      string            `json:"state"`
				TotalCount int               `json:"total_count"`
				Statuses   []json.RawMessage `json:"statuses"`
			}
			decode(t, statusPath, raw, &combined)
			t.Logf("combined status state %q, total_count %d, %d statuses", combined.State, combined.TotalCount, len(combined.Statuses))
		}
		suitesPath := "/repos/" + repo + "/commits/" + sha + "/check-suites?app_id=" + appQuery
		code, raw = get(t, suitesPath, checkLabels, checkMaps...)
		if code == http.StatusOK {
			var suites struct {
				TotalCount  int `json:"total_count"`
				CheckSuites []struct {
					ID         int64  `json:"id"`
					Status     string `json:"status"`
					Conclusion string `json:"conclusion"`
					App        struct {
						ID int64 `json:"id"`
					} `json:"app"`
				} `json:"check_suites"`
			}
			decode(t, suitesPath, raw, &suites)
			t.Logf("total_count %d check suites", suites.TotalCount)
			for _, s := range suites.CheckSuites {
				t.Logf("  suite id %d, app id %d, status %q, conclusion %q", s.ID, s.App.ID, s.Status, s.Conclusion)
			}
		}
	})

	t.Run("unknown sha", func(t *testing.T) {
		code, _ := post(t, strings.Repeat("0", 40), "completed", "success", 0)
		t.Logf("POST %s with a head_sha of 40 zeros answered %d", checkRunsPath, code)
		if code == http.StatusCreated {
			t.Errorf("POST %s with an unknown head_sha answered 201", checkRunsPath)
		}
	})

	t.Run("cleanup state", func(t *testing.T) {
		code, created := post(t, sha, "completed", "success", 3)
		t.Logf("POST %s answered %d: check run id %d", checkRunsPath, code, created.ID)
		if code != http.StatusCreated {
			t.Fatalf("POST %s answered %d, want 201", checkRunsPath, code)
		}
	})
}

// appJWT signs an RS256 App JWT (iat now-60 s, exp now+9 min) for GET /app, which
// appauth does not expose; it never logs the key or the token.
func appJWT(t *testing.T, appID int64, keyPEM []byte) string {
	t.Helper()
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		t.Fatal("the private key holds no PEM block")
	}
	var key *rsa.PrivateKey
	switch block.Type {
	case "RSA PRIVATE KEY":
		k, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			t.Fatal("the private key is not a valid PKCS#1 RSA key")
		}
		key = k
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			t.Fatal("the private key is not a valid PKCS#8 key")
		}
		k, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			t.Fatal("the PKCS#8 private key is not an RSA key")
		}
		key = k
	default:
		t.Fatal("the private key PEM block is not RSA PRIVATE KEY or PRIVATE KEY")
	}
	now := time.Now()
	claims, err := json.Marshal(map[string]any{"iat": now.Add(-60 * time.Second).Unix(), "exp": now.Add(9 * time.Minute).Unix(), "iss": strconv.FormatInt(appID, 10)})
	if err != nil {
		t.Fatalf("encode jwt claims: %v", err)
	}
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(nil, key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatalf("sign jwt: %v", err)
	}
	return signing + "." + enc.EncodeToString(sig)
}

// probe GETs path and decodes a 200 into out. It returns true only for a decoded 200.
// A 401, 403 or 404 is a logged outcome; any other status or a transport error
// fails the step without stopping the test.
func probe(t *testing.T, client *rest.Client, path string, out any) bool {
	t.Helper()
	resp, err := client.Get(stepCtx(t), path)
	if err != nil {
		var se *rest.StatusError
		if errors.As(err, &se) && (se.Status == http.StatusUnauthorized || se.Status == http.StatusForbidden || se.Status == http.StatusNotFound) {
			t.Logf("GET %s answered %d", path, se.Status)
			return false
		}
		t.Errorf("GET %s: %s", path, failure(err))
		return false
	}
	if resp.Status != http.StatusOK {
		t.Errorf("GET %s answered %d, want 200, 401, 403 or 404", path, resp.Status)
		return false
	}
	if err := json.Unmarshal(resp.Body, out); err != nil {
		t.Errorf("GET %s: the answer is not the expected JSON", path)
		return false
	}
	t.Logf("GET %s answered 200", path)
	return true
}

func optionalID(id *int64) string {
	if id == nil {
		return "absent"
	}
	return strconv.FormatInt(*id, 10)
}

// bypassEnum logs only the documented values of current_user_can_bypass, never a raw fragment.
func bypassEnum(s *string) string {
	if s == nil {
		return "absent"
	}
	switch *s {
	case "always", "pull_requests_only", "never":
		return *s
	}
	return "other"
}

func optionalBool(b *bool) string {
	if b == nil {
		return "absent"
	}
	return strconv.FormatBool(*b)
}

func getJSON[T any](t *testing.T, ctx context.Context, client *rest.Client, path string) T {
	t.Helper()
	resp, err := client.Get(ctx, path)
	if err != nil {
		t.Fatalf("GET %s: %s", path, failure(err))
	}
	var v T
	if err := json.Unmarshal(resp.Body, &v); err != nil {
		t.Fatalf("GET %s: the answer is not the expected JSON", path)
	}
	return v
}

func liveInt(t *testing.T, name string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(liveEnv(t, name), 10, 64)
	if err != nil {
		t.Fatalf("%s must be an integer", name)
	}
	return n
}

// failure renders a status or rate-limit error as its status and wait time only, so a live log holds no more than that.
func failure(err error) string {
	var se *rest.StatusError
	var rl *rest.RateLimitedError
	switch {
	case errors.As(err, &se):
		return "status " + strconv.Itoa(se.Status)
	case errors.As(err, &rl):
		return "rate limited, status " + strconv.Itoa(rl.Status) + ", until " + rl.Until.UTC().Format(time.RFC3339)
	}
	return err.Error()
}
