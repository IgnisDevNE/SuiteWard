package installer

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// What the installer asks about the account that owns the App (UX-6, UX-7): a choice between personal and an organization, and for an
// organization its login, which GitHub is asked about before the browser opens: a typo or a word that is no organization must not
// become an App that GitHub creates under the wrong account.

// accountFlags are the answers of a flow that creates its App, except the account and the manifest code.
func accountFlags() map[string]string {
	f := manifestFlags()
	delete(f, "github-account")
	delete(f, "manifest-code")
	return f
}

type browserResult struct {
	res GitHubResult
	err error
}

// withBrowser runs the flow of env while the browser answers its manifest page with code, and returns what the flow returned. A flow
// that ends before the browser is needed, or while it answers, is returned as it is: its error says why.
func withBrowser(t *testing.T, env githubEnv, out *syncBuffer, code string) (GitHubResult, error) {
	t.Helper()
	done := make(chan browserResult, 1)
	go func() {
		res, err := resolve(t, env)
		done <- browserResult{res, err}
	}()
	deadline := time.Now().Add(5 * time.Second)
	base := ""
	for base == "" {
		select {
		case r := <-done:
			return r.res, r.err
		default:
		}
		if base = listenerURL.FindString(out.String()); base == "" {
			if time.Now().After(deadline) {
				t.Fatalf("the flow never opened its listener:\n%s", out.String())
			}
			time.Sleep(5 * time.Millisecond)
		}
	}
	err := browserAnswers(base, code)
	select {
	case r := <-done:
		return r.res, r.err
	case <-time.After(10 * time.Second):
		t.Fatalf("the flow did not return after the browser answered (%v):\n%s", err, out.String())
		return GitHubResult{}, nil
	}
}

// browserAnswers is the browser of a user who approves the App: it loads the manifest page and follows GitHub's redirect with code.
func browserAnswers(base, code string) error {
	resp, err := http.Get(base) //nolint:gosec,noctx // a loopback test server
	if err != nil {
		return err
	}
	page, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil {
		return err
	}
	action := formAction.FindSubmatch(page)
	if action == nil {
		return errors.New("the manifest page has no form")
	}
	_, state, _ := strings.Cut(html.UnescapeString(string(action[1])), "state=")
	resp, err = http.Get(fmt.Sprintf("%scallback?code=%s&state=%s", base, code, state)) //nolint:gosec,noctx // a loopback test server
	if err != nil {
		return err
	}
	return resp.Body.Close()
}

func TestPlainTerminalAsksTheAccountAsBefore(t *testing.T) {
	g := newFakeGitHub(t)
	env, out := flowEnv(t, g, accountFlags(), strings.NewReader("acme\n"), true)
	res, err := withBrowser(t, env, out, "thecode1")
	if err != nil || res.Account != "acme" {
		t.Fatalf("resolveGitHub = %v, %v\n%s", res, err, out.String())
	}
	if want := `GitHub account that will own the App ("personal" or an organization login) [personal]: `; !strings.Contains(out.String(), want) {
		t.Errorf("the question changed in plain mode; want %q in:\n%s", want, out.String())
	}
	if !g.sawRequest("GET /users/acme") {
		t.Errorf("the organization was not looked up before the browser opened: %v", g.requests)
	}
	if strings.Contains(out.String(), "Account that will own") {
		t.Errorf("the full-screen question leaked into plain mode:\n%s", out.String())
	}
}

