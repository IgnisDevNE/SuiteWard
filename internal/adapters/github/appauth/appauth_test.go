package appauth_test

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/appauth"
)

const (
	appID          = 12345
	installationID = 678
)

var (
	t0           = time.Date(2026, 10, 11, 12, 0, 0, 0, time.UTC)
	repositories = []string{"alpha", "beta"}
	syncPerms    = map[string]string{"metadata": "read", "pull_requests": "read", "contents": "read"}
	// testKey is generated once per test binary; nothing is read from disk.
	testKey = sync.OnceValue(func() *rsa.PrivateKey {
		k, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		return k
	})
)

func pkcs1PEM(k *rsa.PrivateKey) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)})
}

func pkcs8PEM(t *testing.T, k any) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(k)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// fakeClock is the injected time source; tests move it explicitly.
type fakeClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fakeClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fakeClock) Set(now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.now = now
}

type capturedRequest struct {
	method, path string
	header       http.Header
	body         []byte
}

// fakeGitHub serves POST /app/installations/{id}/access_tokens; respond gets
// the 1-based number of the request and returns the status and body.
type fakeGitHub struct {
	srv     *httptest.Server
	mu      sync.Mutex
	reqs    []capturedRequest
	respond func(n int, r *http.Request) (int, string)
}

func newFakeGitHub(t *testing.T, respond func(n int, r *http.Request) (int, string)) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{respond: respond}
	g.srv = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read body", http.StatusBadRequest)
			return
		}
		g.mu.Lock()
		g.reqs = append(g.reqs, capturedRequest{r.Method, r.URL.Path, r.Header.Clone(), body})
		n := len(g.reqs)
		g.mu.Unlock()
		status, out := g.respond(n, r)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, out) // the client may already be gone; the test asserts on its side
	}))
	g.srv.Config.ErrorLog = log.New(io.Discard, "", 0) // keeps server messages out of the package-logs check
	g.srv.Start()
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) requests() []capturedRequest {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]capturedRequest(nil), g.reqs...)
}

func tokenBody(value string, expires time.Time, perms map[string]string) string {
	b, err := json.Marshal(map[string]any{"token": value, "expires_at": expires.Format(time.RFC3339), "permissions": perms})
	if err != nil {
		panic(err)
	}
	return string(b)
}

func tokenValue(n int) string { return fmt.Sprintf("ghs_testTokenValue%04d", n) }

// issuing answers every request with a fresh token valid for one hour and the requested permissions.
func issuing(clock *fakeClock) func(int, *http.Request) (int, string) {
	return func(n int, _ *http.Request) (int, string) {
		return http.StatusCreated, tokenBody(tokenValue(n), clock.Now().Add(time.Hour), syncPerms)
	}
}

func newApp(t *testing.T, clock *fakeClock) appauth.App {
	t.Helper()
	app, err := appauth.NewApp(appID, pkcs1PEM(testKey()), clock.Now)
	if err != nil {
		t.Fatalf("NewApp: %v", err)
	}
	return app
}

func newInstallation(t *testing.T, clock *fakeClock, g *fakeGitHub) *appauth.Installation {
	t.Helper()
	inst, err := newApp(t, clock).Installation(installationID, repositories, syncPerms, g.srv.Client(), g.srv.URL)
	if err != nil {
		t.Fatalf("Installation: %v", err)
	}
	return inst
}

// bearerJWT returns the JWT of a captured request.
func bearerJWT(t *testing.T, r capturedRequest) string {
	t.Helper()
	jwt, ok := strings.CutPrefix(r.header.Get("Authorization"), "Bearer ")
	if !ok {
		t.Fatalf("Authorization is not a bearer credential")
	}
	return jwt
}

