// Package appauth authenticates as SuiteWard's GitHub App: it signs the App
// JWT and mints narrowed installation tokens (docs/contracts/github.md, A1).
// It logs nothing and never renders a token value or the private key.
package appauth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const (
	// renewBefore is how long before expires_at a cached token is replaced (D-GITHUB-ACCESS).
	renewBefore = 5 * time.Minute
	// maxAnswer bounds the access_tokens answer read into memory.
	maxAnswer = 1 << 20
)

// StatusError is a non-201 answer to the installation token request.
type StatusError struct{ Status int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("appauth: access_tokens answered %d", e.Status)
}

// ErrPermissionsMismatch reports a granted permission map that differs from the requested one.
var ErrPermissionsMismatch = errors.New("appauth: granted permissions differ from the requested ones")

// App holds the App id and its RSA private key.
type App struct {
	iss   string
	key   *rsa.PrivateKey
	clock func() time.Time
}

// NewApp parses a PKCS#1 or PKCS#8 RSA private key PEM; clock is the only time source.
func NewApp(appID int64, privateKeyPEM []byte, clock func() time.Time) (App, error) {
	if appID <= 0 {
		return App{}, errors.New("appauth: app id must be positive")
	}
	if clock == nil {
		return App{}, errors.New("appauth: clock is required")
	}
	key, err := parseKey(privateKeyPEM)
	if err != nil {
		return App{}, err
	}
	return App{iss: strconv.FormatInt(appID, 10), key: key, clock: clock}, nil
}

// parseKey names the format problem and never echoes the input or a parser message.
func parseKey(privateKeyPEM []byte) (*rsa.PrivateKey, error) {
	block, _ := pem.Decode(privateKeyPEM)
	if block == nil {
		return nil, errors.New("appauth: private key holds no PEM block")
	}
	switch block.Type {
	case "RSA PRIVATE KEY":
		if _, ok := block.Headers["Proc-Type"]; ok {
			return nil, errors.New("appauth: private key is an encrypted PEM; want an unencrypted RSA key")
		}
		key, err := x509.ParsePKCS1PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("appauth: private key is not a valid PKCS#1 RSA key")
		}
		return key, nil
	case "PRIVATE KEY":
		parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
		if err != nil {
			return nil, errors.New("appauth: private key is not a valid PKCS#8 key")
		}
		key, ok := parsed.(*rsa.PrivateKey)
		if !ok {
			return nil, errors.New("appauth: PKCS#8 private key is not an RSA key")
		}
		return key, nil
	case "ENCRYPTED PRIVATE KEY":
		return nil, errors.New("appauth: private key is an encrypted PKCS#8 PEM; want an unencrypted RSA key")
	case "EC PRIVATE KEY":
		return nil, errors.New("appauth: private key is an EC key; want an RSA key")
	default:
		return nil, errors.New("appauth: unsupported PEM block; want RSA PRIVATE KEY (PKCS#1) or PRIVATE KEY (PKCS#8)")
	}
}

// Format renders the App without its key, whatever the verb.
func (a App) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, "appauth.App{iss: "+a.iss+"}") }

