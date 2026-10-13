package totp_test

import (
	"bytes"
	"encoding/base32"
	"encoding/hex"
	"errors"
	"io"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/totp"
)

// rfcSecret is the public SHA-1 test secret of RFC 6238 Appendix B.
var rfcSecret = []byte("12345678901234567890")

// rfcStep is the step of 1111111111, whose RFC 6238 code is 050471.
const (
	rfcStep = int64(37037037)
	rfcCode = "050471"
)

func TestCodeMatchesRFC6238Vectors(t *testing.T) {
	// The last 6 digits of the published 8-digit SHA-1 values; step is the
	// unix time divided by 30.
	tests := []struct {
		name string
		unix int64
		step int64
		code string
	}{
		{name: "59", unix: 59, step: 1, code: "287082"},
		{name: "1111111109", unix: 1111111109, step: 37037036, code: "081804"},
		{name: "1111111111", unix: 1111111111, step: 37037037, code: "050471"},
		{name: "1234567890", unix: 1234567890, step: 41152263, code: "005924"},
		{name: "2000000000", unix: 2000000000, step: 66666666, code: "279037"},
		{name: "20000000000", unix: 20000000000, step: 666666666, code: "353130"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := totp.Code(rfcSecret, tt.step)
			if err != nil {
				t.Fatalf("Code(step %d) error = %v", tt.step, err)
			}
			if got != tt.code {
				t.Fatalf("Code(step %d) = %q, want %q", tt.step, got, tt.code)
			}
			step, err := totp.Verify(rfcSecret, tt.code, time.Unix(tt.unix, 0), 0)
			if err != nil {
				t.Fatalf("Verify(%q at %d) error = %v", tt.code, tt.unix, err)
			}
			if step != tt.step {
				t.Fatalf("Verify(%q at %d) step = %d, want %d", tt.code, tt.unix, step, tt.step)
			}
		})
	}
}

func TestCodeRejectsNegativeStep(t *testing.T) {
	if _, err := totp.Code(rfcSecret, 0); err != nil {
		t.Fatalf("Code(step 0) error = %v", err)
	}
	if code, err := totp.Code(rfcSecret, -1); err == nil {
		t.Fatalf("Code(step -1) = %q, want an error", code)
	}
}

func TestVerifyToleranceWindow(t *testing.T) {
	// rfcCode belongs to rfcStep; each at puts it at a different distance from
	// the current step.
	const (
		atCurrent   = 1111111111 // step 37037037
		atNext      = 1111111140 // step 37037038: rfcCode is the previous step
		atPrevious  = 1111111109 // step 37037036: rfcCode is the next step
		atTwoAfter  = 1111111170 // step 37037039: rfcCode is two steps behind
		atTwoBefore = 1111111050 // step 37037035: rfcCode is two steps ahead
	)
	tests := []struct {
		name      string
		code      string
		at        int64
		tolerance int
		wantStep  int64
		wantErr   error
	}{
		{name: "current step at tolerance 0", code: rfcCode, at: atCurrent, tolerance: 0, wantStep: rfcStep},
		{name: "previous step at tolerance 0", code: rfcCode, at: atNext, tolerance: 0, wantErr: totp.ErrInvalidCode},
		{name: "next step at tolerance 0", code: rfcCode, at: atPrevious, tolerance: 0, wantErr: totp.ErrInvalidCode},
		{name: "current step at tolerance 1", code: rfcCode, at: atCurrent, tolerance: 1, wantStep: rfcStep},
		{name: "previous step at tolerance 1", code: rfcCode, at: atNext, tolerance: 1, wantStep: rfcStep},
		{name: "next step at tolerance 1", code: rfcCode, at: atPrevious, tolerance: 1, wantStep: rfcStep},
		{name: "two steps behind at tolerance 1", code: rfcCode, at: atTwoAfter, tolerance: 1, wantErr: totp.ErrInvalidCode},
		{name: "two steps ahead at tolerance 1", code: rfcCode, at: atTwoBefore, tolerance: 1, wantErr: totp.ErrInvalidCode},
		{name: "two steps behind at tolerance 2", code: rfcCode, at: atTwoAfter, tolerance: 2, wantStep: rfcStep},
		{name: "two steps ahead at tolerance 2", code: rfcCode, at: atTwoBefore, tolerance: 2, wantStep: rfcStep},
		{name: "returns the matching step, not the current one", code: "081804", at: atCurrent, tolerance: 1, wantStep: 37037036},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			step, err := totp.Verify(rfcSecret, tt.code, time.Unix(tt.at, 0), tt.tolerance)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Verify error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Verify error = %v", err)
			}
			if step != tt.wantStep {
				t.Fatalf("Verify step = %d, want %d", step, tt.wantStep)
			}
		})
	}
}

