package installer

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// testAppName is the name the manifest of a test run carries.
const testAppName = "suiteward-q1w2e3"

func TestListenLoopbackBindsOnlyTheLoopbackInterface(t *testing.T) {
	l, err := listenLoopback(0)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = l.Close() }()
	addr, ok := l.Addr().(*net.TCPAddr)
	if !ok || !addr.IP.Equal(net.IPv4(127, 0, 0, 1)) {
		t.Fatalf("the listener is on %v, want 127.0.0.1", l.Addr())
	}
}

func TestConvertManifestAccepts(t *testing.T) {
	ctx := context.Background()
	t.Run("a good conversion", func(t *testing.T) {
		g := newFakeGitHub(t)
		app, err := convertManifest(ctx, g.srv.Client(), g.srv.URL, "abc123", "acme", testAppName)
		if err != nil {
			t.Fatal(err)
		}
		if app.ID != fakeAppID || app.Slug != "suiteward-abcd" || app.OwnerLogin != "acme" || app.OwnerType != "Organization" || string(app.PrivateKey) != string(testKeyPEM()) {
			t.Fatalf("converted app = %+v", app)
		}
		if !g.sawRequest("POST /app-manifests/abc123/conversions") {
			t.Fatalf("the conversion request was not sent: %v", g.requests)
		}
		if got := g.header("POST /app-manifests/abc123/conversions").Get("Authorization"); got != "" {
			t.Errorf("the conversion carries an Authorization header %q; the code is the credential", got)
		}
	})
	t.Run("a personal account", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.ownerLogin, g.ownerType = "alice", "User"
		app, err := convertManifest(ctx, g.srv.Client(), g.srv.URL, "abc123", accountPersonal, testAppName)
		if err != nil || app.OwnerLogin != "alice" {
			t.Fatalf("convertManifest = %+v, %v", app, err)
		}
	})
}

func TestConvertManifestRefusals(t *testing.T) {
	ctx := context.Background()
	refused := map[string]func(*fakeGitHub){
		"wrong permissions": func(g *fakeGitHub) {
			g.convertPerms = map[string]string{"metadata": "read", "pull_requests": "write", "contents": "write", "administration": "read", "checks": "write"}
		},
		"an extra permission": func(g *fakeGitHub) {
			g.convertPerms = map[string]string{"metadata": "read", "pull_requests": "write", "contents": "read", "administration": "read", "checks": "write", "issues": "read"}
		},
		"another organization": func(g *fakeGitHub) { g.ownerLogin = "other" },
		"a user owner for an organization": func(g *fakeGitHub) {
			g.ownerType = "User"
		},
	}
	for name, mutate := range refused {
		t.Run("refuses "+name, func(t *testing.T) {
			g := newFakeGitHub(t)
			mutate(g)
			_, err := convertManifest(ctx, g.srv.Client(), g.srv.URL, "abc123", "acme", testAppName)
			if err == nil {
				t.Fatal("the conversion was accepted")
			}
			if !strings.Contains(err.Error(), "suiteward-abcd") {
				t.Errorf("the error does not name the App to delete: %v", err)
			}
			for _, secret := range []string{pemBody(), "client-secret-value", "webhook-secret-value"} {
				if strings.Contains(err.Error(), secret) {
					t.Errorf("the error carries a secret of the conversion: %v", err)
				}
			}
		})
	}
	t.Run("refuses an organization owner for a personal App", func(t *testing.T) {
		g := newFakeGitHub(t)
		if _, err := convertManifest(ctx, g.srv.Client(), g.srv.URL, "abc123", accountPersonal, testAppName); err == nil {
			t.Fatal("an organization-owned App was accepted as personal")
		}
	})
}

func TestConvertManifestFailures(t *testing.T) {
	ctx := context.Background()
	t.Run("a refused status carries no answer text", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.convertStatus = http.StatusNotFound
		_, err := convertManifest(ctx, g.srv.Client(), g.srv.URL, "abc123", "acme", testAppName)
		if err == nil || !strings.Contains(err.Error(), "404") || strings.Contains(err.Error(), "leak-me-in-no-error") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a transport error does not carry the code", func(t *testing.T) {
		g := newFakeGitHub(t)
		url := g.srv.URL
		g.srv.Close()
		_, err := convertManifest(ctx, g.srv.Client(), url, "secretcode9", "acme", testAppName)
		if err == nil || strings.Contains(err.Error(), "secretcode9") {
			t.Fatalf("error = %v", err)
		}
	})
	t.Run("a code that is not a code is refused before any request", func(t *testing.T) {
		g := newFakeGitHub(t)
		for _, code := range []string{"", "../x", "a/b", "a b", "a?b=c"} {
			if _, err := convertManifest(ctx, g.srv.Client(), g.srv.URL, code, "acme", testAppName); err == nil {
				t.Errorf("code %q was accepted", code)
			}
		}
		if len(g.requests) != 0 {
			t.Fatalf("requests were sent: %v", g.requests)
		}
	})
}

