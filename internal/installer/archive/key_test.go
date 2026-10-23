package archive

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

func TestNewKeyReturnsAMatchingPostQuantumPairInTheAgeTextForms(t *testing.T) {
	recipient, identity, err := NewKey()
	if err != nil {
		t.Fatalf("NewKey: %v", err)
	}
	if !strings.HasPrefix(recipient, "age1pq1") || !strings.HasPrefix(identity, "AGE-SECRET-KEY-PQ-1") {
		t.Fatalf("not post-quantum age keys: a recipient of %d characters starting %.8q, and an identity of %d characters", len(recipient), recipient, len(identity))
	}
	rec, err := ParseRecipient(recipient)
	if err != nil {
		t.Fatalf("ParseRecipient: %v", err)
	}
	id, err := ParseIdentity(identity)
	if err != nil {
		t.Fatalf("ParseIdentity: %v", err)
	}
	if id.Recipient().String() != recipient || rec.String() != recipient {
		t.Error("the identity is not the private half of the recipient")
	}
	other, _, err := NewKey()
	if err != nil || other == recipient {
		t.Errorf("a second key pair equals the first: %v", err)
	}
}

func TestRecoveryKitParsesBackToTheIdentityAndExplainsItself(t *testing.T) {
	recipient, identity, err := NewKey()
	if err != nil {
		t.Fatal(err)
	}
	kit := RecoveryKit(identity, recipient, "acme\nAGE-SECRET-KEY-PQ-1INJECTED")

	id, err := ParseIdentity(kit)
	if err != nil {
		t.Fatalf("the kit does not parse: %v", err)
	}
	if id.String() != identity {
		t.Error("the kit parses to another identity")
	}
	for _, want := range []string{recipient, Fingerprint(recipient), `"acme\nAGE-SECRET-KEY-PQ-1INJECTED"`, "off this machine", "no backup can be restored"} {
		if !strings.Contains(kit, want) {
			t.Errorf("the kit does not say %q:\n%s", want, kit)
		}
	}
	for _, line := range strings.Split(kit, "\n") {
		if line != "" && !strings.HasPrefix(line, "#") && line != identity {
			t.Errorf("line %q is neither a comment nor the identity", line)
		}
	}
	// A kit of a backup made for this recipient opens it.
	rec, err := ParseRecipient(recipient)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Open(bytes.NewReader(writeArchive(t, rec, testInfo(), nil)), id); err != nil {
		t.Errorf("the key of the kit does not open a backup made for its recipient: %v", err)
	}
}

func TestParseIdentityTakesAKitFileAndRefusesAnythingElse(t *testing.T) {
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	secret := id.String()
	for name, kit := range map[string]string{
		"bare":                secret,
		"comments and blanks": "# created by age-keygen\n\n  # indented comment\n" + secret + "\n\n# trailing\n",
		"carriage returns":    "# kit\r\n" + secret + "\r\n",
		"surrounding spaces":  "  " + secret + "  \n",
	} {
		got, err := ParseIdentity(kit)
		if err != nil || got.String() != secret {
			t.Errorf("%s: %v", name, err)
		}
	}

	other, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	garbage := "AGE-SECRET-KEY-PQ-1" + strings.Repeat("Q", 20)
	for name, kit := range map[string]string{
		"nothing":              "",
		"only comments":        "# one\n# two\n",
		"garbage":              garbage,
		"two identities":       secret + "\n" + other.String() + "\n",
		"a recipient":          id.Recipient().String(),
		"an identity and junk": secret + "\nnot a key\n",
	} {
		_, err := ParseIdentity(kit)
		if err == nil {
			t.Errorf("%s: parsed", name)
			continue
		}
		// No part of the secret goes into an error.
		for _, leak := range []string{garbage, secret, other.String(), "QQQQQQ"} {
			if strings.Contains(err.Error(), leak) {
				t.Errorf("%s: the error repeats %q", name, leak)
			}
		}
		if !strings.Contains(err.Error(), "recovery kit") {
			t.Errorf("%s: the error does not name the recovery kit: %v", name, err)
		}
	}
}

func TestAClassicKeyIsRefusedBecauseBackupsUsePostQuantumKeys(t *testing.T) {
	classic, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	secret, public := classic.String(), classic.Recipient().String()
	if !strings.HasPrefix(secret, "AGE-SECRET-KEY-1") || !strings.HasPrefix(public, "age1") || strings.HasPrefix(public, "age1pq1") {
		t.Fatalf("the control is not a classic key pair: %.8q", public)
	}

	if _, err := ParseRecipient(public); err == nil || !strings.Contains(err.Error(), "post-quantum") {
		t.Errorf("ParseRecipient of a classic recipient = %v, want a refusal that says backups use post-quantum keys", err)
	}
	_, err = ParseIdentity("# a kit made by age-keygen\n" + secret + "\n")
	if err == nil || !strings.Contains(err.Error(), "post-quantum") {
		t.Fatalf("ParseIdentity of a classic identity = %v, want a refusal that says backups use post-quantum keys", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Error("the refusal repeats the private key")
	}
}

func TestParseRecipientRefusesWhatIsNotARecipient(t *testing.T) {
	id, err := age.GenerateHybridIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ParseRecipient(" " + id.Recipient().String() + "\n"); err != nil || got.String() != id.Recipient().String() {
		t.Errorf("a recipient with surrounding space = %v", err)
	}
	for name, s := range map[string]string{"empty": "", "garbage": "age1garbage", "an identity": id.String(), "two": id.Recipient().String() + "\n" + id.Recipient().String()} {
		_, err := ParseRecipient(s)
		if err == nil {
			t.Errorf("%s: parsed", name)
		} else if strings.Contains(err.Error(), "AGE-SECRET-KEY") {
			t.Errorf("%s: the error repeats a private key", name)
		}
	}
}

func TestFingerprintIsStableAndIsOfTheRecipientString(t *testing.T) {
	// SHA-256 of the recipient text, base64 without padding: the form ssh-keygen shows for its keys.
	const recipient = "age1ql3z7hjy54pw3hyww5ayyfg7zqgvc7w3j2elw8zmrj2kg5sfn9aqmcac8p"
	const want = "SHA256:z35WgjExUGhb8W5CxOpI4WjhboIGxcStN8ctATy2pfQ"
	if got := Fingerprint(recipient); got != want {
		t.Errorf("Fingerprint = %q, want %q", got, want)
	}
	if got := Fingerprint(recipient); got != want {
		t.Errorf("Fingerprint is not stable: %q", got)
	}
	if Fingerprint(recipient+"x") == want {
		t.Error("another recipient has the same fingerprint")
	}
}

func TestFileNameIsTheCreationTimeInUTC(t *testing.T) {
	at := time.Date(2026, 10, 22, 14, 5, 9, 0, time.FixedZone("CEST", 2*60*60))
	if got, want := FileName(at), "suiteward-backup-20261022-120509.tar.age"; got != want {
		t.Errorf("FileName = %q, want %q", got, want)
	}
}
