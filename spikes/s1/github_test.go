package main

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func testClient(t *testing.T, base string, out *bytes.Buffer) *client {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	return &client{
		base: base, appID: "42", installationID: "7", key: key, http: http.DefaultClient,
		log:   &logger{w: out, start: time.Now()},
		etags: map[string]string{}, cache: map[string][]byte{},
	}
}

// The log may carry only whitelisted response headers: no request header, token or extra response header.
func TestHTTPLogKeepsOnlyWhitelistedHeaders(t *testing.T) {
	const secret = "ghs_installation_token_value"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			w.Header().Set("Authorization", "Bearer leaked-in-response")
			_, _ = w.Write([]byte(`{"token":"` + secret + `","expires_at":"2999-01-01T00:00:00Z"}`))
			return
		}
		w.Header().Set("ETag", `"abc"`)
		w.Header().Set("X-RateLimit-Remaining", "4999")
		w.Header().Set("X-GitHub-Request-Id", "REQ1")
		w.Header().Set("Set-Cookie", "session=cookie-value")
		w.Header().Set("Authorization", "Bearer leaked-in-response")
		w.Header().Set("X-Extra", "extra-value")
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	var out bytes.Buffer
	c := testClient(t, srv.URL, &out)
	if _, err := c.get(context.Background(), "/repos/o/r/pulls?state=open"); err != nil {
		t.Fatal(err)
	}

	log := out.String()
	for _, banned := range []string{secret, "Bearer", "leaked-in-response", "cookie-value", "extra-value", "Set-Cookie", "X-Extra", "Authorization", "eyJ"} {
		if strings.Contains(log, banned) {
			t.Errorf("log contains %q:\n%s", banned, log)
		}
	}
	var got struct {
		Event   string            `json:"event"`
		Path    string            `json:"path"`
		Headers map[string]string `json:"headers"`
	}
	lines := strings.Split(strings.TrimSpace(log), "\n")
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"ETag": `"abc"`, "X-RateLimit-Remaining": "4999", "X-GitHub-Request-Id": "REQ1"}
	if got.Headers["Date"] == "" { // the test server adds Date, which is whitelisted
		t.Errorf("Date missing from %v", got.Headers)
	}
	delete(got.Headers, "Date")
	if got.Event != "http" || len(got.Headers) != len(want) {
		t.Fatalf("event %q headers %v, want %v", got.Event, got.Headers, want)
	}
	for k, v := range want {
		if got.Headers[k] != v {
			t.Errorf("header %s = %q, want %q", k, got.Headers[k], v)
		}
	}
}

func TestConditionalGetReusesCachedBodyOn304(t *testing.T) {
	var sent []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/access_tokens") {
			_, _ = w.Write([]byte(`{"token":"t","expires_at":"2999-01-01T00:00:00Z"}`))
			return
		}
		sent = append(sent, r.Header.Get("If-None-Match"))
		if r.Header.Get("If-None-Match") == `"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"v1"`)
		_, _ = w.Write([]byte(`[{"number":1}]`))
	}))
	defer srv.Close()
	var out bytes.Buffer
	c := testClient(t, srv.URL, &out)
	for range 2 {
		b, err := c.get(context.Background(), "/p")
		if err != nil || string(b) != `[{"number":1}]` {
			t.Fatalf("body %q err %v", b, err)
		}
	}
	if len(sent) != 2 || sent[0] != "" || sent[1] != `"v1"` {
		t.Fatalf("If-None-Match sequence %q", sent)
	}
	if c.calls[len(c.calls)-1].Status != http.StatusNotModified {
		t.Fatalf("calls %+v", c.calls)
	}
}

func TestAppJWTVerifies(t *testing.T) {
	c := testClient(t, "", &bytes.Buffer{})
	now := time.Now()
	tok, err := c.appJWT(now)
	if err != nil {
		t.Fatal(err)
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		t.Fatalf("jwt has %d parts", len(parts))
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(&c.key.PublicKey, crypto.SHA256, sum[:], sig); err != nil {
		t.Fatalf("signature: %v", err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
	var claims struct {
		Iss      string `json:"iss"`
		Iat, Exp int64
	}
	if err := json.Unmarshal(raw, &claims); err != nil {
		t.Fatal(err)
	}
	if claims.Iss != "42" || claims.Exp-claims.Iat > 600 || claims.Exp <= now.Unix() {
		t.Fatalf("claims %+v", claims)
	}
}
