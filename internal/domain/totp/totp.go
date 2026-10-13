// Package totp implements RFC 6238 time-based one-time passwords with
// HMAC-SHA-1, 6 digits and a 30 second step, as the owner's approval
// credential (ADR 0027). It only derives and checks codes: storing the secret
// and rejecting a replayed step are the owner store's job.
//
// API:
//   - SecretBytes, Digits and Period fix the parameters: a 20-byte secret,
//     6-digit codes and a 30 s step.
//   - NewSecret reads exactly SecretBytes from a reader (crypto/rand.Reader).
//   - Step maps a time to its step, floor(unix seconds / 30).
//   - Code derives the RFC 4226 HOTP code of a step, zero-padded to 6 digits.
//   - Verify accepts a code of a step within tolerance (0 to 2) of the step of
//     at, compares every candidate in constant time and returns the matching
//     step, the current step first.
//   - URI builds the otpauth:// key URI for an authenticator app.
//
// A secret of the wrong length is ErrInvalidSecret; a malformed or
// non-matching code is ErrInvalidCode. No error text contains a secret or a
// code.
package totp

import (
	"errors"
	"io"
	"time"
)

// SecretBytes is the length of a secret: 160 bits, the HMAC-SHA-1 output size
// recommended by RFC 4226.
const SecretBytes = 20

// Digits is the number of decimal digits in a code.
const Digits = 6

// Period is the length of a time step.
const Period = 30 * time.Second

// ErrInvalidSecret reports a secret that is not SecretBytes long.
var ErrInvalidSecret = errors.New("totp: invalid secret")

// ErrInvalidCode reports a code that is malformed or matches no step in the
// tolerance window.
var ErrInvalidCode = errors.New("totp: invalid code")

var errNotImplemented = errors.New("totp: not implemented")

// NewSecret reads a secret of exactly SecretBytes from r.
func NewSecret(r io.Reader) ([]byte, error) {
	return nil, errNotImplemented
}

// Step returns the time step of t: floor(unix seconds / 30).
func Step(t time.Time) int64 {
	panic("not implemented")
}

// Code returns the 6-digit code of secret at step.
func Code(secret []byte, step int64) (string, error) {
	return "", errNotImplemented
}

// Verify checks code against the steps Step(at)-tolerance to
// Step(at)+tolerance and returns the step it matches.
func Verify(secret []byte, code string, at time.Time, tolerance int) (int64, error) {
	return 0, errNotImplemented
}

// URI returns the otpauth://totp key URI of secret for issuer and account.
func URI(secret []byte, issuer, account string) (string, error) {
	return "", errNotImplemented
}