// verifyJWT checks the RS256 signature with pub and returns the decoded header and claims.
func verifyJWT(t *testing.T, jwt string, pub *rsa.PublicKey) (string, map[string]any) {
	t.Helper()
	parts := strings.Split(jwt, ".")
	if len(parts) != 3 {
		t.Fatalf("JWT has %d parts, want 3", len(parts))
	}
	enc := base64.RawURLEncoding
	header, err := enc.DecodeString(parts[0])
	if err != nil {
		t.Fatalf("header: %v", err)
	}
	payload, err := enc.DecodeString(parts[1])
	if err != nil {
		t.Fatalf("claims: %v", err)
	}
	sig, err := enc.DecodeString(parts[2])
	if err != nil {
		t.Fatalf("signature: %v", err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(pub, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
	var claims map[string]any
	dec := json.NewDecoder(bytes.NewReader(payload))
	dec.UseNumber()
	if err := dec.Decode(&claims); err != nil {
		t.Fatalf("claims: %v", err)
	}
	return string(header), claims
}

func TestJWTIsRS256WithExactClaimsAtFixedClock(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: t0}
	g := newFakeGitHub(t, issuing(clock))
	if _, err := newInstallation(t, clock, g).Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	reqs := g.requests()
	if len(reqs) != 1 {
		t.Fatalf("got %d requests, want 1", len(reqs))
	}
	header, claims := verifyJWT(t, bearerJWT(t, reqs[0]), &testKey().PublicKey)
	if header != `{"alg":"RS256","typ":"JWT"}` {
		t.Errorf("header = %s", header)
	}
	want := map[string]any{
		"iat": json.Number(fmt.Sprint(t0.Add(-60 * time.Second).Unix())),
		"exp": json.Number(fmt.Sprint(t0.Add(9 * time.Minute).Unix())),
		"iss": "12345",
	}
	if !maps.Equal(claims, want) {
		t.Errorf("claims = %v, want exactly %v", claims, want)
	}
}

func TestNewAppAcceptsPKCS1AndPKCS8(t *testing.T) {
	t.Parallel()
	for name, keyPEM := range map[string][]byte{"PKCS#1": pkcs1PEM(testKey()), "PKCS#8": pkcs8PEM(t, testKey())} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			clock := &fakeClock{now: t0}
			g := newFakeGitHub(t, issuing(clock))
			app, err := appauth.NewApp(appID, keyPEM, clock.Now)
			if err != nil {
				t.Fatalf("NewApp: %v", err)
			}
			inst, err := app.Installation(installationID, repositories, syncPerms, g.srv.Client(), g.srv.URL)
			if err != nil {
				t.Fatalf("Installation: %v", err)
			}
			if _, err := inst.Token(context.Background()); err != nil {
				t.Fatalf("Token: %v", err)
			}
			verifyJWT(t, bearerJWT(t, g.requests()[0]), &testKey().PublicKey)
		})
	}
}

func TestNewAppRejectsOtherKeysWithoutEchoingInput(t *testing.T) {
	t.Parallel()
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	sec1, err := x509.MarshalECPrivateKey(ecKey)
	if err != nil {
		t.Fatal(err)
	}
	noise := make([]byte, 256)
	if _, err := rand.Read(noise); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name, mentions string
		pem            []byte
	}{
		{"empty", "PEM", nil},
		{"garbage", "PEM", []byte("not a key: SECRETMARKER-garbage")},
		{"EC SEC1", "RSA", pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1})},
		{"EC PKCS#8", "RSA", pkcs8PEM(t, ecKey)},
		{"encrypted PKCS#1", "encrypted", pem.EncodeToMemory(&pem.Block{
			Type:    "RSA PRIVATE KEY",
			Headers: map[string]string{"Proc-Type": "4,ENCRYPTED", "DEK-Info": "AES-256-CBC,00112233445566778899AABBCCDDEEFF"},
			Bytes:   noise,
		})},
		{"encrypted PKCS#8", "encrypted", pem.EncodeToMemory(&pem.Block{Type: "ENCRYPTED PRIVATE KEY", Bytes: noise})},
		{"corrupt PKCS#1", "PKCS#1", pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: noise})},
		{"corrupt PKCS#8", "PKCS#8", pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: noise})},
		{"certificate", "PEM", pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: noise})},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := appauth.NewApp(appID, c.pem, (&fakeClock{now: t0}).Now)
			if err == nil {
				t.Fatal("NewApp accepted the key")
			}
			msg := err.Error()
			if !strings.Contains(msg, c.mentions) {
				t.Errorf("error %q does not name the problem (%s)", msg, c.mentions)
			}
			for _, leak := range []string{"SECRETMARKER", base64.StdEncoding.EncodeToString(noise)[:16], "AES-256-CBC", fmt.Sprintf("%x", noise[:8])} {
				if strings.Contains(msg, leak) {
					t.Errorf("error %q echoes the input", msg)
				}
			}
			if len(c.pem) > 0 && strings.Contains(msg, string(c.pem)) {
				t.Errorf("error echoes the PEM")
			}
		})
	}
}