func TestAnOrganizationGitHubDoesNotKnowIsRefusedBeforeTheBrowserOpens(t *testing.T) {
	cases := []struct {
		name  string
		setup func(g *fakeGitHub)
		login string
		want  []string
		not   string
	}{
		{"no such account", func(g *fakeGitHub) { g.setUser("ghost", userMissing) }, "ghost", []string{"ghost", "no account"}, ""},
		{"a user, not an organization", func(g *fakeGitHub) { g.setUser("alice", "User") }, "alice", []string{"alice", "not an organization", "personal"}, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newFakeGitHub(t)
			tc.setup(g)
			f := accountFlags()
			f["github-account"] = tc.login
			env, out := flowEnv(t, g, f, strings.NewReader(""), false)
			env.ManifestWait = 100 * time.Millisecond
			_, err := resolve(t, env)
			if err == nil {
				t.Fatal("the account was accepted")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error lacks %q: %v", want, err)
				}
			}
			if tc.not != "" && strings.Contains(err.Error(), tc.not) {
				t.Errorf("the error says %q, which is not what GitHub answered: %v", tc.not, err)
			}
			if g.sawRequest("POST /app-manifests") || listenerURL.MatchString(out.String()) {
				t.Errorf("the browser was opened for an account that was refused: %v\n%s", g.requests, out.String())
			}
		})
	}
}

// failingUsers is an HTTP client for which the look-up of an account fails on the network.
type failingUsers struct{ http.RoundTripper }

func (f failingUsers) RoundTrip(r *http.Request) (*http.Response, error) {
	if strings.HasPrefix(r.URL.Path, "/users/") {
		return nil, errors.New("connection reset by peer")
	}
	return f.RoundTripper.RoundTrip(r)
}

// What GitHub answers to the look-up says nothing of the account when it is a rate limit, an error of GitHub or the network: the install
// is not stopped for it. The conversion checks the owner of the App once it exists, and names it.
func TestATransientFailureOfTheOrganizationLookupWarnsAndGoesOn(t *testing.T) {
	cases := []struct {
		name  string
		setup func(g *fakeGitHub, env *githubEnv)
		why   string
	}{
		{"a rate limit", func(g *fakeGitHub, _ *githubEnv) {
			g.answer("GET /users/acme", http.StatusForbidden, "API rate limit exceeded for 10.0.0.1")
		}, "HTTP 403"},
		{"too many requests", func(g *fakeGitHub, _ *githubEnv) {
			g.answer("GET /users/acme", http.StatusTooManyRequests, "slow down")
		}, "HTTP 429"},
		{"an error of GitHub", func(g *fakeGitHub, _ *githubEnv) {
			g.answer("GET /users/acme", http.StatusBadGateway, "<html>bad gateway</html>")
		}, "HTTP 502"},
		{"the network", func(g *fakeGitHub, env *githubEnv) {
			env.HTTP = &http.Client{Transport: failingUsers{g.srv.Client().Transport}}
		}, "connection reset by peer"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			g := newFakeGitHub(t)
			f := accountFlags()
			f["github-account"] = "acme"
			env, out := flowEnv(t, g, f, strings.NewReader(""), false)
			tc.setup(g, &env)
			res, err := withBrowser(t, env, out, "thecode1")
			if err != nil || res.Account != "acme" {
				t.Fatalf("resolveGitHub = %v, %v\n%s", res, err, out.String())
			}
			if !g.sawRequest("POST /app-manifests") {
				t.Error("the App was not created")
			}
			text := out.String()
			for _, want := range []string{"Could not check acme on GitHub (" + tc.why + ")", "This is not a problem", "after the App is created"} {
				if !strings.Contains(text, want) {
					t.Errorf("the output lacks %q:\n%s", want, text)
				}
			}
			for _, leaked := range []string{"rate limit exceeded", "bad gateway", "slow down"} {
				if strings.Contains(text, leaked) {
					t.Errorf("the output carries text of GitHub's answer:\n%s", text)
				}
			}
		})
	}
}

// A manifest code exists only once GitHub has created the App: refusing the account then would leave an App without a page that
// deletes it, so the code goes to the conversion, whose own check names the App.
func TestTheOrganizationIsNotLookedUpForAManifestCodeAlreadyGiven(t *testing.T) {
	g := newFakeGitHub(t)
	g.setUser("acme", userMissing)
	env, _ := flowEnv(t, g, manifestFlags(), strings.NewReader(""), false)
	res, err := resolve(t, env)
	if err != nil || res.Account != "acme" {
		t.Fatalf("resolveGitHub = %v, %v", res, err)
	}
	if g.sawRequest("GET /users/") {
		t.Errorf("the organization was looked up for a code that exists already: %v", g.requests)
	}
}