func TestParseManifestCode(t *testing.T) {
	cases := []struct {
		name, input string
		want        string
		wantErr     bool
	}{
		{"a bare code", "  abc123 \n", "abc123", false},
		{"a callback URL", "http://127.0.0.1:9/callback?code=abc123&state=s1", "abc123", false},
		{"a callback URL with the wrong state", "http://127.0.0.1:9/callback?code=abc123&state=other", "", true},
		{"a URL with no code", "http://127.0.0.1:9/callback?state=s1", "", true},
		{"a URL without a state", "http://127.0.0.1:9/callback?code=abc123", "abc123", false},
		{"empty", "  ", "", true},
		{"not a code", "abc 123", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseManifestCode(tc.input, "s1")
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("parseManifestCode(%q) = %q, %v", tc.input, got, err)
			}
			if err != nil && strings.Contains(err.Error(), "abc123") {
				t.Errorf("the error echoes the code: %v", err)
			}
		})
	}
}

// manifestEnv is an environment for the manifest step alone; in is the terminal input.
func manifestEnv(t *testing.T, in io.Reader, terminal bool) (githubEnv, *syncBuffer) {
	t.Helper()
	return manifestEnvWith(t, in, terminal, func() ([]byte, error) { return nil, errors.New("no password here") })
}

// manifestEnvWith is manifestEnv with the reader that answers a hidden question.
func manifestEnvWith(t *testing.T, in io.Reader, terminal bool, readPassword func() ([]byte, error)) (githubEnv, *syncBuffer) {
	t.Helper()
	out := &syncBuffer{}
	r, err := NewResolver(map[string]string{}, map[string]string{}, Terminal{In: in, Out: out, IsTerminal: terminal, ReadPassword: readPassword})
	if err != nil {
		t.Fatal(err)
	}
	return githubEnv{R: r, Out: out, WebURL: "https://github.example", Tier: config.TierCoLocated, ManifestWait: 5 * time.Second, PasteAfter: time.Hour}, out
}

type codeResult struct {
	code, name string
	err        error
}

func startManifest(env githubEnv, account string) <-chan codeResult {
	done := make(chan codeResult, 1)
	go func() {
		code, name, err := obtainManifestCode(context.Background(), env, account)
		done <- codeResult{code, name, err}
	}()
	return done
}

func await(t *testing.T, done <-chan codeResult) codeResult {
	t.Helper()
	select {
	case res := <-done:
		return res
	case <-time.After(10 * time.Second):
		t.Fatal("obtainManifestCode did not return")
		return codeResult{}
	}
}

func TestManifestPageCarriesTheFiveAndTheState(t *testing.T) {
	cases := map[string]string{
		accountPersonal: "https://github.example/settings/apps/new?state=",
		"acme":          "https://github.example/organizations/acme/settings/apps/new?state=",
	}
	for account, wantPrefix := range cases {
		t.Run(account, func(t *testing.T) {
			env, out := manifestEnv(t, strings.NewReader(""), false)
			done := startManifest(env, account)
			base := waitForListener(t, out)
			action, manifest := fetchForm(t, base)
			if !strings.HasPrefix(action, wantPrefix) || len(stateOf(t, action)) < 16 {
				t.Fatalf("form action = %q, want prefix %q and a random state", action, wantPrefix)
			}
			name := checkManifest(t, base, manifest)
			if !strings.Contains(out.String(), name) {
				t.Errorf("the App name %q was not printed before the form is sent:\n%s", name, out.String())
			}
			if status := callback(t, base, "thecode1", stateOf(t, action)); status != http.StatusOK {
				t.Errorf("the callback answered %d", status)
			}
			if res := await(t, done); res.err != nil || res.code != "thecode1" || res.name != name {
				t.Fatalf("result = %+v", res)
			}
		})
	}
}

func TestManifestCallbackRefusesAWrongStateAndKeepsWaiting(t *testing.T) {
	env, out := manifestEnv(t, strings.NewReader(""), false)
	done := startManifest(env, "acme")
	base := waitForListener(t, out)
	action, _ := fetchForm(t, base)
	if status := callback(t, base, "evilcode", "not-the-state"); status != http.StatusBadRequest {
		t.Errorf("a wrong state answered %d, want 400", status)
	}
	select {
	case res := <-done:
		t.Fatalf("a wrong state ended the wait: %+v", res)
	case <-time.After(50 * time.Millisecond):
	}
	callback(t, base, "goodcode", stateOf(t, action))
	if res := await(t, done); res.err != nil || res.code != "goodcode" {
		t.Fatalf("result = %+v", res)
	}
	if strings.Contains(out.String(), "evilcode") || strings.Contains(out.String(), "goodcode") {
		t.Errorf("the code was printed:\n%s", out.String())
	}
}