func TestNewAppRejectsInvalidArguments(t *testing.T) {
	t.Parallel()
	key := pkcs1PEM(testKey())
	clock := (&fakeClock{now: t0}).Now
	if _, err := appauth.NewApp(appID, key, clock); err != nil {
		t.Fatalf("valid arguments rejected: %v", err)
	}
	for name, call := range map[string]func() error{
		"zero app id":     func() error { _, err := appauth.NewApp(0, key, clock); return err },
		"negative app id": func() error { _, err := appauth.NewApp(-1, key, clock); return err },
		"nil clock":       func() error { _, err := appauth.NewApp(appID, key, nil); return err },
	} {
		if call() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestInstallationRejectsInvalidArguments(t *testing.T) {
	t.Parallel()
	app := newApp(t, &fakeClock{now: t0})
	client := http.DefaultClient
	base := "https://api.github.example"
	if _, err := app.Installation(installationID, repositories, syncPerms, client, base); err != nil {
		t.Fatalf("valid arguments rejected: %v", err)
	}
	cases := map[string]func() error{
		"zero id":          func() error { _, err := app.Installation(0, repositories, syncPerms, client, base); return err },
		"negative id":      func() error { _, err := app.Installation(-5, repositories, syncPerms, client, base); return err },
		"nil repositories": func() error { _, err := app.Installation(installationID, nil, syncPerms, client, base); return err },
		"empty repositories": func() error {
			_, err := app.Installation(installationID, []string{}, syncPerms, client, base)
			return err
		},
		"empty repository name": func() error {
			_, err := app.Installation(installationID, []string{"alpha", ""}, syncPerms, client, base)
			return err
		},
		"repository with owner": func() error {
			_, err := app.Installation(installationID, []string{"owner/alpha"}, syncPerms, client, base)
			return err
		},
		"nil permissions": func() error { _, err := app.Installation(installationID, repositories, nil, client, base); return err },
		"empty permissions": func() error {
			_, err := app.Installation(installationID, repositories, map[string]string{}, client, base)
			return err
		},
		"empty permission level": func() error {
			_, err := app.Installation(installationID, repositories, map[string]string{"contents": ""}, client, base)
			return err
		},
		"empty permission name": func() error {
			_, err := app.Installation(installationID, repositories, map[string]string{"": "read"}, client, base)
			return err
		},
		"nil http client": func() error {
			_, err := app.Installation(installationID, repositories, syncPerms, nil, base)
			return err
		},
		"empty base URL": func() error {
			_, err := app.Installation(installationID, repositories, syncPerms, client, "")
			return err
		},
		"relative base URL": func() error {
			_, err := app.Installation(installationID, repositories, syncPerms, client, "api/v3")
			return err
		},
		"non-HTTP base URL": func() error {
			_, err := app.Installation(installationID, repositories, syncPerms, client, "ftp://api.github.example")
			return err
		},
		"base URL with a query": func() error {
			_, err := app.Installation(installationID, repositories, syncPerms, client, base+"/api/v3?x=1")
			return err
		},
		"base URL with an empty query": func() error {
			_, err := app.Installation(installationID, repositories, syncPerms, client, base+"/api/v3?")
			return err
		},
		"base URL with a fragment": func() error {
			_, err := app.Installation(installationID, repositories, syncPerms, client, base+"/api/v3#frag")
			return err
		},
		"base URL with user info": func() error {
			_, err := app.Installation(installationID, repositories, syncPerms, client, "https://user:pass@api.github.example")
			return err
		},
		"duplicate repository": func() error {
			_, err := app.Installation(installationID, []string{"alpha", "beta", "alpha"}, syncPerms, client, base)
			return err
		},
		"duplicate repository differing in case": func() error {
			_, err := app.Installation(installationID, []string{"alpha", "Alpha"}, syncPerms, client, base)
			return err
		},
	}
	for name, call := range cases {
		if call() == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

func TestTokenRequestCarriesHeadersJWTAndExactBody(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: t0}
	g := newFakeGitHub(t, issuing(clock))
	tok, err := newInstallation(t, clock, g).Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if tok.Reveal() != tokenValue(1) {
		t.Errorf("Reveal returned another value")
	}
	if !tok.ExpiresAt().Equal(t0.Add(time.Hour)) {
		t.Errorf("ExpiresAt = %v, want %v", tok.ExpiresAt(), t0.Add(time.Hour))
	}
	r := g.requests()[0]
	if r.method != http.MethodPost || r.path != "/app/installations/678/access_tokens" {
		t.Errorf("request = %s %s", r.method, r.path)
	}
	for name, want := range map[string]string{
		"Accept":               "application/vnd.github+json",
		"X-GitHub-Api-Version": "2022-11-28",
		"Content-Type":         "application/json",
	} {
		if got := r.header.Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	verifyJWT(t, bearerJWT(t, r), &testKey().PublicKey)
	want := `{"repositories":["alpha","beta"],"permissions":{"contents":"read","metadata":"read","pull_requests":"read"}}`
	if string(r.body) != want {
		t.Errorf("body = %s\nwant   %s", r.body, want)
	}
}

func TestTokenUsesBaseURLPathPrefix(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: t0}
	g := newFakeGitHub(t, issuing(clock))
	inst, err := newApp(t, clock).Installation(installationID, repositories, syncPerms, g.srv.Client(), g.srv.URL+"/api/v3/")
	if err != nil {
		t.Fatalf("Installation: %v", err)
	}
	if _, err := inst.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if got := g.requests()[0].path; got != "/api/v3/app/installations/678/access_tokens" {
		t.Errorf("path = %s", got)
	}
}

func TestInstallationKeepsItsOwnCopyOfTheRequest(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: t0}
	g := newFakeGitHub(t, issuing(clock))
	repos := []string{"alpha", "beta"}
	perms := maps.Clone(syncPerms)
	inst, err := newApp(t, clock).Installation(installationID, repos, perms, g.srv.Client(), g.srv.URL)
	if err != nil {
		t.Fatalf("Installation: %v", err)
	}
	repos[0] = "gamma"
	perms["checks"] = "write"
	if _, err := inst.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	want := `{"repositories":["alpha","beta"],"permissions":{"contents":"read","metadata":"read","pull_requests":"read"}}`
	if got := string(g.requests()[0].body); got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestTokenIsReusedUntilFiveMinutesBeforeExpiry(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: t0}
	g := newFakeGitHub(t, issuing(clock))
	inst := newInstallation(t, clock, g)
	ctx := context.Background()

	first, err := inst.Token(ctx)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	clock.Set(t0.Add(55*time.Minute - time.Second))
	again, err := inst.Token(ctx)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if n := len(g.requests()); n != 1 {
		t.Fatalf("before the renewal point: %d requests, want 1", n)
	}
	if again.Reveal() != first.Reveal() || again.Identity() != first.Identity() {
		t.Errorf("cached token was not reused")
	}

	clock.Set(t0.Add(55 * time.Minute))
	renewed, err := inst.Token(ctx)
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	reqs := g.requests()
	if len(reqs) != 2 {
		t.Fatalf("at the renewal point: %d requests, want 2", len(reqs))
	}
	if renewed.Reveal() != tokenValue(2) || renewed.Identity() == first.Identity() {
		t.Errorf("token was not renewed")
	}
	_, claims := verifyJWT(t, bearerJWT(t, reqs[1]), &testKey().PublicKey)
	if want := json.Number(fmt.Sprint(t0.Add(54 * time.Minute).Unix())); claims["iat"] != want {
		t.Errorf("renewal JWT iat = %v, want %v from the injected clock", claims["iat"], want)
	}
}

func TestConcurrentTokenCallsMintOnce(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: t0}
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	arrived := make(chan struct{}, 1)
	g := newFakeGitHub(t, func(n int, r *http.Request) (int, string) {
		select {
		case arrived <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		return issuing(clock)(n, r)
	})
	defer unblock()
	inst := newInstallation(t, clock, g)

	type result struct {
		tok appauth.Token
		err error
	}
	const callers = 16
	results := make(chan result, callers)
	var started sync.WaitGroup
	started.Add(callers)
	for range callers {
		go func() {
			started.Done()
			tok, err := inst.Token(context.Background())
			results <- result{tok, err}
		}()
	}
	started.Wait()
	select {
	case <-arrived:
	case r := <-results:
		t.Fatalf("Token returned before any request reached GitHub: %v", r.err)
	}
	unblock()
	for range callers {
		r := <-results
		if r.err != nil {
			t.Fatalf("Token: %v", r.err)
		}
		if r.tok.Reveal() != tokenValue(1) {
			t.Errorf("a caller got another token")
		}
	}
	if n := len(g.requests()); n != 1 {
		t.Errorf("%d concurrent calls made %d requests, want 1", callers, n)
	}
}

func TestTokenWaitHonorsContext(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: t0}
	release := make(chan struct{})
	var releaseOnce sync.Once
	unblock := func() { releaseOnce.Do(func() { close(release) }) }
	arrived := make(chan struct{}, 1)
	g := newFakeGitHub(t, func(n int, r *http.Request) (int, string) {
		select {
		case arrived <- struct{}{}:
		default:
		}
		select {
		case <-release:
		case <-r.Context().Done():
		}
		return issuing(clock)(n, r)
	})
	defer unblock()
	inst := newInstallation(t, clock, g)

	first := make(chan error, 1)
	go func() {
		_, err := inst.Token(context.Background())
		first <- err
	}()
	select {
	case <-arrived:
	case err := <-first:
		t.Fatalf("Token returned before any request reached GitHub: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := inst.Token(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("waiting caller with a cancelled context: err = %v, want context.Canceled", err)
	}
	unblock()
	if err := <-first; err != nil {
		t.Fatalf("minting caller: %v", err)
	}
	if n := len(g.requests()); n != 1 {
		t.Errorf("%d requests, want 1", n)
	}
}

func TestTokenFailuresAreErrorsAndNothingIsCached(t *testing.T) {
	t.Parallel()
	wider := maps.Clone(syncPerms)
	wider["checks"] = "write"
	narrower := map[string]string{"metadata": "read", "contents": "read"}
	upgraded := maps.Clone(syncPerms)
	upgraded["contents"] = "write"
	cases := []struct {
		name   string
		status int
		body   func(now time.Time) string
		// wantStatus is the StatusError status, or 0 when another error is expected.
		wantStatus int
		wantErr    error
	}{
		{"401", http.StatusUnauthorized, func(time.Time) string { return `{"message":"Bad credentials"}` }, 401, nil},
		{"403", http.StatusForbidden, func(time.Time) string { return `{"message":"Forbidden"}` }, 403, nil},
		{"404", http.StatusNotFound, func(time.Time) string { return `{"message":"Not Found"}` }, 404, nil},
		{"422", http.StatusUnprocessableEntity, func(time.Time) string { return `{"message":"Validation Failed"}` }, 422, nil},
		{"200 instead of 201", http.StatusOK, func(now time.Time) string { return tokenBody(tokenValue(1), now.Add(time.Hour), syncPerms) }, 200, nil},
		{"wider permissions", http.StatusCreated, func(now time.Time) string { return tokenBody(tokenValue(1), now.Add(time.Hour), wider) }, 0, appauth.ErrPermissionsMismatch},
		{"narrower permissions", http.StatusCreated, func(now time.Time) string { return tokenBody(tokenValue(1), now.Add(time.Hour), narrower) }, 0, appauth.ErrPermissionsMismatch},
		{"different level", http.StatusCreated, func(now time.Time) string { return tokenBody(tokenValue(1), now.Add(time.Hour), upgraded) }, 0, appauth.ErrPermissionsMismatch},
		{"no token", http.StatusCreated, func(now time.Time) string { return tokenBody("", now.Add(time.Hour), syncPerms) }, 0, nil},
		{"already expired", http.StatusCreated, func(now time.Time) string { return tokenBody(tokenValue(1), now.Add(-time.Minute), syncPerms) }, 0, nil},
		{"not JSON", http.StatusCreated, func(time.Time) string { return "<html>" }, 0, nil},
		{"no expires_at", http.StatusCreated, func(time.Time) string {
			return fmt.Sprintf(`{"token":%q,"permissions":{"metadata":"read","pull_requests":"read","contents":"read"}}`, tokenValue(1))
		}, 0, nil},
		{"malformed expires_at", http.StatusCreated, func(time.Time) string {
			return fmt.Sprintf(`{"token":%q,"expires_at":"tomorrow","permissions":{"metadata":"read","pull_requests":"read","contents":"read"}}`, tokenValue(1))
		}, 0, nil},
		{"expires inside the renewal window", http.StatusCreated, func(now time.Time) string {
			return tokenBody(tokenValue(1), now.Add(4*time.Minute), syncPerms)
		}, 0, nil},
		{"expires at the renewal point", http.StatusCreated, func(now time.Time) string {
			return tokenBody(tokenValue(1), now.Add(5*time.Minute), syncPerms)
		}, 0, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			clock := &fakeClock{now: t0}
			g := newFakeGitHub(t, func(n int, r *http.Request) (int, string) {
				if n == 1 {
					return c.status, c.body(clock.Now())
				}
				return issuing(clock)(n, r)
			})
			inst := newInstallation(t, clock, g)
			_, err := inst.Token(context.Background())
			if err == nil {
				t.Fatal("Token accepted the answer")
			}
			if strings.Contains(err.Error(), tokenValue(1)) {
				t.Errorf("error %q carries the token value", err)
			}
			var se *appauth.StatusError
			switch {
			case c.wantStatus != 0:
				if !errors.As(err, &se) || se.Status != c.wantStatus {
					t.Errorf("err = %v, want a StatusError with status %d", err, c.wantStatus)
				}
			case c.wantErr != nil:
				if !errors.Is(err, c.wantErr) {
					t.Errorf("err = %v, want %v", err, c.wantErr)
				}
			case errors.As(err, &se):
				t.Errorf("err = %v, want a malformed-answer error, not a StatusError", err)
			}
			tok, err := inst.Token(context.Background())
			if err != nil {
				t.Fatalf("next Token: %v", err)
			}
			if n := len(g.requests()); n != 2 || tok.Reveal() != tokenValue(2) {
				t.Errorf("the failed answer was cached: %d requests", n)
			}
		})
	}
}

func TestSecretsAreNeverRendered(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: t0}
	g := newFakeGitHub(t, issuing(clock))
	app := newApp(t, clock)
	inst, err := app.Installation(installationID, repositories, syncPerms, g.srv.Client(), g.srv.URL)
	if err != nil {
		t.Fatalf("Installation: %v", err)
	}
	tok, err := inst.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	var text, js bytes.Buffer
	slog.New(slog.NewTextHandler(&text, nil)).Info("render", "tok", tok, "inst", inst, "app", app)
	slog.New(slog.NewJSONHandler(&js, nil)).Info("render", "tok", tok, "inst", inst, "app", app)
	marshalled, err := json.Marshal(tok)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	rendered := map[string]string{
		"token verbs":        fmt.Sprintf("%v %+v %#v %s", tok, tok, tok, tok),
		"token other verbs":  fmt.Sprintf("%q %x %X %d", tok, tok, tok, tok),
		"token methods":      tok.String() + tok.GoString(),
		"installation verbs": fmt.Sprintf("%v %+v %#v %s %x", inst, inst, inst, inst, inst),
		"app verbs":          fmt.Sprintf("%v %+v %#v %s %x %d", app, app, app, app, app, app),
		"slog text":          text.String(),
		"slog JSON":          js.String(),
		"json":               string(marshalled),
	}
	secrets := map[string]string{
		"token value":      tokenValue(1),
		"key exponent":     testKey().D.String(),
		"key exponent hex": testKey().D.Text(16),
	}
	for where, s := range rendered {
		for what, secret := range secrets {
			if strings.Contains(s, secret) {
				t.Errorf("%s render the %s", where, what)
			}
		}
	}
}