func TestPersonalIsNotLookedUp(t *testing.T) {
	g := newFakeGitHub(t)
	g.ownerLogin, g.ownerType = "alice", "User"
	g.repos[0].name = "alice/app"
	f := accountFlags()
	f["github-account"], f["repositories"] = "personal", "alice/app"
	env, out := flowEnv(t, g, f, strings.NewReader(""), false)
	res, err := withBrowser(t, env, out, "thecode1")
	if err != nil || res.Account != "alice" || g.sawRequest("GET /users/") {
		t.Fatalf("resolveGitHub = %v, %v; requests %v", res, err, g.requests)
	}
}

func TestTheWordOrganizationNeedsTheLoginWhereNothingCanAsk(t *testing.T) {
	g := newFakeGitHub(t)
	f := accountFlags()
	f["github-account"] = "organization"
	env, out := flowEnv(t, g, f, strings.NewReader(""), false)
	env.ManifestWait = 100 * time.Millisecond
	_, err := resolve(t, env)
	if err == nil || !strings.Contains(err.Error(), "--github-account <login>") {
		t.Fatalf("resolveGitHub = %v, want an error that says to pass the login itself", err)
	}
	if g.sawRequest("GET /users/") || listenerURL.MatchString(out.String()) {
		t.Errorf("the word was taken for a login: %v\n%s", g.requests, out.String())
	}
}

// On a terminal a refusal is explained and the question asked again; an answer that came from a flag is refused for good, because it
// would come back unchanged.
func TestARefusedAccountIsAskedAgainOnlyWhereItWasAsked(t *testing.T) {
	t.Run("typed at a plain terminal", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.setUser("ghost", userMissing)
		env, out := flowEnv(t, g, accountFlags(), strings.NewReader("ghost\nacme\n"), true)
		res, err := withBrowser(t, env, out, "thecode1")
		if err != nil || res.Account != "acme" {
			t.Fatalf("resolveGitHub = %v, %v\n%s", res, err, out.String())
		}
		text := out.String()
		if strings.Count(text, "no account named ghost") != 1 || strings.Count(text, `("personal" or an organization login) [personal]: `) != 2 {
			t.Errorf("the refusal was not explained once and the question asked twice:\n%s", text)
		}
	})
	t.Run("given as a flag", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.setUser("ghost", userMissing)
		f := accountFlags()
		f["github-account"] = "ghost"
		env, out := flowEnv(t, g, f, strings.NewReader("acme\n"), true)
		env.ManifestWait = 100 * time.Millisecond
		_, err := resolve(t, env)
		if err == nil || !strings.Contains(err.Error(), "no account") {
			t.Fatalf("resolveGitHub = %v, want the refusal", err)
		}
		if strings.Contains(out.String(), "[personal]") {
			t.Errorf("a flag was asked about again:\n%s", out.String())
		}
	})
	t.Run("a login that is not a login", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, out := flowEnv(t, g, accountFlags(), strings.NewReader("not a login!\nacme\n"), true)
		res, err := withBrowser(t, env, out, "thecode1")
		if err != nil || res.Account != "acme" || strings.Count(out.String(), `[personal]: `) != 2 {
			t.Fatalf("resolveGitHub = %v, %v\n%s", res, err, out.String())
		}
	})
}