func TestVerifyRejectsToleranceOutOfRange(t *testing.T) {
	at := time.Unix(1111111111, 0)
	if _, err := totp.Verify(rfcSecret, rfcCode, at, 2); err != nil {
		t.Fatalf("Verify(tolerance 2) error = %v", err)
	}
	for _, tolerance := range []int{-1, 3} {
		if step, err := totp.Verify(rfcSecret, rfcCode, at, tolerance); err == nil {
			t.Fatalf("Verify(tolerance %d) = %d, want an error", tolerance, step)
		}
	}
}

func TestVerifyRejectsMalformedCodes(t *testing.T) {
	tests := []struct {
		name string
		code string
	}{
		{name: "five digits", code: "12345"},
		{name: "seven digits", code: "1234567"},
		{name: "letter", code: "12a456"},
		{name: "leading space", code: " 123456"},
		{name: "fullwidth digit", code: "12345６"},
		{name: "empty", code: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := totp.Verify(rfcSecret, tt.code, time.Unix(1111111111, 0), 1)
			if !errors.Is(err, totp.ErrInvalidCode) {
				t.Fatalf("Verify(%q) error = %v, want ErrInvalidCode", tt.code, err)
			}
		})
	}
}

func TestWrongLengthSecretIsInvalid(t *testing.T) {
	tests := []struct {
		name   string
		secret []byte
	}{
		{name: "nil", secret: nil},
		{name: "empty", secret: []byte{}},
		{name: "one byte short", secret: rfcSecret[:totp.SecretBytes-1]},
		{name: "one byte long", secret: append(bytes.Clone(rfcSecret), '1')},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := totp.Code(tt.secret, rfcStep); !errors.Is(err, totp.ErrInvalidSecret) {
				t.Fatalf("Code error = %v, want ErrInvalidSecret", err)
			}
			if _, err := totp.Verify(tt.secret, rfcCode, time.Unix(1111111111, 0), 1); !errors.Is(err, totp.ErrInvalidSecret) {
				t.Fatalf("Verify error = %v, want ErrInvalidSecret", err)
			}
			if _, err := totp.URI(tt.secret, "SuiteWard", "owner"); !errors.Is(err, totp.ErrInvalidSecret) {
				t.Fatalf("URI error = %v, want ErrInvalidSecret", err)
			}
		})
	}
}

func TestNewSecret(t *testing.T) {
	t.Run("reads exactly SecretBytes", func(t *testing.T) {
		want := bytes.Repeat([]byte{0xa5}, totp.SecretBytes)
		r := bytes.NewReader(bytes.Repeat([]byte{0xa5}, 2*totp.SecretBytes))
		got, err := totp.NewSecret(r)
		if err != nil {
			t.Fatalf("NewSecret error = %v", err)
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("NewSecret = %x, want %x", got, want)
		}
		if r.Len() != totp.SecretBytes {
			t.Fatalf("NewSecret left %d bytes unread, want %d", r.Len(), totp.SecretBytes)
		}
	})
	t.Run("short reader", func(t *testing.T) {
		_, err := totp.NewSecret(bytes.NewReader(make([]byte, totp.SecretBytes-1)))
		if !errors.Is(err, io.ErrUnexpectedEOF) {
			t.Fatalf("NewSecret error = %v, want io.ErrUnexpectedEOF", err)
		}
	})
	t.Run("empty reader", func(t *testing.T) {
		_, err := totp.NewSecret(bytes.NewReader(nil))
		if !errors.Is(err, io.EOF) {
			t.Fatalf("NewSecret error = %v, want io.EOF", err)
		}
	})
	t.Run("wraps the reader error", func(t *testing.T) {
		errBroken := errors.New("broken source")
		r := io.MultiReader(bytes.NewReader(make([]byte, 10)), iotest.ErrReader(errBroken))
		_, err := totp.NewSecret(r)
		if !errors.Is(err, errBroken) {
			t.Fatalf("NewSecret error = %v, want it to wrap %v", err, errBroken)
		}
	})
}