// lockedBuffer collects writes from any goroutine.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// TestPackageLogsNothing replaces the process-wide loggers, so it is not parallel.
func TestPackageLogsNothing(t *testing.T) {
	var out lockedBuffer
	prevSlog, prevWriter, prevFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewTextHandler(&out, &slog.HandlerOptions{Level: slog.LevelDebug})))
	log.SetOutput(&out)
	t.Cleanup(func() {
		slog.SetDefault(prevSlog)
		log.SetOutput(prevWriter)
		log.SetFlags(prevFlags)
	})

	clock := &fakeClock{now: t0}
	wider := maps.Clone(syncPerms)
	wider["checks"] = "write"
	g := newFakeGitHub(t, func(n int, r *http.Request) (int, string) {
		switch n {
		case 1:
			return http.StatusForbidden, `{"message":"Forbidden"}`
		case 2:
			return http.StatusCreated, tokenBody(tokenValue(n), clock.Now().Add(time.Hour), wider)
		default:
			return issuing(clock)(n, r)
		}
	})
	inst := newInstallation(t, clock, g)
	for range 2 {
		if _, err := inst.Token(context.Background()); err == nil {
			t.Fatal("Token accepted a failing answer")
		}
	}
	if _, err := inst.Token(context.Background()); err != nil {
		t.Fatalf("Token: %v", err)
	}
	if s := out.String(); s != "" {
		t.Errorf("the package logged: %q", s)
	}
}