func TestTheAccountIsAChoiceOnAFullScreen(t *testing.T) {
	t.Run("an organization and its login", func(t *testing.T) {
		g := newFakeGitHub(t)
		env, out := fullFlowEnv(t, liveContext(t), g, accountFlags(), strings.NewReader("2\nacme\n"), true)
		res, err := withBrowser(t, env, out, "thecode1")
		if err != nil || res.Account != "acme" {
			t.Fatalf("resolveGitHub = %v, %v", res, err)
		}
		for _, want := range []string{"GitHub account for the App", "personal", "organization", "Organization login (as in github.com/<org>)"} {
			if !shows(out.String(), want) {
				t.Errorf("the screen never showed %q", want)
			}
		}
		if !g.sawRequest("GET /users/acme") {
			t.Errorf("the organization was not looked up: %v", g.requests)
		}
	})
	t.Run("a refusal takes the user back to the choice", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.setUser("ghost", userMissing)
		env, out := fullFlowEnv(t, liveContext(t), g, accountFlags(), strings.NewReader("2\nghost\n2\nacme\n"), true)
		res, err := withBrowser(t, env, out, "thecode1")
		if err != nil || res.Account != "acme" {
			t.Fatalf("resolveGitHub = %v, %v", res, err)
		}
		// The second 2 is read as the choice again: were only the login asked again, it would be taken for the login "2".
		if !shows(out.String(), "no account named ghost") {
			t.Errorf("the refusal was never shown:\n%s", out.String())
		}
	})
	t.Run("an account that is given is not asked about", func(t *testing.T) {
		g := newFakeGitHub(t)
		f := accountFlags()
		f["github-account"] = "acme"
		env, out := fullFlowEnv(t, liveContext(t), g, f, strings.NewReader(""), true)
		if _, err := withBrowser(t, env, out, "thecode1"); err != nil {
			t.Fatal(err)
		}
		if shows(out.String(), "GitHub account for the App") {
			t.Error("the page of a question that is not asked wiped the screen")
		}
	})
}

// cancelling is an HTTP client whose answer comes as Ctrl-C is pressed: it cancels the context, then answers.
type cancelling struct {
	cancel func()
	status int
	body   string
}

func (c cancelling) RoundTrip(*http.Request) (*http.Response, error) {
	c.cancel()
	return &http.Response{StatusCode: c.status, Body: io.NopCloser(strings.NewReader(c.body)), Header: http.Header{}}, nil
}

// Ctrl-C is not a failure of GitHub, whichever way the look-up sees it: after the answer arrives as well as while it is awaited, the
// look-up ends with the interrupt, and the install is not told to go on.
func TestACtrlCDuringTheOrganizationLookupIsTheInterruptAndNoWarning(t *testing.T) {
	for name, answer := range map[string]struct {
		status int
		body   string
	}{
		"an answer that cannot be read": {http.StatusOK, "not json at all"},
		"an error status":               {http.StatusBadGateway, ""},
	} {
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			env := githubEnv{APIURL: "https://api.github.example", HTTP: &http.Client{Transport: cancelling{cancel, answer.status, answer.body}}}
			if err := lookupOrganization(ctx, env, "acme"); !errors.Is(err, context.Canceled) {
				var failed lookupFailed
				t.Fatalf("lookupOrganization = %v (a lookupFailed: %v), want the interrupt", err, errors.As(err, &failed))
			}
		})
	}
	t.Run("the flow does not warn", func(t *testing.T) {
		g := newFakeGitHub(t)
		ctx, cancel := context.WithCancel(context.Background())
		t.Cleanup(cancel)
		f := accountFlags()
		f["github-account"] = "acme"
		env, out := flowEnv(t, g, f, strings.NewReader(""), false)
		env.HTTP = &http.Client{Transport: cancelling{cancel, http.StatusOK, "not json at all"}}
		if _, err := resolveGitHub(ctx, env); !errors.Is(err, context.Canceled) {
			t.Fatalf("resolveGitHub = %v, want the interrupt", err)
		}
		if strings.Contains(out.String(), "Could not check") || g.sawRequest("POST /app-manifests") {
			t.Errorf("the interrupt was taken for a failure of GitHub, and the install went on:\n%s", out.String())
		}
	})
}
