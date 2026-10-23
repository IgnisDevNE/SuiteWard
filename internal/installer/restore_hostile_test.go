package installer

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/archive"
)

// Anyone who holds the public recipient can write an archive that decrypts, so what a restore prints of one and builds a suggestion
// from is checked when the archive is read.

// hostileBackup writes an archive whose manifest says info and whose status.json is status, with a good dump and good artifacts.
func hostileBackup(t *testing.T, info archive.Info, status string) string {
	t.Helper()
	recipient, _ := testRecoveryKey(t)
	rec, err := archive.ParseRecipient(recipient)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "hostile.tar.age")
	f, err := os.Create(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	w, err := archive.NewWriter(f, rec, info)
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []struct{ name, body string }{{backupStatus, status}, {backupDump, completeDump}, {backupVolume, tarBytes(t)}} {
		if err := w.Add(m.name, int64(len(m.body)), strings.NewReader(m.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}

func goodInfo() archive.Info {
	return archive.Info{SuitewardVersion: "v0.2.0", ImageDigest: oldDigest, Instance: testInstance, Tier: "co-located", CreatedAt: backupTime}
}

// noControl fails when text holds a character that a terminal would act on, a line break apart.
func noControl(t *testing.T, what, text string) {
	t.Helper()
	if i := strings.IndexFunc(text, func(r rune) bool { return (unicode.IsControl(r) && r != '\n') || unicode.Is(unicode.Cf, r) }); i >= 0 {
		t.Errorf("%s holds a control character at %d: %q", what, i, text[i:min(i+8, len(text))])
	}
}

func TestRestoreRefusesAManifestWhoseFieldsAreNotWhatTheyShouldBe(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(i *archive.Info)
		field string
	}{
		{"a digest with an escape sequence", func(i *archive.Info) { i.ImageDigest = oldDigest + "\x1b[2J" }, "image digest"},
		{"a digest that is short", func(i *archive.Info) { i.ImageDigest = "sha256:abc" }, "image digest"},
		{"a digest of another algorithm", func(i *archive.Info) { i.ImageDigest = "md5:" + strings.Repeat("a", 64) }, "image digest"},
		{"a digest that is a command", func(i *archive.Info) { i.ImageDigest = oldDigest + "; rm -rf ~" }, "image digest"},
		{"an instance with a line break", func(i *archive.Info) { i.Instance = testInstance + "\nsuiteward install --image evil" }, "instance"},
		{"an instance in capitals", func(i *archive.Info) { i.Instance = "1A2B3C4D" }, "instance"},
		{"an instance that is too long", func(i *archive.Info) { i.Instance = testInstance + "00" }, "instance"},
		{"a version with a space", func(i *archive.Info) { i.SuitewardVersion = "v0.2.0 evil" }, "version"},
		{"a version with an escape sequence", func(i *archive.Info) { i.SuitewardVersion = "v0.2.0\x1b]0;x\a" }, "version"},
		{"an empty version", func(i *archive.Info) { i.SuitewardVersion = "" }, "version"},
		{"a tier with a carriage return", func(i *archive.Info) { i.Tier = "isolated\r" }, "tier"},
		{"a tier that is a sentence", func(i *archive.Info) { i.Tier = "isolated and then some" }, "tier"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := restoreHarness(t)
			info := goodInfo()
			tc.edit(&info)
			err := u.restore(hostileBackup(t, info, statusBody), confirmed(t, t.TempDir())...)
			if err == nil || !errors.Is(err, archive.ErrInvalid) || !strings.Contains(err.Error(), tc.field) {
				t.Fatalf("got %v, want an invalid-backup refusal that names the %s", err, tc.field)
			}
			noControl(t, "the error", err.Error())
			noControl(t, "the output", u.out.String())
			if strings.Contains(err.Error()+u.out.String(), "evil") {
				t.Errorf("what the archive says reached the output:\n%v\n%s", err, u.out.String())
			}
			nothingChanged(t, u)
		})
	}
}

// A backup made while the image of the stack was not known has no digest: that is not a hostile manifest.
func TestRestoreAcceptsAManifestWithoutAnImageDigest(t *testing.T) {
	u := restoreHarness(t)
	info := goodInfo()
	info.ImageDigest = ""
	if err := u.restore(hostileBackup(t, info, statusBody), confirmed(t, t.TempDir())...); err != nil {
		t.Fatal(err)
	}
}

// The status of a backup is checked as the status of a service is, and a version that is not a plain name is refused.
func TestRestoreRefusesAStatusWhoseVersionIsNotAPlainName(t *testing.T) {
	status := strings.Replace(statusBody, `"version":"v0.2.0"`, `"version":"v0.2.0\u001b[2J evil"`, 1)
	u := restoreHarness(t)
	err := u.restore(hostileBackup(t, goodInfo(), status), confirmed(t, t.TempDir())...)
	if err == nil || !strings.Contains(err.Error(), "version") {
		t.Fatalf("got %v, want a refusal that names the version", err)
	}
	noControl(t, "the error", err.Error())
	nothingChanged(t, u)
}

// What a restore prints of the status of a backup goes through the cleaning of the full screen, on one line, also in plain output,
// which reaches the terminal as it is.
func TestRestoreCleansWhatItPrintsOfTheStatusOfABackup(t *testing.T) {
	status := `{"version":"v0.2.0","schemaVersion":20,"instance":"` + testInstance + `",` +
		`"owner":{"login":"oc\u001b[2Jto\r\nsuiteward install --image evil"},"github":[{"repo":"ev\u0007il\u001b[31m/app","integrity":"passed"}]}`
	u := restoreHarness(t)
	u.status = func(context.Context, string) ([]byte, error) { return []byte(status), nil } // the service says the same, so the compare holds
	if err := u.restore(hostileBackup(t, goodInfo(), status), confirmed(t, t.TempDir())...); err != nil {
		t.Fatal(err)
	}
	out := u.out.String()
	noControl(t, "the output", out)
	for _, want := range []string{"oc[2Jto suiteward install --image evil", "evil[31m/app"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks the cleaned %q:\n%s", want, out)
		}
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "suiteward install --image") {
			t.Errorf("a line of the archive is a line of the output: %q", line)
		}
	}
}

