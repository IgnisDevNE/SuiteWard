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
//     step, the current step first; steps before the Unix epoch are skipped.
//   - URI builds the otpauth:// key URI for an authenticator app.
//
// A secret of the wrong length is ErrInvalidSecret; a malformed or
// non-matching code (including any time whose window lies before the epoch)
// is ErrInvalidCode; Code of a step before the epoch is ErrInvalidStep. Code
// and Verify return no other error than these sentinels and the tolerance
// range error. No error text contains a secret or a code.
package totp

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/base32"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strings"
	"time"
)

// SecretBytes is the length of a secret: 160 bits, the HMAC-SHA-1 output size
// recommended by RFC 4226.
const SecretBytes = 20

// Digits is the number of decimal digits in a code.
const Digits = 6

// Period is the length of a time step.
const Period = 30 * time.Second

// maxTolerance is the widest window Verify accepts, in steps either side.
const maxTolerance = 2

// ErrInvalidSecret reports a secret that is not SecretBytes long.
var ErrInvalidSecret = errors.New("totp: invalid secret")

// ErrInvalidCode reports a code that is malformed or matches no step in the
// tolerance window.
var ErrInvalidCode = errors.New("totp: invalid code")

// ErrInvalidStep reports a step before the Unix epoch, which has no code.
var ErrInvalidStep = errors.New("totp: invalid step")

// NewSecret reads a secret of exactly SecretBytes from r.
func NewSecret(r io.Reader) ([]byte, error) {
	secret := make([]byte, SecretBytes)
	if _, err := io.ReadFull(r, secret); err != nil {
		return nil, fmt.Errorf("totp: read secret: %w", err)
	}
	return secret, nil
}

// Step returns the time step of t: floor(unix seconds / 30).
func Step(t time.Time) int64 {
	seconds := t.Unix()
	period := int64(Period / time.Second)
	step := seconds / period
	if seconds%period < 0 {
		step--
	}
	return step
}

// Code returns the 6-digit code of secret at step: RFC 4226 HOTP with
// HMAC-SHA-1 and dynamic truncation, with step as the counter.
func Code(secret []byte, step int64) (string, error) {
	if err := checkSecret(secret); err != nil {
		return "", err
	}
	if step < 0 {
		return "", fmt.Errorf("%w: %d is before the epoch", ErrInvalidStep, step)
	}
	var counter [8]byte
	binary.BigEndian.PutUint64(counter[:], uint64(step))
	mac := hmac.New(sha1.New, secret)
	mac.Write(counter[:])
	sum := mac.Sum(nil)
	offset := sum[len(sum)-1] & 0x0f
	value := binary.BigEndian.Uint32(sum[offset:offset+4]) & 0x7fffffff
	return fmt.Sprintf("%0*d", Digits, value%1_000_000), nil
}

// Verify checks code against the steps Step(at)-tolerance to
// Step(at)+tolerance and returns the step it matches, checking the current
// step first, then one step either side, and so on. Every candidate is
// compared in constant time. tolerance must be 0 to 2.
func Verify(secret []byte, code string, at time.Time, tolerance int) (int64, error) {
	if !wellFormed(code) {
		return 0, ErrInvalidCode
	}
	if err := checkSecret(secret); err != nil {
		return 0, err
	}
	if tolerance < 0 || tolerance > maxTolerance {
		return 0, fmt.Errorf("totp: tolerance %d outside 0 to %d", tolerance, maxTolerance)
	}
	current := Step(at)
	matched := false
	var match int64
	for distance := 0; distance <= tolerance; distance++ {
		for _, step := range candidates(current, int64(distance)) {
			if step < 0 {
				continue
			}
			candidate, err := Code(secret, step)
			if err != nil {
				return 0, err
			}
			if subtle.ConstantTimeCompare([]byte(candidate), []byte(code)) == 1 && !matched {
				matched = true
				match = step
			}
		}
	}
	if !matched {
		return 0, ErrInvalidCode
	}
	return match, nil
}

// candidates returns the steps distance away from current, the earlier first.
func candidates(current, distance int64) []int64 {
	if distance == 0 {
		return []int64{current}
	}
	return []int64{current - distance, current + distance}
}

// URI returns the otpauth://totp key URI of secret for issuer and account:
// the label issuer:account and the parameters secret (base32, no padding),
// issuer, algorithm, digits and period, each escaped by net/url.
func URI(secret []byte, issuer, account string) (string, error) {
	if err := checkSecret(secret); err != nil {
		return "", err
	}
	if issuer == "" || account == "" || strings.Contains(issuer, ":") || strings.Contains(account, ":") {
		return "", errors.New("totp: issuer and account must be non-empty and contain no colon")
	}
	encoded := base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret)
	return "otpauth://totp/" + url.PathEscape(issuer) + ":" + url.PathEscape(account) +
		"?secret=" + url.QueryEscape(encoded) +
		"&issuer=" + url.QueryEscape(issuer) +
		fmt.Sprintf("&algorithm=SHA1&digits=%d&period=%d", Digits, int(Period/time.Second)), nil
}

func checkSecret(secret []byte) error {
	if len(secret) != SecretBytes {
		return fmt.Errorf("%w: %d bytes, want %d", ErrInvalidSecret, len(secret), SecretBytes)
	}
	return nil
}

// wellFormed reports whether code is exactly Digits ASCII digits.
func wellFormed(code string) bool {
	if len(code) != Digits {
		return false
	}
	for i := 0; i < len(code); i++ {
		if code[i] < '0' || code[i] > '9' {
			return false
		}
	}
	return true
}