func TestManifestListenerRefusesAnotherHost(t *testing.T) {
	env, out := manifestEnv(t, strings.NewReader(""), false)
	env.ManifestWait = 300 * time.Millisecond
	done := startManifest(env, "acme")
	base := waitForListener(t, out)
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, base, nil)
	req.Host = "evil.example"
	resp, err := http.DefaultClient.Do(req) //nolint:gosec // a loopback test server
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("a request for another host answered %d, want 403", resp.StatusCode)
	}
	await(t, done)
}

func TestManifestTimesOut(t *testing.T) {
	env, _ := manifestEnv(t, strings.NewReader(""), false)
	env.ManifestWait = 50 * time.Millisecond
	res := await(t, startManifest(env, "acme"))
	if res.err == nil || !strings.Contains(res.err.Error(), "timed out") {
		t.Fatalf("result = %+v", res)
	}
}

func TestManifestIsolatedPrintsThePortForward(t *testing.T) {
	env, out := manifestEnv(t, strings.NewReader(""), false)
	env.Tier = config.TierIsolated
	env.ManifestWait = 200 * time.Millisecond
	done := startManifest(env, "acme")
	base := waitForListener(t, out)
	port := strings.TrimSuffix(strings.TrimPrefix(base, "http://127.0.0.1:"), "/")
	text := out.String()
	for _, want := range []string{"from your computer, forward the port first, then open the address on the computer", "ssm start-session", "AWS-StartPortForwardingSession", `"portNumber":["` + port + `"]`, `"localPortNumber":["` + port + `"]`} {
		if !strings.Contains(text, want) {
			t.Errorf("the output lacks %q:\n%s", want, text)
		}
	}
	await(t, done)
}

func TestManifestCoLocatedPrintsNoPortForward(t *testing.T) {
	env, out := manifestEnv(t, strings.NewReader(""), false)
	env.ManifestWait = 100 * time.Millisecond
	done := startManifest(env, "acme")
	waitForListener(t, out)
	await(t, done)
	if strings.Contains(out.String(), "ssm") {
		t.Errorf("a co-located install printed a port-forward hint:\n%s", out.String())
	}
}

// checkManifest asserts the manifest the page sends: the five permissions, no webhook, private, the loopback redirect.
func checkManifest(t *testing.T, base string, manifest []byte) (name string) {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(manifest, &m); err != nil {
		t.Fatalf("the manifest is not JSON: %v\n%s", err, manifest)
	}
	if _, has := m["hook_attributes"]; has {
		t.Error("the manifest carries hook_attributes; the webhook is off")
	}
	if m["public"] != false || m["url"] != "https://github.com/IgnisDevNE/SuiteWard" || m["redirect_url"] != strings.TrimSuffix(base, "/")+"/callback" {
		t.Errorf("manifest = %v", m)
	}
	name, _ = m["name"].(string)
	if !strings.HasPrefix(name, "suiteward-") || len(name) > 34 {
		t.Errorf("name = %q", name)
	}
	perms, _ := m["default_permissions"].(map[string]any)
	if len(perms) != len(appPermissions) {
		t.Errorf("default_permissions = %v, want exactly the five", perms)
	}
	for k, v := range appPermissions {
		if perms[k] != v {
			t.Errorf("default_permissions[%s] = %v, want %s", k, perms[k], v)
		}
	}
	return name
}

func TestConvertManifestNamesTheAppWhenTheAnswerIsUnusable(t *testing.T) {
	for name, raw := range map[string]string{"undecodable": "not json at all", "incomplete": `{"id": 0}`, "without a key": `{"id": 5, "slug": "x", "owner": {"login": "acme"}}`} {
		t.Run(name, func(t *testing.T) {
			g := newFakeGitHub(t)
			g.convertRaw = raw
			_, err := convertManifest(context.Background(), g.srv.Client(), g.srv.URL, "abc123", "acme", testAppName)
			if err == nil || !strings.Contains(err.Error(), testAppName) {
				t.Fatalf("error = %v, want one that names the App %s so an orphan can be found", err, testAppName)
			}
		})
	}
	t.Run("a code from an earlier attempt has no name to give", func(t *testing.T) {
		g := newFakeGitHub(t)
		g.convertRaw = "not json at all"
		_, err := convertManifest(context.Background(), g.srv.Client(), g.srv.URL, "abc123", "acme", "")
		if err == nil || strings.Contains(err.Error(), "named") {
			t.Fatalf("error = %v", err)
		}
	})
}