func TestIdentityIsStableAndDiffersPerToken(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: t0}
	g := newFakeGitHub(t, issuing(clock))
	inst := newInstallation(t, clock, g)
	first, err := inst.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	id := first.Identity()
	if id == "" || id != first.Identity() {
		t.Fatalf("Identity is not stable for one token")
	}
	if strings.Contains(id, first.Reveal()) || strings.Contains(first.Reveal(), id) {
		t.Errorf("Identity exposes the value")
	}
	cached, err := inst.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if cached.Identity() != id {
		t.Errorf("Identity changed without a rotation")
	}
	clock.Set(t0.Add(time.Hour))
	second, err := inst.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	if second.Identity() == id {
		t.Errorf("Identity did not change on rotation")
	}
}

// renderings prints v in every verb position the package must survive.
func renderings(v any) string {
	return fmt.Sprintf("%v %+v %#v %s %q %x %d", v, v, v, v, v, v, v)
}

func TestNoTokenValueIsPrintedInAnyPosition(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: t0}
	g := newFakeGitHub(t, issuing(clock))
	inst := newInstallation(t, clock, g)
	tok, err := inst.Token(context.Background())
	if err != nil {
		t.Fatalf("Token: %v", err)
	}
	// An unexported field is printed by reflection, which cannot call methods.
	type hasToken struct{ tok appauth.Token }
	type hasInstallation struct{ inst *appauth.Installation }
	type hasInstallationValue struct{ inst appauth.Installation }
	rendered := map[string]string{
		"installation pointer":          renderings(inst),
		"installation value":            renderings(*inst),
		"unexported token field":        renderings(hasToken{tok}),
		"unexported token field ptr":    renderings(&hasToken{tok}),
		"unexported installation ptr":   renderings(hasInstallation{inst}),
		"unexported installation val":   renderings(hasInstallationValue{*inst}),
		"token slice":                   renderings([]appauth.Token{tok}),
		"token map":                     renderings(map[string]appauth.Token{"k": tok}),
		"installation value in a slice": renderings([]appauth.Installation{*inst}),
	}
	for where, s := range rendered {
		if strings.Contains(s, tokenValue(1)) {
			t.Errorf("%s prints the token value: %s", where, s)
		}
	}
	if tok.Reveal() != tokenValue(1) {
		t.Errorf("Reveal = %q, want the token value", tok.Reveal())
	}
	if again, err := inst.Token(context.Background()); err != nil || again.Reveal() != tokenValue(1) {
		t.Errorf("the cached token lost its value: %v", err)
	}
}

