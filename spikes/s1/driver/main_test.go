package main

import (
	"bytes"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const fakeToken = "ghs_installation_token_value"

func writeKey(t *testing.T) string {
	t.Helper()
	k, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(t.TempDir(), "key.pem")
	if err := os.WriteFile(p, pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(k)}), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func env(base, keyFile, repo string) func(string) string {
	m := map[string]string{"S1_APP_ID": "42", "S1_INSTALLATION_ID": "7", "S1_REPO": repo, "S1_KEY_FILE": keyFile, "S1_API_BASE": base}
	return func(k string) string { return m[k] }
}

func TestRefusesAnyOtherRepositoryBeforeTouchingTheKey(t *testing.T) {
	var out bytes.Buffer
	err := run([]string{"seed"}, env("http://127.0.0.1:1", "/no/such/key", "IgnisDevNE/SuiteWard"), &out)
	if err == nil || !strings.Contains(err.Error(), "only writes to IgnisDevNE/SuiteWardQ") || out.Len() != 0 {
		t.Fatalf("err %v, output %q", err, out.String())
	}
}

var ts = regexp.MustCompile(`^\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d\.\d{9}Z$`)

// Every subcommand sends the expected request, prints one JSON line with both timestamps and never the token.
func TestSubcommands(t *testing.T) {
	var reqs []string
	var tokenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		key := r.Method + " " + r.URL.Path
		reqs = append(reqs, key+" "+string(b))
		const repo = "/repos/IgnisDevNE/SuiteWardQ"
		switch {
		case strings.HasSuffix(key, "/access_tokens"):
			tokenBody = string(b)
			_, _ = w.Write([]byte(`{"token":"` + fakeToken + `"}`))
		case key == "GET "+repo+"/contents/README.md":
			_, _ = w.Write([]byte(`{}`))
		case key == "GET "+repo+"/contents/tests/push-1.txt":
			_, _ = w.Write([]byte(`{"sha":"blob1"}`))
		case key == "DELETE "+repo+"/contents/tests/push-1.txt":
			_, _ = w.Write([]byte(`{"commit":{"sha":"c3"}}`))
		case key == "GET "+repo+"/pulls/9/commits":
			_, _ = w.Write([]byte(`[{"sha":"c1"},{"sha":"c2"}]`))
		case key == "GET "+repo+"/commits/c2":
			_, _ = w.Write([]byte(`{"parents":[{"sha":"c1"}],"files":[{"filename":"other/x.txt","status":"added"}]}`))
		case key == "GET "+repo+"/commits/c1":
			_, _ = w.Write([]byte(`{"parents":[{"sha":"base0"}],"files":[{"filename":"other/y.txt","status":"added"},{"filename":"tests/push-1.txt","status":"added"}]}`))
		case strings.HasPrefix(key, "GET "+repo+"/contents/"):
			http.Error(w, `{"message":"Not Found"}`, http.StatusNotFound)
		case key == "GET "+repo+"/git/ref/heads/main":
			_, _ = w.Write([]byte(`{"object":{"sha":"base0"}}`))
		case key == "POST "+repo+"/git/refs":
			w.WriteHeader(http.StatusCreated)
			_, _ = w.Write([]byte(`{}`))
		case strings.HasPrefix(key, "PUT "+repo+"/contents/"):
			_, _ = w.Write([]byte(`{"commit":{"sha":"c1"}}`))
		case key == "POST "+repo+"/pulls":
			_, _ = w.Write([]byte(`{"number":9,"head":{"sha":"c1"}}`))
		case key == "GET "+repo+"/pulls/9":
			_, _ = w.Write([]byte(`{"head":{"ref":"feat"}}`))
		case key == "POST "+repo+"/issues/9/comments":
			_, _ = w.Write([]byte(`{"id":77,"created_at":"2026-01-01T00:00:00Z"}`))
		case key == "PATCH "+repo+"/issues/comments/77":
			_, _ = w.Write([]byte(`{"id":77,"updated_at":"2026-01-02T00:00:00Z"}`))
		case key == "DELETE "+repo+"/issues/comments/77":
			w.WriteHeader(http.StatusNoContent)
		case key == "PUT "+repo+"/pulls/9/merge":
			_, _ = w.Write([]byte(`{"sha":"m1","merged":true}`))
		case key == "PATCH "+repo+"/pulls/9":
			_, _ = w.Write([]byte(`{"state":"closed"}`))
		default:
			t.Errorf("unexpected request %s", key)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	keyFile := writeKey(t)
	keyPEM, _ := os.ReadFile(keyFile)

	cases := []struct {
		args    []string
		last    string // the last request, whose timestamps are printed
		wantOut map[string]any
	}{
		{[]string{"seed"}, "PUT /repos/IgnisDevNE/SuiteWardQ/contents/tests/a.txt", map[string]any{"created": []any{"tests/a.txt"}, "present": []any{"README.md"}}},
		{[]string{"open", "feat"}, "POST /repos/IgnisDevNE/SuiteWardQ/pulls", map[string]any{"pr": 9.0, "head_sha": "c1", "base_sha": "base0"}},
		{[]string{"push", "9"}, "PUT /repos/IgnisDevNE/SuiteWardQ/contents/tests/push-", map[string]any{"head_sha": "c1", "outside": false}},
		{[]string{"push", "9", "-outside"}, "PUT /repos/IgnisDevNE/SuiteWardQ/contents/other/push-", map[string]any{"head_sha": "c1", "outside": true}},
		{[]string{"comment", "9", "/suiteward", "approve", "abc"}, `POST /repos/IgnisDevNE/SuiteWardQ/issues/9/comments {"body":"/suiteward approve abc"}`, map[string]any{"comment_id": 77.0}},
		{[]string{"edit", "9", "77", "changed"}, `PATCH /repos/IgnisDevNE/SuiteWardQ/issues/comments/77 {"body":"changed"}`, map[string]any{"updated_at": "2026-01-02T00:00:00Z"}},
		{[]string{"delete", "9", "77"}, "DELETE /repos/IgnisDevNE/SuiteWardQ/issues/comments/77", map[string]any{"comment_id": 77.0}},
		{[]string{"merge", "9", "squash"}, `PUT /repos/IgnisDevNE/SuiteWardQ/pulls/9/merge {"merge_method":"squash"}`, map[string]any{"merge_commit_sha": "m1", "requested_method": "squash", "merged": true}},
		{[]string{"comment", "9", "keep", "-outside"}, `POST /repos/IgnisDevNE/SuiteWardQ/issues/9/comments {"body":"keep -outside"}`, map[string]any{"comment_id": 77.0}},
		{[]string{"revert", "9"}, "DELETE /repos/IgnisDevNE/SuiteWardQ/contents/tests/push-1.txt", map[string]any{"pr": 9.0, "action": "delete", "path": "tests/push-1.txt", "head_sha": "c3"}},
		{[]string{"close", "9"}, `PATCH /repos/IgnisDevNE/SuiteWardQ/pulls/9 {"state":"closed"}`, map[string]any{"pr": 9.0}},
	}
	for _, tc := range cases {
		t.Run(strings.Join(tc.args, " "), func(t *testing.T) {
			reqs = nil
			var out bytes.Buffer
			if err := run(tc.args, env(srv.URL, keyFile, onlyRepo), &out); err != nil {
				t.Fatal(err)
			}
			if got := reqs[len(reqs)-1]; !strings.HasPrefix(got, tc.last) {
				t.Errorf("last request %q, want prefix %q", got, tc.last)
			}
			if strings.Contains(out.String(), fakeToken) || strings.Contains(out.String(), "PRIVATE KEY") || strings.Contains(out.String(), string(keyPEM)) {
				t.Errorf("output leaks a secret: %s", out.String())
			}
			if strings.Count(out.String(), "\n") != 1 {
				t.Fatalf("want one line, got %q", out.String())
			}
			var got map[string]any
			if err := json.Unmarshal(out.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			for _, k := range []string{"t_before", "t_after"} {
				if s, _ := got[k].(string); !ts.MatchString(s) {
					t.Errorf("%s = %v, want nine-digit UTC timestamp", k, got[k])
				}
			}
			if got["t_before"].(string) > got["t_after"].(string) || got["cmd"] != tc.args[0] || got["status"] == nil {
				t.Errorf("fields %v", got)
			}
			for k, want := range tc.wantOut {
				if g, _ := json.Marshal(got[k]); string(g) != mustJSON(want) {
					t.Errorf("%s = %s, want %s", k, g, mustJSON(want))
				}
			}
		})
	}
	if tokenBody != `{"permissions":{"contents":"write","issues":"write","pull_requests":"write"}}` {
		t.Errorf("token request asked for %s", tokenBody)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestBadArgumentsRequestNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
	}))
	defer srv.Close()
	keyFile := writeKey(t)
	for _, args := range [][]string{{}, {"nope"}, {"open"}, {"open", "../x"}, {"push", "x"}, {"merge", "9", "fast"}, {"edit", "9", "77"}, {"comment", "9"}, {"seed", "extra"}, {"close", "9", "-outside"}, {"revert"}, {"revert", "9", "x"}} {
		if err := run(args, env(srv.URL, keyFile, onlyRepo), io.Discard); err == nil {
			t.Errorf("%v: no error", args)
		}
	}
}

// A PR whose last tests/ change modified a file is reverted by writing the base content back over the head's blob.
func TestRevertRestoresAModifiedFile(t *testing.T) {
	var put string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		const repo = "/repos/IgnisDevNE/SuiteWardQ"
		switch key := r.Method + " " + r.URL.Path + "?" + r.URL.RawQuery; key {
		case "POST /app/installations/7/access_tokens?":
			_, _ = w.Write([]byte(`{"token":"` + fakeToken + `"}`))
		case "GET " + repo + "/pulls/10?":
			_, _ = w.Write([]byte(`{"head":{"ref":"feat10"}}`))
		case "GET " + repo + "/pulls/10/commits?per_page=100":
			_, _ = w.Write([]byte(`[{"sha":"d1"}]`))
		case "GET " + repo + "/commits/d1?":
			_, _ = w.Write([]byte(`{"parents":[{"sha":"base0"}],"files":[{"filename":"tests/a.txt","status":"modified"}]}`))
		case "GET " + repo + "/contents/tests/a.txt?ref=feat10":
			_, _ = w.Write([]byte(`{"sha":"blobH","content":"bmV3Cg=="}`))
		case "GET " + repo + "/contents/tests/a.txt?ref=base0":
			_, _ = w.Write([]byte(`{"sha":"blobB","content":"YQo=\n"}`))
		case "PUT " + repo + "/contents/tests/a.txt?":
			put = string(b)
			_, _ = w.Write([]byte(`{"commit":{"sha":"d2"}}`))
		default:
			t.Errorf("unexpected request %s", key)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	var out bytes.Buffer
	if err := run([]string{"revert", "10"}, env(srv.URL, writeKey(t), onlyRepo), &out); err != nil {
		t.Fatal(err)
	}
	var body map[string]string
	if err := json.Unmarshal([]byte(put), &body); err != nil || body["content"] != "YQo=" || body["sha"] != "blobH" || body["branch"] != "feat10" {
		t.Errorf("put body %s (%v)", put, err)
	}
	if !strings.Contains(out.String(), `"action":"restore"`) || !strings.Contains(out.String(), `"head_sha":"d2"`) {
		t.Errorf("output %s", out.String())
	}
}

func TestErrorMessagesAreTruncated(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			_, _ = w.Write([]byte(`{"token":"` + fakeToken + `"}`))
			return
		}
		w.WriteHeader(http.StatusUnprocessableEntity)
		_, _ = w.Write([]byte(`{"message":"` + strings.Repeat("x", 300) + `"}`))
	}))
	defer srv.Close()
	err := run([]string{"comment", "9", "hi"}, env(srv.URL, writeKey(t), onlyRepo), io.Discard)
	if err == nil || strings.Count(err.Error(), "x") != 200 {
		t.Fatalf("error %v, want the message cut to 200 characters", err)
	}
}
