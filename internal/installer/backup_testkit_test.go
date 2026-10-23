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

// assertNoPlaintext fails when any file under dir holds one of the texts: a backup is encrypted at rest, and its spool leaves nothing.
func assertNoPlaintext(t *testing.T, dir string, texts ...string) {
	t.Helper()
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		raw := readFile(t, path)
		for _, text := range texts {
			if strings.Contains(raw, text) {
				t.Errorf("%s holds %q in plaintext", path, text)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
