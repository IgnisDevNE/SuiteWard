package installer

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/archive"
)

// completeDump ends as pg_dump ends a dump: a restore refuses a dump that does not.
const completeDump = dumpText + "\n--\n-- PostgreSQL database dump complete\n--\n"

// tarBytes is a tarball of an artifact volume.
func tarBytes(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, f := range []struct{ name, body string }{{"artifacts/", ""}, {"artifacts/ab/abcdef", "an artifact"}} {
		hdr := &tar.Header{Name: f.name, Mode: 0o600, Size: int64(len(f.body)), Typeflag: tar.TypeReg}
		if strings.HasSuffix(f.name, "/") {
			hdr.Typeflag, hdr.Mode = tar.TypeDir, 0o700
		}
		if err := w.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(f.body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.String()
}

// aBackup writes a backup of an instance to a directory of its own and returns the file. edit changes what is backed up.
func aBackup(t *testing.T, edit func(b *backup, e *backupEngine)) string {
	t.Helper()
	engine := &backupEngine{dump: completeDump, volume: tarBytes(t)}
	b := newBackup(t, engine)
	b.dir = t.TempDir()
	if edit != nil {
		edit(&b, engine)
	}
	path, err := takeBackup(context.Background(), b)
	if err != nil {
		t.Fatal(err)
	}
	return path
}

// backupOfVersion is a backup of another release than the one that runs, as its status and its manifest say.
func backupOfVersion(version, image string) func(b *backup, e *backupEngine) {
	return func(b *backup, _ *backupEngine) {
		b.image = image
		b.status = func(context.Context) ([]byte, error) {
			return []byte(`{"version":"` + version + `","schemaVersion":19,"instance":"` + testInstance + `"}`), nil
		}
	}
}

// restoreHarness is the stack of the upgrade harness, ready to be restored: a dump that is complete, a volume that is a tarball.
func restoreHarness(t *testing.T) *upgradeHarness {
	t.Helper()
	u := newUpgradeHarness(t, nil)
	u.first("exec -i suiteward-db pg_dump", completeDump)
	u.volumeData = tarBytes(t)
	return u
}

// restore runs `suiteward restore file` on the stack of the harness; the safety backup goes to the backups directory of the harness.
func (u *upgradeHarness) restore(file string, args ...string) error {
	u.t.Helper()
	return Restore(context.Background(), u.restoreArgs(file, args...), u.env())
}

func (u *upgradeHarness) restoreArgs(file string, args ...string) []string {
	all := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--runtime", "podman", "--connection", "pm", "--backup-dir", u.backups}
	if file != "" {
		all = append([]string{file}, all...)
	}
	return append(all, args...)
}

// confirmed are the flags that confirm a restore where nobody can type the instance id.
func confirmed(t *testing.T, dir string) []string {
	t.Helper()
	return []string{"--kit", writeTestKit(t, dir), "--yes", "--instance", testInstance}
}

// destructive lists what the engine was asked to change that a restore does only after the confirmation, before the first call that contains marker.
func destructive(f *fakeEngine, marker string) []string {
	var out []string
	for _, c := range f.calls {
		line := normalized(c)
		if strings.Contains(line, marker) {
			return out
		}
		for _, verb := range []string{"rm ", "run ", "stop ", "start ", "volume rm", "volume create", "volume import", "secret create", "secret rm"} {
			if strings.HasPrefix(line, verb) {
				out = append(out, line)
			}
		}
	}
	return out
}

// nothingChanged fails when the engine was asked for anything but reads, a backup of the current state included.
func nothingChanged(t *testing.T, u *upgradeHarness) {
	t.Helper()
	if got := destructive(u.f, "\x00"); len(got) != 0 {
		t.Errorf("the engine was asked to change something: %v", got)
	}
	if len(u.f.find("psql -X -q -1")) != 0 {
		t.Error("a dump was loaded")
	}
	if entries, _ := os.ReadDir(u.backups); len(entries) != 0 {
		t.Errorf("the refused restore left %v in the backups directory", entries)
	}
}

func TestRestoreReplacesTheInstanceInOrderAndChecksIt(t *testing.T) {
	u := restoreHarness(t)
	file := aBackup(t, nil)
	if err := u.restore(file, confirmed(t, t.TempDir())...); err != nil {
		t.Fatal(err)
	}
	u.assertOrder(
		"exec -i suiteward-db pg_dump -U suiteward -d suiteward", "volume export suiteward-data --output", // the safety backup of what runs
		"rm --force --ignore --time 45 suiteward", // the service stops
		" -q -1 -v ON_ERROR_STOP=1 -U suiteward -d suiteward -f -",
		"volume rm suiteward-data", "volume create suiteward-data", "volume import suiteward-data",
		"run --rm --replace --name suiteward-migrate", "run --detach --replace --name suiteward --network",
	)
	// Nothing is changed before the confirmed restore has taken the safety backup.
	if got := destructive(u.f, "volume export"); len(got) != 0 {
		t.Errorf("the instance was changed before its safety backup: %v", got)
	}
	if u.imported != tarBytes(t) {
		t.Errorf("the volume was given %q, want the artifacts of the backup", u.imported)
	}
	safety := oneArchive(t, u.backups)
	_, members := openBackup(t, safety)
	if members["suiteward.sql"] != completeDump || members["status.json"] != statusBody {
		t.Errorf("the safety backup does not hold the state that was replaced: %v", members)
	}
	out := u.out.String()
	for _, want := range []string{safety, "v0.2.0", testInstance, "octo", "octo/app", "same GitHub App", "before the restore"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	u.f.assertNoSecret(t, "HUNTER2-TOTP", DatabaseURL(appRole, "x"))
	noIdentityInCalls(t, u.f.calls)
	noIdentityIn(t, "the output", out)
	if strings.Contains(out, "HUNTER2-TOTP") {
		t.Error("the content of the backup reached the output")
	}
}

// The schema is emptied and the dump loaded in one transaction, on standard input: a load that fails leaves the database as it was.
func TestRestoreLoadsTheDumpInOneTransactionOnStandardInput(t *testing.T) {
	u := restoreHarness(t)
	if err := u.restore(aBackup(t, nil), confirmed(t, t.TempDir())...); err != nil {
		t.Fatal(err)
	}
	loads := u.f.find(" -q -1 -v ON_ERROR_STOP=1")
	if len(loads) != 1 {
		t.Fatalf("the dump was loaded %d times: %v", len(loads), u.f.lines())
	}
	if !strings.HasPrefix(loads[0].stdin, "DROP SCHEMA public CASCADE;\nCREATE SCHEMA public;\n") || !strings.HasSuffix(loads[0].stdin, completeDump) {
		t.Errorf("the load is not the empty schema and the dump:\n%q", loads[0].stdin)
	}
	if strings.Contains(loads[0].line(), "HUNTER2-TOTP") {
		t.Error("the dump is in the argument list")
	}
}

func TestRestoreRefusesBeforeChangingAnything(t *testing.T) {
	_, other, err := archive.NewKey()
	if err != nil {
		t.Fatal(err)
	}
	type refusal struct {
		name  string
		file  func(t *testing.T) string
		args  func(t *testing.T) []string
		check func(t *testing.T, err error)
	}
	mention := func(want ...string) func(t *testing.T, err error) {
		return func(t *testing.T, err error) {
			t.Helper()
			for _, w := range want {
				if !strings.Contains(err.Error(), w) {
					t.Errorf("the refusal lacks %q: %v", w, err)
				}
			}
		}
	}
	good := func(t *testing.T) []string { return confirmed(t, t.TempDir()) }
	damaged := func(change func(raw []byte) []byte) func(t *testing.T) string {
		return func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "damaged.tar.age")
			if err := os.WriteFile(path, change([]byte(readFile(t, aBackup(t, nil)))), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}
	}
	cases := []refusal{
		{"another instance's kit", func(t *testing.T) string { return aBackup(t, nil) }, func(t *testing.T) []string {
			kit := filepath.Join(t.TempDir(), "other.txt")
			if err := os.WriteFile(kit, []byte(archive.RecoveryKit(other, "age1pq1x", "other")), 0o600); err != nil {
				t.Fatal(err)
			}
			return []string{"--kit", kit, "--yes", "--instance", testInstance}
		}, func(t *testing.T, err error) {
			if !errors.Is(err, archive.ErrWrongKey) {
				t.Errorf("got %v, want the wrong-key refusal", err)
			}
		}},
		{"a backup altered after it was written", damaged(func(raw []byte) []byte { raw[len(raw)/2] ^= 0xff; return raw }), good, func(t *testing.T, err error) {
			if !errors.Is(err, archive.ErrInvalid) {
				t.Errorf("got %v, want the invalid-backup refusal", err)
			}
		}},
		{"a backup cut short", damaged(func(raw []byte) []byte { return raw[:len(raw)/2] }), good, func(t *testing.T, err error) {
			if !errors.Is(err, archive.ErrInvalid) {
				t.Errorf("got %v, want the invalid-backup refusal", err)
			}
		}},
		{"a file that is not a backup", func(t *testing.T) string {
			path := filepath.Join(t.TempDir(), "x.tar.age")
			if err := os.WriteFile(path, []byte("not an age file"), 0o600); err != nil {
				t.Fatal(err)
			}
			return path
		}, good, func(t *testing.T, err error) {
			if !errors.Is(err, archive.ErrInvalid) {
				t.Errorf("got %v", err)
			}
		}},
		{"a file that is not there", func(t *testing.T) string { return filepath.Join(t.TempDir(), "absent.tar.age") }, good, mention("absent.tar.age")},
		{"a kit that is not there", func(t *testing.T) string { return aBackup(t, nil) }, func(t *testing.T) []string {
			return []string{"--kit", filepath.Join(t.TempDir(), "absent.txt"), "--yes", "--instance", testInstance}
		}, mention("absent.txt")},
		{"a dump that was cut short", func(t *testing.T) string {
			return aBackup(t, func(_ *backup, e *backupEngine) { e.dump = dumpText })
		}, good, mention("dump", "cut short")},
		{"artifacts that are not a tarball", func(t *testing.T) string {
			return aBackup(t, func(_ *backup, e *backupEngine) { e.volume = "TARBYTES" })
		}, good, mention("artifacts", "tar")},
		{"another release", func(t *testing.T) string { return aBackup(t, backupOfVersion("v0.1.9", testImage)) }, good, mention("v0.1.9", "v0.2.0", releaseImageRepo+"@"+testDigest, "install")},
		{"no way to confirm", func(t *testing.T) string { return aBackup(t, nil) }, func(t *testing.T) []string { return []string{"--kit", writeTestKit(t, t.TempDir())} }, mention("--yes", "--instance", testInstance)},
		{"an instance that is not the backup's", func(t *testing.T) string { return aBackup(t, nil) }, func(t *testing.T) []string {
			return []string{"--kit", writeTestKit(t, t.TempDir()), "--yes", "--instance", "deadbeef"}
		}, mention("not confirmed", "nothing was changed")},
		{"--yes without the instance", func(t *testing.T) string { return aBackup(t, nil) }, func(t *testing.T) []string {
			return []string{"--kit", writeTestKit(t, t.TempDir()), "--yes"}
		}, mention("--instance")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := restoreHarness(t)
			err := u.restore(tc.file(t), tc.args(t)...)
			if err == nil {
				t.Fatal("the restore went on")
			}
			tc.check(t, err)
			nothingChanged(t, u)
			noIdentityIn(t, "the error", err.Error())
			if strings.Contains(err.Error(), "HUNTER2-TOTP") {
				t.Error("the dump reached the error")
			}
		})
	}
}

func TestRestoreRefusesABackupThatLacksAMember(t *testing.T) {
	recipient, _ := testRecoveryKey(t)
	rec, err := archive.ParseRecipient(recipient)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "partial.tar.age")
	f, err := os.Create(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	w, err := archive.NewWriter(f, rec, archive.Info{SuitewardVersion: "v0.2.0", ImageDigest: oldDigest, Instance: testInstance, Tier: "co-located", CreatedAt: backupTime})
	if err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{backupStatus: statusBody, backupVolume: tarBytes(t)} {
		if err := w.Add(name, int64(len(body)), strings.NewReader(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	u := restoreHarness(t)
	err = u.restore(path, confirmed(t, t.TempDir())...)
	if err == nil || !strings.Contains(err.Error(), backupDump) {
		t.Fatalf("got %v, want a refusal that names the missing dump", err)
	}
	nothingChanged(t, u)
}

func TestRestoreNeedsExactlyOneSource(t *testing.T) {
	u := restoreHarness(t)
	for name, args := range map[string][]string{
		"none":                   u.restoreArgs(""),
		"a file and a directory": u.restoreArgs(aBackup(t, nil), "--plaintext-dir", t.TempDir()),
		"two files":              u.restoreArgs("one.tar.age", "two.tar.age"),
	} {
		t.Run(name, func(t *testing.T) {
			if err := Restore(context.Background(), args, u.env()); !errors.Is(err, ErrUsage) {
				t.Errorf("got %v, want a usage error", err)
			}
		})
	}
	nothingChanged(t, u)
}

// On a terminal the owner types the instance id of the backup; a flag does not answer for them.
func TestRestoreOnATerminalAsksForTheInstanceId(t *testing.T) {
	kit := writeTestKit(t, t.TempDir())
	run := func(u *upgradeHarness, input string, args ...string) error {
		env := u.env()
		env.Term = Terminal{In: strings.NewReader(input), Out: &u.out, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, nil }}
		return Restore(context.Background(), append(u.restoreArgs(aBackup(t, nil), "--kit", kit), args...), env)
	}
	u := restoreHarness(t)
	if err := run(u, testInstance+"\n"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u.out.String(), "Type the instance id "+testInstance) {
		t.Errorf("the question does not name the instance id:\n%s", u.out.String())
	}
	for name, input := range map[string]string{"another id": "deadbeef\n", "yes": "yes\n", "no answer": "", "an empty answer": "\n"} {
		t.Run(name, func(t *testing.T) {
			u := restoreHarness(t)
			err := run(u, input, "--yes", "--instance", testInstance) // the flags do not answer on a terminal
			if err == nil || !strings.Contains(err.Error(), "not confirmed") {
				t.Fatalf("got %v", err)
			}
			nothingChanged(t, u)
		})
	}
}

func TestRestoreTakesTheKitFromAHiddenPromptOrFromStandardInput(t *testing.T) {
	_, identity := testRecoveryKey(t)
	kitText := archive.RecoveryKit(identity, "unused", testInstance)

	// A terminal: the identity line is pasted without echo, then the id is typed.
	u := restoreHarness(t)
	env := u.env()
	env.Term = Terminal{In: strings.NewReader(testInstance + "\n"), Out: &u.out, IsTerminal: true, ReadPassword: func() ([]byte, error) { return []byte(identity), nil }}
	if err := Restore(context.Background(), u.restoreArgs(aBackup(t, nil)), env); err != nil {
		t.Fatal(err)
	}
	noIdentityIn(t, "the output", u.out.String())
	noIdentityInCalls(t, u.f.calls)

	// No terminal: the kit is piped in, and the flags confirm.
	u = restoreHarness(t)
	env = u.env()
	env.Term = Terminal{In: strings.NewReader(kitText), Out: &u.out}
	if err := Restore(context.Background(), u.restoreArgs(aBackup(t, nil), "--yes", "--instance", testInstance), env); err != nil {
		t.Fatal(err)
	}
	noIdentityIn(t, "the output", u.out.String())

	// Nothing piped in: there is no key to open the backup with.
	u = restoreHarness(t)
	env = u.env()
	env.Term = Terminal{In: strings.NewReader(""), Out: &u.out}
	if err := Restore(context.Background(), u.restoreArgs(aBackup(t, nil), "--yes", "--instance", testInstance), env); err == nil || !strings.Contains(err.Error(), "recovery") {
		t.Fatalf("got %v, want a refusal that asks for the recovery key", err)
	}
	nothingChanged(t, u)
}

// A key is never an argument, and so never in the process list.
func TestRestoreRefusesTheKeyAsAnArgument(t *testing.T) {
	_, identity := testRecoveryKey(t)
	u := restoreHarness(t)
	err := u.restore(aBackup(t, nil), "--yes", "--instance", testInstance, "--recovery-key", identity)
	if err == nil || !errors.Is(err, ErrUsage) {
		t.Fatalf("got %v, want a usage error: there is no such flag", err)
	}
	noIdentityIn(t, "the error", err.Error())
	nothingChanged(t, u)
}

func TestRestoreOfAnInstanceThatDoesNotAnswerHasNoSafetyBackupAndComparesTheRelease(t *testing.T) {
	u := restoreHarness(t)
	u.status = func(ctx context.Context, address string) ([]byte, error) {
		if len(u.f.find("--name suiteward --network")) == 0 { // the service runs again only when the restore started it
			return nil, errConnectionRefused
		}
		return httpGet(ctx, address)
	}
	if err := u.restore(aBackup(t, nil), confirmed(t, t.TempDir())...); err != nil {
		t.Fatal(err)
	}
	if len(u.f.find("pg_dump")) != 0 {
		t.Error("a safety backup was taken of an instance that does not answer")
	}
	if !strings.Contains(u.out.String(), "does not answer") {
		t.Errorf("the output does not say that nothing was backed up:\n%s", u.out.String())
	}

	// Without /status the release is compared by the digest of the installed image.
	u = restoreHarness(t)
	u.status = func(context.Context, string) ([]byte, error) { return nil, errConnectionRefused }
	err := u.restore(aBackup(t, backupOfVersion("v0.1.9", testImage)), confirmed(t, t.TempDir())...)
	if err == nil || !strings.Contains(err.Error(), releaseImageRepo+"@"+testDigest) {
		t.Fatalf("got %v, want the release to install first", err)
	}
	nothingChanged(t, u)
}

func TestRestoreWarnsOfAnotherInstanceAndAnotherTier(t *testing.T) {
	u := restoreHarness(t)
	file := aBackup(t, func(b *backup, _ *backupEngine) {
		b.tier = "isolated"
		b.status = func(context.Context) ([]byte, error) {
			return []byte(strings.Replace(statusBody, testInstance, "0a0b0c0d", 1)), nil
		}
	})
	if err := u.restore(file, "--kit", writeTestKit(t, t.TempDir()), "--yes", "--instance", "0a0b0c0d"); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"0a0b0c0d", "isolated", "another instance", "tier"} {
		if !strings.Contains(u.out.String(), want) {
			t.Errorf("the preview lacks %q:\n%s", want, u.out.String())
		}
	}
}

// ---- backups of the releases before this one ----

// plaintextBackup is a directory as `install --upgrade` of alpha.1 and alpha.2 and the manual runbook leave it.
func plaintextBackup(t *testing.T, dump, tarball string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range map[string]string{backupDump: dump, backupVolume: tarball, backupStatus: statusBody} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

func TestRestoreOfAPlaintextDirectoryWarnsAndNeedsNoKit(t *testing.T) {
	u := restoreHarness(t)
	dir := plaintextBackup(t, completeDump, tarBytes(t))
	if err := u.restore("", "--plaintext-dir", dir, "--yes", "--instance", testInstance); err != nil {
		t.Fatal(err)
	}
	out := u.out.String()
	for _, want := range []string{"plaintext", "not encrypted", "v0.2.0", "octo"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	u.assertOrder("pg_dump", "rm --force --ignore --time 45 suiteward", " -q -1 -v ON_ERROR_STOP=1", "volume import suiteward-data", "--name suiteward --network")
	if u.imported != tarBytes(t) {
		t.Errorf("the artifacts were not imported: %q", u.imported)
	}
	if got := readFile(t, filepath.Join(dir, backupDump)); got != completeDump {
		t.Error("the plaintext backup was changed")
	}
}

func TestRestoreOfAPlaintextDirectoryNeedsAKeyForTheSafetyBackupAndAGoodBackup(t *testing.T) {
	// The safety backup is encrypted, and there is no kit to take its key from: without a recipient in the settings the restore refuses.
	u := newUpgradeHarness(t, noRecipient)
	u.first("exec -i suiteward-db pg_dump", completeDump)
	err := u.restore("", "--plaintext-dir", plaintextBackup(t, completeDump, tarBytes(t)), "--yes", "--instance", testInstance)
	if err == nil || !strings.Contains(err.Error(), "suiteward backup init") {
		t.Fatalf("got %v, want a refusal that names `suiteward backup init`", err)
	}
	nothingChanged(t, u)

	// The checks of an encrypted backup apply to the directory too.
	for name, dir := range map[string]string{
		"a dump cut short":       plaintextBackup(t, dumpText, tarBytes(t)),
		"a damaged tarball":      plaintextBackup(t, completeDump, "TARBYTES"),
		"a directory of nothing": t.TempDir(),
	} {
		t.Run(name, func(t *testing.T) {
			u := restoreHarness(t)
			if err := u.restore("", "--plaintext-dir", dir, "--yes", "--instance", testInstance); err == nil {
				t.Fatal("the restore went on")
			}
			nothingChanged(t, u)
		})
	}
}

// ---- a failure after the service was stopped ----

func TestRestoreThatFailsAfterTheStopNamesTheSafetyBackupAndHowToGoOn(t *testing.T) {
	cases := []struct {
		name string
		rule rule
		want []string
	}{
		{"the load", rule{match: " -q -1 -v ON_ERROR_STOP=1", err: errors.New("psql: ERROR: COPY owner, line 1: \"HUNTER2-TOTP\"")}, []string{"the database was not changed", "suiteward install"}},
		{"the import", rule{match: "volume import", err: errInjected}, []string{errInjected.Error(), "database was replaced", "suiteward restore"}},
		{"the migration", rule{match: "run --rm --replace --name suiteward-migrate", err: errInjected}, []string{errInjected.Error(), "suiteward install"}},
		{"the start", rule{match: "run --detach --replace --name suiteward --network", err: errInjected}, []string{errInjected.Error(), "suiteward install"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := restoreHarness(t)
			u.f.rules = append([]rule{tc.rule}, u.f.rules...)
			err := u.restore(aBackup(t, nil), confirmed(t, t.TempDir())...)
			if err == nil {
				t.Fatal("the restore reported success")
			}
			safety := oneArchive(t, u.backups)
			for _, want := range append(tc.want, safety, "suiteward restore "+safety) {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("the error lacks %q:\n%v", want, err)
				}
			}
			if strings.Contains(err.Error(), "HUNTER2-TOTP") {
				t.Errorf("the dump reached the error: %v", err)
			}
			if entries, _ := os.ReadDir(u.backups); len(entries) != 1 {
				t.Errorf("the failed restore left %v beside the safety backup", entries)
			}
		})
	}
}

// What the service reports after the restore is compared with the status that was backed up.
func TestRestoreReportsWhatDiffersBetweenTheServiceAndTheBackup(t *testing.T) {
	cases := map[string]string{
		"the owner":     strings.Replace(statusBody, `"login":"octo"`, `"login":"someone"`, 1),
		"the version":   strings.Replace(statusBody, `"version":"v0.2.0"`, `"version":"v0.2.1"`, 1),
		"the schema":    strings.Replace(statusBody, `"schemaVersion":20`, `"schemaVersion":21`, 1),
		"the integrity": strings.Replace(statusBody, `"integrity":"passed"`, `"integrity":"failed"`, 1),
	}
	for name, after := range cases {
		t.Run(name, func(t *testing.T) {
			u := restoreHarness(t)
			u.status = func(_ context.Context, _ string) ([]byte, error) {
				if len(u.f.find("--name suiteward --network")) == 0 {
					return []byte(statusBody), nil
				}
				return []byte(after), nil
			}
			err := u.restore(aBackup(t, nil), confirmed(t, t.TempDir())...)
			if err == nil || !strings.Contains(err.Error(), "does not match") || !strings.Contains(err.Error(), oneArchive(t, u.backups)) {
				t.Fatalf("got %v, want a report that names the difference and the safety backup", err)
			}
		})
	}

	// A service that has not checked the integrity yet is not a difference.
	u := restoreHarness(t)
	u.status = func(_ context.Context, _ string) ([]byte, error) {
		if len(u.f.find("--name suiteward --network")) == 0 {
			return []byte(statusBody), nil
		}
		return []byte(strings.Replace(statusBody, `,"integrity":"passed"`, ``, 1)), nil
	}
	if err := u.restore(aBackup(t, nil), confirmed(t, t.TempDir())...); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u.out.String(), "integrity") {
		t.Errorf("the output does not say that the integrity is not checked yet:\n%s", u.out.String())
	}
}

func TestRestoreWaitsForTheServiceToAnswerAfterTheStart(t *testing.T) {
	u := restoreHarness(t)
	u.status = func(_ context.Context, _ string) ([]byte, error) {
		if len(u.f.find("--name suiteward --network")) == 0 {
			return []byte(statusBody), nil
		}
		return nil, errConnectionRefused
	}
	err := u.restore(aBackup(t, nil), confirmed(t, t.TempDir())...)
	if err == nil || !strings.Contains(err.Error(), "/status") || !strings.Contains(err.Error(), oneArchive(t, u.backups)) {
		t.Fatalf("got %v, want a report that /status never answered and the safety backup", err)
	}
}

// ---- the other tier and the other runtime ----

func TestRestoreOnTheIsolatedTierStopsTheUnitsAndStartsThemAgain(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	u.f.dump = completeDump
	u.f.roleExists = true
	u.f.objects[VolumeData] = true
	file := aBackup(t, func(b *backup, _ *backupEngine) { b.tier = "isolated" })
	args := []string{file, "--tier", "isolated", "--settings", settingsFile(u.home), "--backup-dir", u.backups, "--kit", writeTestKit(t, t.TempDir()), "--yes", "--instance", testInstance}
	fastHealthWait(t)
	if err := Restore(context.Background(), args, u.env()); err != nil {
		t.Fatal(err)
	}
	at := 0
	for _, marker := range []string{
		"podman exec -i suiteward-db pg_dump", "podman volume export suiteward-data --output",
		"systemctl --user stop suiteward.service suiteward-migrate.service",
		" -q -1 -v ON_ERROR_STOP=1 -U suiteward -d suiteward -f -",
		"podman volume rm suiteward-data", "systemctl --user restart suiteward-data-volume.service", "podman volume import suiteward-data",
		"systemctl --user restart suiteward-migrate.service", "systemctl --user start suiteward.service",
	} {
		i := u.f.index(marker, at)
		if i < 0 {
			t.Fatalf("no %q after call %d:\n%s", marker, at, strings.Join(u.f.lines(), "\n"))
		}
		at = i + 1
	}
	if u.f.imported != tarBytes(t) {
		t.Errorf("the volume was given %q", u.f.imported)
	}
	oneArchive(t, u.backups)
}

func TestRestoreOnTheIsolatedTierNamesTheUnitToStartWhenItFails(t *testing.T) {
	u := newIsoUpgrade(t, nil)
	u.f.dump = completeDump
	u.f.roleExists = true
	u.f.objects[VolumeData] = true
	u.f.failOn["podman volume import"] = errInjected
	file := aBackup(t, func(b *backup, _ *backupEngine) { b.tier = "isolated" })
	args := []string{file, "--tier", "isolated", "--settings", settingsFile(u.home), "--backup-dir", u.backups, "--kit", writeTestKit(t, t.TempDir()), "--yes", "--instance", testInstance}
	err := Restore(context.Background(), args, u.env())
	if err == nil || !strings.Contains(err.Error(), errInjected.Error()) || !strings.Contains(err.Error(), "suiteward restore "+oneArchive(t, u.backups)) {
		t.Fatalf("got %v", err)
	}
}

func TestRestoreOnDockerReplacesTheContainersAndWritesTheVolumeThroughAThrowawayContainer(t *testing.T) {
	h, backups := newDockerUpgrade(t)
	h.first("exec -i suiteward-db pg_dump", completeDump)
	file := aBackup(t, nil)
	args := []string{file, "--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "docker", "--backup-dir", backups, "--kit", writeTestKit(t, t.TempDir()), "--yes", "--instance", testInstance}
	if err := Restore(context.Background(), args, h.newEnv()); err != nil {
		t.Fatal(err)
	}
	h.assertOrder("pg_dump", "stop --time 45 suiteward", " -q -1 -v ON_ERROR_STOP=1", "volume rm suiteward-data", "volume create suiteward-data", "tar -xf - -C /data", "run --rm --name suiteward-migrate", "run --detach --name suiteward --network")
	imports := h.f.find("tar -xf - -C /data")
	if len(imports) != 1 || imports[0].stdin != tarBytes(t) {
		t.Errorf("the volume was not written from the tarball of the backup: %v", imports)
	}
}

// The order the restore keeps is also what the screen shows, and the safety backup and the paths outlast it.
func TestRestoreOnAFullScreenWalksTheSteps(t *testing.T) {
	u := restoreHarness(t)
	c := newTermConsole(&u.out)
	env := u.env()
	env.OpenScreen = c.open
	if err := Restore(liveContext(t), u.restoreArgs(aBackup(t, nil), confirmed(t, t.TempDir())...), env); err != nil {
		t.Fatal(err)
	}
	c.steps(t, "Verify", "Preview", "Safety backup", "Stop", "Load", "Start", "Compare")
	if !strings.Contains(c.tail(), oneArchive(t, u.backups)) {
		t.Errorf("the safety backup is not in the scrollback after Close:\n%s", c.tail())
	}
	noRepeats(t, c.tail())
	if !c.closed() {
		t.Error("the screen was not closed")
	}
}

func TestMainRoutesRestoreAndTheUsageNamesIt(t *testing.T) {
	u := restoreHarness(t)
	var stderr bytes.Buffer
	args := append([]string{"restore"}, u.restoreArgs(aBackup(t, nil), confirmed(t, t.TempDir())...)...)
	if code := Main(context.Background(), args, u.env(), &stderr); code != 0 {
		t.Fatalf("exit %d, stderr %q", code, stderr.String())
	}
	for _, want := range []string{"suiteward restore <backup.tar.age>", "--plaintext-dir"} {
		if !strings.Contains(Usage, want) {
			t.Errorf("the usage lacks %q:\n%s", want, Usage)
		}
	}
	stderr.Reset()
	if code := Main(context.Background(), []string{"restore"}, u.env(), &stderr); code != 2 || !strings.Contains(stderr.String(), "suiteward restore") {
		t.Errorf("no source: exit %d, stderr %q", code, stderr.String())
	}
}
