package installer

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"filippo.io/age"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/archive"
)

// testInstance is the instance id the services of the tests report in /status.
const testInstance = "1a2b3c4d"

var (
	testKeyOnce      sync.Once
	testKeyRecipient string
	testKeyIdentity  string
	testKeyErr       error
)

// testRecoveryKey is one key pair for the whole run: making a post-quantum key costs more than a test should.
func testRecoveryKey(t *testing.T) (recipient, identity string) {
	t.Helper()
	testKeyOnce.Do(func() { testKeyRecipient, testKeyIdentity, testKeyErr = archive.NewKey() })
	if testKeyErr != nil {
		t.Fatal(testKeyErr)
	}
	return testKeyRecipient, testKeyIdentity
}

// writeTestKit writes the recovery kit of the test key into dir and returns its path.
func writeTestKit(t *testing.T, dir string) string {
	t.Helper()
	recipient, identity := testRecoveryKey(t)
	path := filepath.Join(dir, "kit.txt")
	if err := os.WriteFile(path, []byte(archive.RecoveryKit(identity, recipient, testInstance)), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// classicRecipient is an ordinary age key, which backups refuse.
func classicRecipient(t *testing.T) string {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return id.Recipient().String()
}

// noIdentityIn fails when text holds the private key of the test key or the first characters of its secret part.
func noIdentityIn(t *testing.T, what, text string) {
	t.Helper()
	_, identity := testRecoveryKey(t)
	if strings.Contains(text, identity) || strings.Contains(text, "AGE-SECRET-KEY") {
		t.Errorf("the private recovery key reached %s", what)
	}
}