func TestURI(t *testing.T) {
	tests := []struct {
		name    string
		issuer  string
		account string
		want    string
	}{
		{
			name:    "fixed secret",
			issuer:  "SuiteWard",
			account: "owner",
			want:    "otpauth://totp/SuiteWard:owner?secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ&issuer=SuiteWard&algorithm=SHA1&digits=6&period=30",
		},
		{
			// net/url path escaping: a space becomes %20 and @ stays literal,
			// as RFC 3986 allows in a path segment.
			name:    "space and @ in the account",
			issuer:  "SuiteWard",
			account: "alice smith@example.com",
			want:    "otpauth://totp/SuiteWard:alice%20smith@example.com?secret=GEZDGNBVGY3TQOJQGEZDGNBVGY3TQOJQ&issuer=SuiteWard&algorithm=SHA1&digits=6&period=30",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := totp.URI(rfcSecret, tt.issuer, tt.account)
			if err != nil {
				t.Fatalf("URI error = %v", err)
			}
			if got != tt.want {
				t.Fatalf("URI =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestURIRejectsInvalidLabel(t *testing.T) {
	if _, err := totp.URI(rfcSecret, "SuiteWard", "owner"); err != nil {
		t.Fatalf("URI(valid label) error = %v", err)
	}
	tests := []struct {
		name    string
		issuer  string
		account string
	}{
		{name: "empty issuer", issuer: "", account: "owner"},
		{name: "empty account", issuer: "SuiteWard", account: ""},
		{name: "colon in issuer", issuer: "Suite:Ward", account: "owner"},
		{name: "colon in account", issuer: "SuiteWard", account: "own:er"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got, err := totp.URI(rfcSecret, tt.issuer, tt.account); err == nil {
				t.Fatalf("URI = %q, want an error", got)
			}
		})
	}
}

func TestErrorsDoNotRevealSecretOrCode(t *testing.T) {
	shortSecret := []byte("1234567890123456789")
	secretForms := func(secret []byte) []string {
		return []string{
			string(secret),
			hex.EncodeToString(secret),
			base32.StdEncoding.WithPadding(base32.NoPadding).EncodeToString(secret),
		}
	}
	at := time.Unix(1111111111, 0)

	_, codeErr := totp.Code(shortSecret, rfcStep)
	_, verifySecretErr := totp.Verify(shortSecret, rfcCode, at, 1)
	_, uriErr := totp.URI(shortSecret, "SuiteWard", "owner")
	_, noMatchErr := totp.Verify(rfcSecret, rfcCode, time.Unix(1111111170, 0), 1)
	_, malformedErr := totp.Verify(rfcSecret, "12a456", at, 1)

	tests := []struct {
		name     string
		err      error
		sentinel error
		hidden   []string
	}{
		{name: "Code with a wrong-length secret", err: codeErr, sentinel: totp.ErrInvalidSecret, hidden: secretForms(shortSecret)},
		{name: "Verify with a wrong-length secret", err: verifySecretErr, sentinel: totp.ErrInvalidSecret, hidden: append(secretForms(shortSecret), rfcCode)},
		{name: "URI with a wrong-length secret", err: uriErr, sentinel: totp.ErrInvalidSecret, hidden: secretForms(shortSecret)},
		{name: "Verify with no matching step", err: noMatchErr, sentinel: totp.ErrInvalidCode, hidden: append(secretForms(rfcSecret), rfcCode)},
		{name: "Verify with a malformed code", err: malformedErr, sentinel: totp.ErrInvalidCode, hidden: append(secretForms(rfcSecret), "12a456")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !errors.Is(tt.err, tt.sentinel) {
				t.Fatalf("error = %v, want %v", tt.err, tt.sentinel)
			}
			for _, hidden := range tt.hidden {
				if strings.Contains(tt.err.Error(), hidden) {
					t.Fatalf("error %q reveals %q", tt.err, hidden)
				}
			}
		})
	}
}

// TestStep stays last: the RED stub of Step panics, which ends the test binary.
func TestStep(t *testing.T) {
	tests := []struct {
		name string
		at   time.Time
		want int64
	}{
		{name: "epoch", at: time.Unix(0, 0), want: 0},
		{name: "29.999 s", at: time.Unix(29, 999_000_000), want: 0},
		{name: "30 s", at: time.Unix(30, 0), want: 1},
		{name: "59.999 s", at: time.Unix(59, 999_000_000), want: 1},
		{name: "60 s", at: time.Unix(60, 0), want: 2},
		{name: "RFC time 1111111111", at: time.Unix(1111111111, 0), want: rfcStep},
		{name: "before the epoch floors", at: time.Unix(-1, 0), want: -1},
		{name: "time zone does not matter", at: time.Unix(30, 0).In(time.FixedZone("UTC+5", 5*3600)), want: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := totp.Step(tt.at); got != tt.want {
				t.Fatalf("Step(%v) = %d, want %d", tt.at, got, tt.want)
			}
		})
	}
}