func TestRevealOnTheZeroTokenFailsLoudly(t *testing.T) {
	t.Parallel()
	var tok appauth.Token
	if s := renderings(tok); strings.Contains(s, "ghs_") {
		t.Errorf("zero Token prints a value: %s", s)
	}
	defer func() {
		if recover() == nil {
			t.Error("Reveal on the zero Token returned instead of panicking")
		}
	}()
	_ = tok.Reveal()
}

func TestCancellingTheMintingCallerReturnsContextCanceledAndCachesNothing(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: t0}
	arrived := make(chan struct{}, 1)
	g := newFakeGitHub(t, func(n int, r *http.Request) (int, string) {
		if n == 1 {
			arrived <- struct{}{}
			<-r.Context().Done()
		}
		return issuing(clock)(n, r)
	})
	inst := newInstallation(t, clock, g)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := inst.Token(ctx)
		done <- err
	}()
	<-arrived
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	tok, err := inst.Token(context.Background())
	if err != nil {
		t.Fatalf("next Token: %v", err)
	}
	if n := len(g.requests()); n != 2 || tok.Reveal() != tokenValue(2) {
		t.Errorf("the cancelled mint was cached: %d requests", n)
	}
}

func TestPrintingAnInstallationWhileMintingIsRaceFree(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: t0}
	g := newFakeGitHub(t, issuing(clock))
	inst := newInstallation(t, clock, g)
	stop := make(chan struct{})
	printed := make(chan struct{})
	go func() {
		defer close(printed)
		for {
			select {
			case <-stop:
				return
			default:
				_ = fmt.Sprint(inst)
			}
		}
	}()
	for range 20 {
		if _, err := inst.Token(context.Background()); err != nil {
			t.Fatalf("Token: %v", err)
		}
		clock.Set(clock.Now().Add(time.Hour)) // past the renewal point: the next call mints
	}
	close(stop)
	<-printed
}