// jwt signs the RS256 App JWT: iat = now - 60 s, exp = now + 9 min, iss = the app id.
func (a App) jwt(now time.Time) (string, error) {
	claims, err := json.Marshal(struct {
		IAT int64  `json:"iat"`
		EXP int64  `json:"exp"`
		ISS string `json:"iss"`
	}{now.Add(-60 * time.Second).Unix(), now.Add(9 * time.Minute).Unix(), a.iss})
	if err != nil {
		return "", fmt.Errorf("appauth: encode jwt claims: %w", err)
	}
	enc := base64.RawURLEncoding
	signing := enc.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`)) + "." + enc.EncodeToString(claims)
	sum := sha256.Sum256([]byte(signing))
	sig, err := rsa.SignPKCS1v15(nil, a.key, crypto.SHA256, sum[:])
	if err != nil {
		return "", fmt.Errorf("appauth: sign jwt: %w", err)
	}
	return signing + "." + enc.EncodeToString(sig), nil
}

// Installation mints tokens for one installation, narrowed to repositories and permissions.
type Installation struct {
	app         App
	id          int64
	endpoint    string
	body        []byte
	permissions map[string]string
	http        *http.Client
	// sem serializes minting so concurrent callers mint at most once; it guards cached.
	sem    chan struct{}
	cached Token
}

// Installation returns the token source of installation id. repositories are
// names without the owner; permissions must be non-empty, since an empty map
// would grant everything the App has.
func (a App) Installation(id int64, repositories []string, permissions map[string]string, httpClient *http.Client, baseURL string) (*Installation, error) {
	if id <= 0 {
		return nil, errors.New("appauth: installation id must be positive")
	}
	if len(repositories) == 0 {
		return nil, errors.New("appauth: at least one repository is required")
	}
	seen := make(map[string]bool, len(repositories))
	for _, r := range repositories {
		if r == "" || strings.Contains(r, "/") {
			return nil, fmt.Errorf("appauth: repository %q must be a name without the owner", r)
		}
		// GitHub repository names are case-insensitive.
		key := strings.ToLower(r)
		if seen[key] {
			return nil, fmt.Errorf("appauth: repository %q is listed twice", r)
		}
		seen[key] = true
	}
	if len(permissions) == 0 {
		return nil, errors.New("appauth: permissions are required; an empty map grants everything the App has")
	}
	for name, level := range permissions {
		if name == "" || level == "" {
			return nil, fmt.Errorf("appauth: permission %q has an empty name or level", name)
		}
	}
	if httpClient == nil {
		return nil, errors.New("appauth: http client is required")
	}
	base, err := url.Parse(baseURL)
	if err != nil || (base.Scheme != "https" && base.Scheme != "http") || base.Host == "" {
		return nil, errors.New("appauth: base URL must be an absolute http(s) URL")
	}
	// The endpoint is the base path plus a fixed suffix: a query, fragment or user info would corrupt or leak it.
	if strings.ContainsAny(baseURL, "?#") || base.User != nil {
		return nil, errors.New("appauth: base URL must not carry a query, a fragment or user info")
	}
	perms := maps.Clone(permissions)
	body, err := json.Marshal(struct {
		Repositories []string          `json:"repositories"`
		Permissions  map[string]string `json:"permissions"`
	}{repositories, perms})
	if err != nil {
		return nil, fmt.Errorf("appauth: encode token request: %w", err)
	}
	return &Installation{
		app:         a,
		id:          id,
		endpoint:    strings.TrimSuffix(base.String(), "/") + "/app/installations/" + strconv.FormatInt(id, 10) + "/access_tokens",
		body:        body,
		permissions: perms,
		http:        httpClient,
		sem:         make(chan struct{}, 1),
	}, nil
}

// Format renders the Installation without its token or key, whatever the verb.
// The value receiver covers both Installation and *Installation.
func (i Installation) Format(f fmt.State, _ rune) {
	_, _ = fmt.Fprintf(f, "appauth.Installation{id: %d, permissions: %v}", i.id, i.permissions)
}

// Token returns a valid installation token, minting one when none is cached
// or the cached one expires within five minutes.
func (i *Installation) Token(ctx context.Context) (Token, error) {
	select {
	case i.sem <- struct{}{}:
	case <-ctx.Done():
		return Token{}, ctx.Err()
	}
	defer func() { <-i.sem }()
	// The zero Token expires at the zero time, so an empty cache always mints.
	if i.app.clock().Before(i.cached.expiresAt.Add(-renewBefore)) {
		return i.cached, nil
	}
	tok, err := i.mint(ctx)
	if err != nil {
		return Token{}, err
	}
	i.cached = tok
	return tok, nil
}

// mint requests a new token; only a complete, exactly-narrowed answer is returned.
func (i *Installation) mint(ctx context.Context) (Token, error) {
	now := i.app.clock()
	jwt, err := i.app.jwt(now)
	if err != nil {
		return Token{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, i.endpoint, bytes.NewReader(i.body))
	if err != nil {
		return Token{}, fmt.Errorf("appauth: build token request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+jwt)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("Content-Type", "application/json")
	resp, err := i.http.Do(req)
	if err != nil {
		// The transport error names the URL, never a header.
		return Token{}, fmt.Errorf("appauth: request installation token: %w", err)
	}
	defer func() { _ = resp.Body.Close() }() // the answer is read below; a close error cannot change it
	if resp.StatusCode != http.StatusCreated {
		return Token{}, &StatusError{Status: resp.StatusCode}
	}
	var out struct {
		Token       string            `json:"token"`
		ExpiresAt   time.Time         `json:"expires_at"`
		Permissions map[string]string `json:"permissions"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxAnswer)).Decode(&out); err != nil {
		return Token{}, errors.New("appauth: access_tokens answer is not a token object")
	}
	if out.Token == "" {
		return Token{}, errors.New("appauth: access_tokens answer has no token")
	}
	// A token inside the renewal window would be re-minted on every call.
	if !out.ExpiresAt.After(now.Add(renewBefore)) {
		return Token{}, errors.New("appauth: access_tokens answer expires inside the renewal window")
	}
	if !maps.Equal(out.Permissions, i.permissions) {
		return Token{}, fmt.Errorf("%w: requested %v, granted %v", ErrPermissionsMismatch, i.permissions, out.Permissions)
	}
	sum := sha256.Sum256([]byte(out.Token))
	return Token{value: &out.Token, expiresAt: out.ExpiresAt, identity: hex.EncodeToString(sum[:8])}, nil
}

// Token is an installation token whose value is never rendered.
type Token struct {
	// value is a pointer to a string: fmt prints an unexported field of this
	// type as an address under every verb, where a plain string field would be printed.
	value     *string
	expiresAt time.Time
	identity  string
}

// Reveal returns the bearer value, only for the Authorization header. The
// zero Token, returned beside an error, has none.
func (t Token) Reveal() string {
	if t.value == nil {
		return ""
	}
	return *t.value
}

// Identity is a stable id of this token, derived from but never equal to the value.
func (t Token) Identity() string { return t.identity }

// ExpiresAt is when GitHub expires the token.
func (t Token) ExpiresAt() time.Time { return t.expiresAt }

// String hides the value.
func (t Token) String() string { return "appauth.Token{identity: " + t.identity + "}" }

// GoString hides the value.
func (t Token) GoString() string { return t.String() }

// MarshalText hides the value from JSON and slog encoders.
func (t Token) MarshalText() ([]byte, error) { return []byte(t.String()), nil }

// Format hides the value whatever the verb.
func (t Token) Format(f fmt.State, _ rune) { _, _ = io.WriteString(f, t.String()) }