// The reason that a backup records for a status that was unavailable is text of the archive too.
func TestRestoreCleansTheReasonOfAnUnavailableStatus(t *testing.T) {
	info := goodInfo()
	info.SuitewardVersion, info.Instance = "unknown", "unknown"
	status := `{"unavailable":true,"reason":"down\u001b[2J\u0007\nsuiteward install --image evil"}`
	u := restoreHarness(t)
	if err := u.restore(hostileBackup(t, info, status), "--kit", writeTestKit(t, t.TempDir()), "--yes", "--instance", "unknown"); err != nil {
		t.Fatal(err)
	}
	out := u.out.String()
	noControl(t, "the output", out)
	if !strings.Contains(out, "down[2J suiteward install --image evil") {
		t.Errorf("the output lacks the cleaned reason:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "suiteward install --image") {
			t.Errorf("a line of the archive is a line of the output: %q", line)
		}
	}
}

// A directory of an earlier release never records an unavailable status, and its version is checked too.
func TestRestoreRefusesAHostilePlaintextDirectory(t *testing.T) {
	for name, status := range map[string]string{
		"an unavailable status": `{"unavailable":true,"reason":"x\u001b[2J"}`,
		"a hostile version":     strings.Replace(statusBody, `"version":"v0.2.0"`, `"version":"v0.2.0\u001b[2J"`, 1),
	} {
		t.Run(name, func(t *testing.T) {
			u := restoreHarness(t)
			dir := plaintextBackup(t, completeDump, tarBytes(t))
			if err := os.WriteFile(filepath.Join(dir, backupStatus), []byte(status), 0o600); err != nil {
				t.Fatal(err)
			}
			err := u.restore("", "--plaintext-dir", dir, "--yes", "--instance", testInstance)
			if err == nil {
				t.Fatal("the restore went on")
			}
			noControl(t, "the error", err.Error())
			noControl(t, "the output", u.out.String())
			nothingChanged(t, u)
		})
	}
}

// parseYesNo can fail on a value that a program passes; the confirmation reports it instead of reading it as a no.
func TestRestoreConfirmReportsAFlagThatIsNotYesOrNo(t *testing.T) {
	r, err := NewResolver(map[string]string{"yes": "maybe", "instance": testInstance}, nil, Terminal{In: strings.NewReader(""), Out: &bytes.Buffer{}})
	if err != nil {
		t.Fatal(err)
	}
	x := &restoreRun{r: r, doc: statusDoc{Instance: testInstance}}
	if err := x.confirm(); err == nil || !strings.Contains(err.Error(), `"maybe"`) {
		t.Fatalf("got %v, want the value that is neither yes nor no", err)
	}
}
