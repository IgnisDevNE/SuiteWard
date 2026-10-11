// Package appauth authenticates as SuiteWard's GitHub App: it signs the App
// JWT and mints narrowed installation tokens (docs/contracts/github.md, A1).
package appauth

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

var errNotImplemented = errors.New("appauth: not implemented")

// StatusError is a non-201 answer to the installation token request.
type StatusError struct{ Status int }

func (e *StatusError) Error() string {
	return fmt.Sprintf("appauth: access_tokens answered %d", e.Status)
}

// ErrPermissionsMismatch reports a granted permission map that differs from the requested one.
var ErrPermissionsMismatch = errors.New("appauth: granted permissions differ from the requested ones")

// App holds the App id and its RSA private key.
type App struct{}

// NewApp parses a PKCS#1 or PKCS#8 RSA private key PEM; clock is the only time source.
func NewApp(appID int64, privateKeyPEM []byte, clock func() time.Time) (App, error) {
	return App{}, errNotImplemented
}

// Installation mints tokens for one installation, narrowed to repositories and permissions.
type Installation struct{}

// Installation returns the token source of installation id. repositories are
// names without the owner; permissions must be non-empty, since an empty map
// would grant everything the App has.
func (a App) Installation(id int64, repositories []string, permissions map[string]string, httpClient *http.Client, baseURL string) (*Installation, error) {
	return nil, errNotImplemented
}

// Token returns a valid installation token, minting one when none is cached
// or the cached one expires within five minutes.
func (i *Installation) Token(ctx context.Context) (Token, error) {
	return Token{}, errNotImplemented
}

// Token is an installation token whose value is never rendered.
type Token struct{}

// Reveal returns the bearer value, only for the Authorization header.
func (t Token) Reveal() string { panic("not implemented") }

// Identity is a stable id of this token, derived from but never equal to the value.
func (t Token) Identity() string { panic("not implemented") }

// ExpiresAt is when GitHub expires the token.
func (t Token) ExpiresAt() time.Time { panic("not implemented") }

// String hides the value.
func (t Token) String() string { panic("not implemented") }

// GoString hides the value.
func (t Token) GoString() string { panic("not implemented") }
