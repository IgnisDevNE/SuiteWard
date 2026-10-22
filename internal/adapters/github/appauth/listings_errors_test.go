package appauth_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/appauth"
)

// Both listings refuse what they cannot send before any request leaves, and name the argument that is wrong.
func TestListingsRefuseWhatTheyCannotSend(t *testing.T) {
	t.Parallel()
	app := newApp(t, &fakeClock{now: t0})
	g := newFakeGitHub(t, func(int, *http.Request) (int, string) { return http.StatusOK, "[]" })
	ctx := context.Background()

	if _, err := app.Installations(ctx, nil, g.srv.URL); err == nil || !strings.Contains(err.Error(), "http client is required") {
		t.Errorf("Installations without a client = %v", err)
	}
	if _, err := app.Installations(ctx, g.srv.Client(), "ftp://example.org"); err == nil || !strings.Contains(err.Error(), "base URL") {
		t.Errorf("Installations with another scheme = %v", err)
	}
	if _, err := app.InstallationRepositories(ctx, 0, g.srv.Client(), g.srv.URL); err == nil || !strings.Contains(err.Error(), "installation id must be positive") {
		t.Errorf("InstallationRepositories without an id = %v", err)
	}
	if _, err := app.InstallationRepositories(ctx, installationID, nil, g.srv.URL); err == nil || !strings.Contains(err.Error(), "http client is required") {
		t.Errorf("InstallationRepositories without a client = %v", err)
	}
	if _, err := app.InstallationRepositories(ctx, installationID, g.srv.Client(), "https://user:pw@example.org"); err == nil || !strings.Contains(err.Error(), "base URL") {
		t.Errorf("InstallationRepositories with user info in the base = %v", err)
	}
	if n := len(g.requests()); n != 0 {
		t.Errorf("%d requests were sent for arguments that were refused", n)
	}
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

// A listing that cannot reach GitHub says which endpoint, and carries neither the JWT nor the installation token.
func TestListingsReportATransportFailureWithoutACredential(t *testing.T) {
	t.Parallel()
	app := newApp(t, &fakeClock{now: t0})
	g := newFakeGitHub(t, func(n int, r *http.Request) (int, string) {
		if r.Method == http.MethodPost {
			return http.StatusCreated, tokenBody(tokenValue(n), t0.Add(time.Hour), map[string]string{"metadata": "read"})
		}
		return http.StatusOK, "[]"
	})
	refusing := func(inner http.RoundTripper) *http.Client {
		return &http.Client{Transport: roundTripperFunc(func(r *http.Request) (*http.Response, error) {
			if r.Method == http.MethodGet {
				return nil, errors.New("connection reset")
			}
			return inner.RoundTrip(r)
		})}
	}
	client := refusing(g.srv.Client().Transport)

	_, err := app.Installations(context.Background(), client, g.srv.URL)
	if err == nil || !strings.Contains(err.Error(), "request installations") || !strings.Contains(err.Error(), "connection reset") {
		t.Errorf("Installations = %v", err)
	}
	_, err = app.InstallationRepositories(context.Background(), installationID, client, g.srv.URL)
	if err == nil || !strings.Contains(err.Error(), "request installation/repositories") {
		t.Fatalf("InstallationRepositories = %v", err)
	}
	for _, r := range g.requests() {
		if auth := r.header.Get("Authorization"); auth != "" && strings.Contains(err.Error(), strings.TrimPrefix(auth, "Bearer ")) {
			t.Errorf("the error holds a credential: %v", err)
		}
	}
}

func TestInstallationRepositoriesRefusesAnEntryWithoutAName(t *testing.T) {
	t.Parallel()
	g := repositoriesFake(t, &queryLog{}, func(string) (int, string) {
		return http.StatusOK, `{"total_count":1,"repositories":[{"full_name":""}]}`
	})
	_, err := newApp(t, &fakeClock{now: t0}).InstallationRepositories(context.Background(), installationID, g.srv.Client(), g.srv.URL)
	if !errors.Is(err, appauth.ErrMalformedAnswer) {
		t.Fatalf("InstallationRepositories = %v, want ErrMalformedAnswer", err)
	}
}
