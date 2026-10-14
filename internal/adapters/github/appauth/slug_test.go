package appauth_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/adapters/github/appauth"
)

func TestSlugReadsGETAppWithTheAppJWT(t *testing.T) {
	t.Parallel()
	clock := &fakeClock{now: t0}
	g := newFakeGitHub(t, func(int, *http.Request) (int, string) {
		return http.StatusOK, `{"id":3792011,"slug":"suitewardq-spike","name":"SuiteWardQ Spike"}`
	})

	slug, err := newApp(t, clock).Slug(context.Background(), g.srv.Client(), g.srv.URL)
	if err != nil {
		t.Fatalf("Slug: %v", err)
	}
	if slug != "suitewardq-spike" {
		t.Errorf("slug = %q, want suitewardq-spike", slug)
	}
	reqs := g.requests()
	if len(reqs) != 1 || reqs[0].method != http.MethodGet || reqs[0].path != "/app" {
		t.Fatalf("requests = %+v, want one GET /app", reqs)
	}
	if got := reqs[0].header.Get("Accept"); got != "application/vnd.github+json" {
		t.Errorf("Accept = %q", got)
	}
	header, claims := verifyJWT(t, bearerJWT(t, reqs[0]), &testKey().PublicKey)
	if !strings.Contains(header, "RS256") || claims["iss"] != "12345" {
		t.Errorf("JWT header %s claims %v, want RS256 issued by the app id", header, claims)
	}
}

func TestSlugUsesTheBaseURLPathPrefix(t *testing.T) {
	t.Parallel()
	g := newFakeGitHub(t, func(int, *http.Request) (int, string) { return http.StatusOK, `{"slug":"a-b"}` })
	if _, err := newApp(t, &fakeClock{now: t0}).Slug(context.Background(), g.srv.Client(), g.srv.URL+"/api/v3/"); err != nil {
		t.Fatalf("Slug: %v", err)
	}
	if reqs := g.requests(); len(reqs) != 1 || reqs[0].path != "/api/v3/app" {
		t.Errorf("requests = %+v, want GET /api/v3/app", reqs)
	}
}

func TestSlugFailuresAreErrorsWithoutTheJWT(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		status     int
		body       string
		wantStatus int // the StatusError status, or 0 when another error is expected
	}{
		{"401", http.StatusUnauthorized, `{"message":"A JSON web token could not be decoded"}`, 401},
		{"404", http.StatusNotFound, `{"message":"Not Found"}`, 404},
		{"201 is not 200", http.StatusCreated, `{"slug":"a-b"}`, 201},
		{"not JSON", http.StatusOK, `<html>`, 0},
		{"no slug", http.StatusOK, `{"id":1}`, 0},
		{"slug that is a path", http.StatusOK, `{"slug":"../users/x"}`, 0},
		{"slug with a bracket", http.StatusOK, `{"slug":"a[bot]"}`, 0},
		{"upper case slug", http.StatusOK, `{"slug":"Spike"}`, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			g := newFakeGitHub(t, func(int, *http.Request) (int, string) { return c.status, c.body })

			_, err := newApp(t, &fakeClock{now: t0}).Slug(context.Background(), g.srv.Client(), g.srv.URL)
			if err == nil {
				t.Fatal("Slug succeeded")
			}
			var status *appauth.StatusError
			if c.wantStatus != 0 && (!errors.As(err, &status) || status.Status != c.wantStatus) {
				t.Errorf("err = %v, want *StatusError with status %d", err, c.wantStatus)
			}
			if c.wantStatus == 0 && errors.As(err, &status) {
				t.Errorf("err = %v, want a malformed-answer error, not a StatusError", err)
			}
			reqs := g.requests()
			if len(reqs) != 1 {
				t.Fatalf("%d requests, want 1", len(reqs))
			}
			if jwt := bearerJWT(t, reqs[0]); strings.Contains(err.Error(), jwt) {
				t.Errorf("error %q holds the JWT", err)
			}
			if strings.Contains(err.Error(), "json web token") {
				t.Errorf("error %q holds answer text", err)
			}
		})
	}
}

func TestSlugRejectsInvalidArguments(t *testing.T) {
	t.Parallel()
	app := newApp(t, &fakeClock{now: t0})
	g := newFakeGitHub(t, func(int, *http.Request) (int, string) { return http.StatusOK, `{"slug":"a-b"}` })
	if _, err := app.Slug(context.Background(), nil, g.srv.URL); err == nil {
		t.Error("nil http client accepted")
	}
	for _, base := range []string{"", "api.github.com", "ftp://x", g.srv.URL + "?x=1", "https://user:pw@example.com"} {
		if _, err := app.Slug(context.Background(), g.srv.Client(), base); err == nil {
			t.Errorf("base URL %q accepted", base)
		}
	}
	if n := len(g.requests()); n != 0 {
		t.Errorf("%d requests, want none", n)
	}
}
