package archive

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"filippo.io/age"
)

// byteOrderMark starts a text file that a Windows tool saved as UTF-8, and would stick to its first line.
const byteOrderMark = string(rune(0xFEFF))

// NewKey makes the key pair of an instance, of age's hybrid post-quantum type (ML-KEM-768 and X25519): the recipient (age1pq1...)
// goes into the instance's settings, the identity (AGE-SECRET-KEY-PQ-1...) only into the recovery kit that the owner keeps off
// the machine. Never print or log the identity.
func NewKey() (recipient, identity string, err error) {
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		return "", "", fmt.Errorf("generate the recovery key: %w", err)
	}
	recipient = id.Recipient().String()
	return recipient, id.String(), nil
}

// ParseRecipient reads the recipient string of the settings, which must be a post-quantum recipient: a classic age1... key is
// refused. The error never repeats the input, which may be a private key.
func ParseRecipient(s string) (*age.HybridRecipient, error) {
	rec, err := age.ParseHybridRecipient(strings.TrimSpace(strings.TrimPrefix(s, byteOrderMark)))
	if err != nil {
		return nil, errors.New("this is not a recovery public key that SuiteWard accepts: it must start with age1pq1 (a post-quantum key), and an ordinary age1... key is refused")
	}
	return rec, nil
}

// ParseIdentity reads the contents of a recovery kit file: comment lines (#) and blank lines are ignored, and exactly one
// identity must remain, and it must be a post-quantum one: a classic AGE-SECRET-KEY-1... key is refused. The errors never repeat
// any of the input.
func ParseIdentity(kit string) (*age.HybridIdentity, error) {
	var lines []string
	for _, line := range strings.Split(strings.TrimPrefix(kit, byteOrderMark), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			lines = append(lines, line)
		}
	}
	if len(lines) != 1 {
		return nil, fmt.Errorf("the recovery kit must hold exactly one private-key line (it starts with AGE-SECRET-KEY-PQ-1), but it holds %d", len(lines))
	}
	id, err := age.ParseHybridIdentity(lines[0])
	if err != nil {
		return nil, errors.New("the private-key line of the recovery kit is not valid: it must start with AGE-SECRET-KEY-PQ-1 (a post-quantum key), and an ordinary AGE-SECRET-KEY-1... key is refused")
	}
	return id, nil
}

// RecoveryKit is the text of the file the owner keeps off the machine. Every line but the identity is a comment, so the file
// parses with ParseIdentity and with age's own tools. The instance is quoted: a line break in it cannot add a line.
func RecoveryKit(identity, recipient, instance string) string {
	return fmt.Sprintf(`# SuiteWard recovery kit
#
# Instance: %q
# Recovery key (public): %s
# Fingerprint: %s
#
# The last line of this file is the private key that opens the encrypted backups of this SuiteWard instance
# (the files named suiteward-backup-*.tar.age). To restore a backup, run the suiteward restore command
# and give it this file.
#
# Keep this file off the computer, for example in a password manager. Without it no backup can be restored,
# and anyone who has it can read every backup of this instance.
%s
`, instance, recipient, Fingerprint(recipient), identity)
}

// Fingerprint is how the owner recognizes a recipient: SHA256: and the unpadded base64 of the SHA-256 of its text.
func Fingerprint(recipient string) string {
	sum := sha256.Sum256([]byte(recipient))
	return "SHA256:" + base64.RawStdEncoding.EncodeToString(sum[:])
}

// FileName is the name of the archive taken at t, in UTC like the createdAt of its manifest.
func FileName(t time.Time) string {
	return "suiteward-backup-" + t.UTC().Format("20060102-150405") + ".tar.age"
}
