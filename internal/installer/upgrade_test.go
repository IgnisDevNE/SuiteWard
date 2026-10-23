package installer

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var (
	oldDigest = "sha256:" + strings.Repeat("b", 64)
	oldImage  = releaseImageRepo + "@" + oldDigest
	oldPin    = "docker.io/library/postgres:18.5-trixie@sha256:" + strings.Repeat("c", 64)
)

const (
	statusBody = `{"version":"v0.2.0","schemaVersion":20,"tier":"co-located","instance":"1a2b3c4d","owner":{"login":"octo","accountId":7,"enrolledAt":"2026-10-01T00:00:00Z"},"github":[{"repo":"octo/app","integrity":"passed"}]}`
	dumpText   = "-- PostgreSQL database dump\nCOPY owner (totp_secret) FROM stdin;\nHUNTER2-TOTP\n"
)

// upgradeHarness is a co-located stack that runs an older release, with a service that answers /status.
type upgradeHarness struct {
	*harness
	backups string
	port    int
	stack   Settings // what the installed stack was made from
}

func newUpgradeHarness(t *testing.T, edit func(s *Settings)) *upgradeHarness {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/status" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, statusBody)
	}))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	h := newHarness(t, "linux")
	h.status = httpGet // the service of this harness answers on a real port
	s := colocatedSettings()
	s.HTTPPort, s.MCPPort, s.Connection, s.Image = port, port+1, "pm", oldImage
	s.BackupRecipient, _ = testRecoveryKey(t)
	if edit != nil {
		edit(&s)
	}
	h.existingStack(s, oldImage, nil)
	h.have(KindNetwork, NetworkName)
	h.have(KindVolume, VolumeData, VolumeDBData)
	h.have(kindSecret, SecretDBAuth, SecretDatabaseOwnerURL, SecretDatabaseURL, SecretMCPAuth, SecretGitHubAppKey)
	h.first("secret inspect --showsecret --format {{.SecretData}} "+SecretDatabaseURL, DatabaseURL(appRole, "x")+"\n")
	h.first("exec -i suiteward-db pg_dump", dumpText)
	h.roleExists = true
	if err := SaveSettings(h.settingsPath(), s); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(h.tokenPath(), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	return &upgradeHarness{harness: h, backups: filepath.Join(h.dir, "backups"), port: port, stack: s}
}

// restack replaces the installed containers: the rules of the new stack go before the ones of the first.
func (u *upgradeHarness) restack(image string, edit func(name string, obj map[string]any)) {
	u.t.Helper()
	inspected := inspectedStack(u.t, u.stack, image, edit)
	for _, name := range []string{ContainerServe, ContainerDB} {
		u.first("inspect --type container "+name, string(inspected[name]))
	}
}

// env is the harness environment, where `volume export` writes its file as the engine does.
func (u *upgradeHarness) env() Env {
	env := u.newEnv()
	inner := env.Deps.Exec
	env.Deps.Exec = func(ctx context.Context, name string, args []string, stdin io.Reader) ([]byte, error) {
		out, err := inner(ctx, name, args, stdin)
		if _, rest, ok := strings.Cut(strings.Join(args, " "), "volume export "); ok && err == nil {
			if fields := strings.Fields(rest); len(fields) == 3 && fields[1] == "--output" {
				if err := os.WriteFile(fields[2], []byte("TARBYTES"), 0o644); err != nil { //nolint:gosec // the engine writes it world-readable
					return nil, err
				}
			}
		}
		return out, err
	}
	return env
}

func (u *upgradeHarness) upgrade(args ...string) error {
	u.t.Helper()
	all := append([]string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--upgrade", "--backup-dir", u.backups, "--runtime", "podman", "--connection", "pm"}, args...)
	return Install(context.Background(), all, u.env())
}

// backup is the one encrypted backup the upgrade made.
func (u *upgradeHarness) backup() string {
	u.t.Helper()
	return oneArchive(u.t, u.backups)
}

// oneArchive is the one backup file under dir.
func oneArchive(t *testing.T, dir string) string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "suiteward-backup-*.tar.age"))
	if err != nil || len(files) != 1 {
		t.Fatalf("want one backup file under %s, got %v, %v", dir, files, err)
	}
	return files[0]
}

// beforeCall lists the changes the engine was asked for before the first call that contains marker.
func (u *upgradeHarness) changesBefore(marker string) []string {
	var out []string
	for _, c := range u.f.calls {
		if strings.Contains(c.line(), marker) {
			return out
		}
		line := normalized(c)
		if slices.ContainsFunc(mutatingVerbs, func(v string) bool { return strings.HasPrefix(line, v) }) {
			out = append(out, line)
		}
	}
	u.t.Fatalf("no call contains %q", marker)
	return nil
}

func (u *upgradeHarness) noChanges() {
	u.t.Helper()
	for _, c := range u.f.calls {
		line := normalized(c)
		if strings.Contains(line, "pg_dump") || strings.Contains(line, "volume export") {
			continue
		}
		if slices.ContainsFunc(append([]string{"pull "}, mutatingVerbs...), func(v string) bool { return strings.HasPrefix(line, v) }) {
			u.t.Fatalf("a change was made: %s", line)
		}
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestUpgradeBacksUpThenPullsThenBringsTheStackUp(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	if err := u.upgrade("--image", testImage, "--yes"); err != nil {
		t.Fatal(err)
	}
	u.assertOrder(
		"exec -i suiteward-db pg_dump -U suiteward -d suiteward",
		"volume export suiteward-data --output",
		"pull "+testImage,
		"rm --force --ignore --time 45 suiteward",
		"run --detach --replace --name suiteward-db ",
		"--format {{json .State}} suiteward-db",
		"run --rm --replace --name suiteward-migrate ",
		"run --detach --replace --name suiteward --network",
	)
	if before := u.changesBefore("pg_dump"); len(before) != 0 {
		t.Errorf("the stack was changed before the backup: %v", before)
	}
	// The dump goes first: the artifact volume taken after it holds every artifact the dump refers to.
	if dump, export := indexOf(u.f.lines(), "pg_dump"), indexOf(u.f.lines(), "volume export"); dump > export {
		t.Errorf("the volume was exported before the dump")
	}
	if got := u.settings().Image; got != testImage {
		t.Errorf("the settings keep %s, not the new image", got)
	}
	if run := u.f.find("--name suiteward --network"); len(run) != 1 || !strings.HasSuffix(run[0].line(), " "+testImage) {
		t.Errorf("the service did not start from the new release: %v", run)
	}
	out := u.out.String()
	for _, want := range []string{oldDigest, testDigest, u.backup(), "suiteward doctor", "restore"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
}

func indexOf(lines []string, text string) int {
	return slices.IndexFunc(lines, func(l string) bool { return strings.Contains(l, text) })
}

func TestUpgradeBackupFilesAreComplete(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	if err := u.upgrade("--image", testImage, "--yes"); err != nil {
		t.Fatal(err)
	}
	path := u.backup()
	manifest, members := openBackup(t, path)
	for file, want := range map[string]string{"suiteward.sql": dumpText, "suiteward-artifacts.tar": "TARBYTES", "status.json": statusBody} {
		if got := members[file]; got != want {
			t.Errorf("%s holds %q, want %q", file, got, want)
		}
	}
	// The settings file of the backup is the one the upgrade found, before it saved the new release.
	if got := members["settings.yaml"]; !strings.Contains(got, oldDigest) || strings.Contains(got, testDigest) {
		t.Errorf("settings.yaml is not the settings of the old release:\n%s", got)
	}
	if manifest.SuitewardVersion != "v0.2.0" || manifest.Instance != testInstance || manifest.Tier != "co-located" || manifest.ImageDigest != oldDigest {
		t.Errorf("the manifest names %+v", manifest.Info)
	}
	if runtime.GOOS != "windows" {
		for p, want := range map[string]os.FileMode{u.backups: 0o700, path: 0o600} {
			if info, err := os.Stat(p); err != nil || info.Mode().Perm() != want {
				t.Errorf("%s has mode %v, %v; want %v", p, info.Mode().Perm(), err, want)
			}
		}
	}
	assertNoPlaintext(t, u.backups, "HUNTER2-TOTP", "TARBYTES")
}

func TestUpgradeNeverShowsTheBackupOrAPassword(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	if err := u.upgrade("--image", testImage, "--yes"); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"HUNTER2-TOTP", "TARBYTES"} {
		if strings.Contains(u.out.String(), secret) {
			t.Errorf("the backup content reached the output")
		}
	}
	u.f.assertNoSecret(t, "HUNTER2-TOTP", DatabaseURL(appRole, "x"))

	// A failing dump does not put its output in the error either.
	u = newUpgradeHarness(t, nil)
	u.f.rules = append([]rule{{match: "pg_dump", err: errors.New("pg_dump: error: connection failed")}}, u.f.rules...)
	err := u.upgrade("--image", testImage, "--yes")
	if err == nil || strings.Contains(err.Error(), "HUNTER2-TOTP") {
		t.Fatalf("got %v", err)
	}
}

func TestUpgradeStopsWhenTheBackupFailsAndChangesNothing(t *testing.T) {
	for name, setup := range map[string]func(u *upgradeHarness){
		"the dump": func(u *upgradeHarness) {
			u.f.rules = append([]rule{{match: "pg_dump", err: errors.New("pg_dump failed")}}, u.f.rules...)
		},
		"empty dump": func(u *upgradeHarness) { u.first("exec -i suiteward-db pg_dump", "") },
		"the volume": func(u *upgradeHarness) {
			u.f.rules = append([]rule{{match: "volume export", err: errors.New("export failed")}}, u.f.rules...)
		},
	} {
		t.Run(name, func(t *testing.T) {
			u := newUpgradeHarness(t, nil)
			setup(u)
			err := u.upgrade("--image", testImage, "--yes")
			if err == nil || !strings.Contains(err.Error(), "backup") {
				t.Fatalf("got %v", err)
			}
			u.noChanges()
			if dirs, _ := filepath.Glob(filepath.Join(u.backups, "*")); len(dirs) != 0 {
				t.Errorf("a partial backup was left behind: %v", dirs)
			}
			if got := u.settings().Image; got != oldImage {
				t.Errorf("the settings changed to %s", got)
			}
		})
	}
}

func TestUpgradeAsksBeforeChangingAnything(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	err := u.upgrade("--image", testImage)
	if err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("got %v", err)
	}
	u.noChanges()
	if len(u.f.find("pg_dump")) != 0 {
		t.Error("the backup ran before the confirmation")
	}

	for answer, proceeds := range map[string]bool{"yes\n": true, "n\n": false} {
		u = newUpgradeHarness(t, nil)
		env := u.env()
		env.Term = Terminal{In: strings.NewReader(answer), Out: &u.out, IsTerminal: true, ReadPassword: func() ([]byte, error) { return nil, nil }}
		args := []string{"--settings", u.settingsPath(), "--token-file", u.tokenPath(), "--upgrade", "--backup-dir", u.backups, "--runtime", "podman", "--connection", "pm", "--image", testImage}
		err := Install(context.Background(), args, env)
		if proceeds != (err == nil) {
			t.Errorf("answer %q: got %v", answer, err)
		}
		if !proceeds {
			u.noChanges()
		}
	}
}

func TestUpgradeRefusesWhenNothingIsInstalled(t *testing.T) {
	h := newHarness(t, "linux")
	err := Install(context.Background(), []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--upgrade", "--yes", "--tier", "co-located", "--runtime", "podman", "--connection", "pm", "--image", testImage}, h.newEnv())
	if err == nil || !strings.Contains(err.Error(), "suiteward install") {
		t.Fatalf("got %v, want a pointer to install", err)
	}
	h.mustNotMutate()
}

func TestUpgradeStopsOnADifferenceBeforeTheBackup(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	u.restack(oldImage, func(name string, obj map[string]any) {
		if name == ContainerServe {
			obj["HostConfig"].(map[string]any)["Memory"] = int64(512 << 20)
		}
	})
	err := u.upgrade("--image", testImage, "--yes")
	var diff *DiffError
	if !errors.As(err, &diff) {
		t.Fatalf("got %v, want a *DiffError", err)
	}
	if len(u.f.find("pg_dump")) != 0 {
		t.Error("a backup was taken of a stack the installer cannot carry")
	}
	u.noChanges()
}

func TestUpgradeToTheInstalledReleaseDoesNothing(t *testing.T) {
	u := newUpgradeHarness(t, func(s *Settings) { s.Image = testImage })
	u.restack(testImage, nil)
	if err := u.upgrade("--image", testImage, "--yes"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u.out.String(), "already on "+testDigest) || !strings.Contains(u.out.String(), "suiteward doctor") {
		t.Errorf("the output must say it is up to date and name doctor:\n%s", u.out.String())
	}
	if len(u.f.find("pg_dump")) != 0 {
		t.Error("an up-to-date stack was backed up")
	}
	u.noChanges()
}

func TestUpgradeDefaultsToTheReleaseTheBinaryCarries(t *testing.T) {
	was := ImageDigest
	t.Cleanup(func() { ImageDigest = was })

	ImageDigest = testDigest
	u := newUpgradeHarness(t, nil)
	if err := u.upgrade("--yes"); err != nil {
		t.Fatal(err)
	}
	if run := u.f.find("--name suiteward --network"); len(run) != 1 || !strings.HasSuffix(run[0].line(), " "+testImage) {
		t.Errorf("the service did not start from the embedded release: %v", run)
	}

	ImageDigest = ""
	u = newUpgradeHarness(t, nil)
	if err := u.upgrade("--yes"); err == nil || !strings.Contains(err.Error(), "--image") {
		t.Fatalf("a development build must ask for --image: %v", err)
	}
	u.noChanges()
}

func TestUpgradeRefusesATagOrAnotherShape(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	if err := u.upgrade("--image", "ghcr.io/ignisdevne/suiteward:v0.3.0", "--yes"); !errors.Is(err, ErrNotDigestImage) {
		t.Fatalf("got %v", err)
	}
	u.noChanges()
}

func TestUpgradeCarriesTheLocalFields(t *testing.T) {
	u := newUpgradeHarness(t, func(s *Settings) {
		s.JobTimeout = "3m"
		s.GitHub = GitHub{AppID: 1234, InstallationID: 5678, Owner: "Acme", Repositories: []string{"Acme/api", "Acme/web"}, TestChecks: map[string]string{"Acme/api": "CI / test"}}
	})
	if err := u.upgrade("--image", testImage, "--yes"); err != nil {
		t.Fatal(err)
	}
	serve := u.f.find("--name suiteward --network")[0].line()
	for _, w := range []string{"--publish 127.0.0.1:" + strconv.Itoa(u.port) + ":8080", "SUITEWARD_JOB_TIMEOUT=3m", "SUITEWARD_GITHUB_APP_ID=1234", " " + testImage} {
		if !strings.Contains(serve, w) {
			t.Errorf("serve lacks %q: %s", w, serve)
		}
	}
	got := u.settings()
	if got.JobTimeout != "3m" || got.HTTPPort != u.port || got.GitHub.AppID != 1234 || got.GitHub.TestChecks["Acme/api"] != "CI / test" {
		t.Errorf("saved settings: %+v", got)
	}
}

func TestUpgradeRecreatesTheDatabaseOnANewPin(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	u.restack(oldImage, func(name string, obj map[string]any) {
		if name == ContainerDB {
			obj["Config"].(map[string]any)["Image"] = oldPin
		}
	})
	if err := u.upgrade("--image", testImage, "--yes"); err != nil {
		t.Fatalf("a database on an older pin is what an upgrade refreshes, not a difference: %v", err)
	}
	postgres, err := PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	if len(u.f.find("pull "+postgres)) != 1 {
		t.Errorf("the new database image was not pulled: %v", u.f.lines())
	}
	if run := u.f.find("--name suiteward-db "); len(run) != 1 || !strings.HasSuffix(run[0].line(), " "+postgres) {
		t.Errorf("the database was not recreated on the new pin: %v", run)
	}
	for _, want := range []string{oldPin, postgres} {
		if !strings.Contains(u.out.String(), want) {
			t.Errorf("the plan does not show %s:\n%s", want, u.out.String())
		}
	}
}

func TestUpgradeOfAV01StackConvertsItsSecrets(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	// In v0.1.x the application URL secret held the owner URL.
	u.first("secret inspect --showsecret --format {{.SecretData}} "+SecretDatabaseURL, DatabaseURL(ownerRole, "x")+"\n")
	if err := u.upgrade("--image", testImage, "--yes"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(sqlOf(u.harness), "\n"), "ALTER ROLE suiteward_app PASSWORD '") || len(u.secretWrites(SecretDatabaseURL)) != 1 {
		t.Errorf("the application role and its secret were not made: %v", u.f.lines())
	}
}

// newDockerUpgrade is a Docker stack that runs an older release, with a service that answers /status and a recovery key; backups
// go to the directory it returns.
func newDockerUpgrade(t *testing.T) (*harness, string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = io.WriteString(w, statusBody) }))
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, _ := strconv.Atoi(u.Port())
	h := dockerHarness(t, "windows")
	h.status = httpGet
	s := DefaultSettings()
	s.Runtime, s.HTTPPort, s.MCPPort, s.Image = RuntimeDocker, port, port+1, oldImage
	s.BackupRecipient, _ = testRecoveryKey(t)
	if err := SaveSettings(h.settingsPath(), s); err != nil {
		t.Fatal(err)
	}
	h.have(KindContainer, ContainerDB, ContainerServe)
	h.have(KindNetwork, NetworkName)
	h.have(KindVolume, VolumeData, VolumeDBData, "suiteward-secrets-suiteward-db", "suiteward-secrets-suiteward-migrate", "suiteward-secrets-suiteward")
	for _, file := range []string{"postgres-password", "database-owner-url", "database-url", "mcp-token"} {
		h.haveDockerSecret(file)
	}
	h.first("cat /s/database-url", DatabaseURL(appRole, "x"))
	h.first("inspect --type container suiteward-db", `[{"Config":{"Image":"`+oldPin+`"}}]`)
	h.first("exec -i suiteward-db pg_dump", dumpText)
	h.first("tar -cf - -C /data .", "TARBYTES")
	h.roleExists = true
	if err := os.WriteFile(h.tokenPath(), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	return h, filepath.Join(h.dir, "backups")
}

func TestUpgradeBacksUpDockerThroughAThrowawayContainer(t *testing.T) {
	h, backups := newDockerUpgrade(t)
	args := []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--upgrade", "--yes", "--backup-dir", backups, "--runtime", "docker", "--image", testImage}
	if err := Install(context.Background(), args, h.newEnv()); err != nil {
		t.Fatal(err)
	}
	export := h.f.find("tar -cf - -C /data .")
	if len(export) != 1 {
		t.Fatalf("the artifact volume was not read by a throwaway container: %v", h.f.lines())
	}
	for _, want := range []string{"--rm", "--network none", "--read-only", "--cap-drop all", suiteward64, VolumeData + ":/data:ro", oldPin} {
		if !strings.Contains(export[0].line(), want) {
			t.Errorf("the export container lacks %q: %s", want, export[0].line())
		}
	}
	if _, members := openBackup(t, oneArchive(t, backups)); members["suiteward-artifacts.tar"] != "TARBYTES" {
		t.Errorf("the tarball is not in the backup: %v", members)
	}
}

// suiteward64 is the uid that runs the service and owns its files; the export container reads as that user.
const suiteward64 = "--user 65532:65532"

func TestUpgradeUsageIsInTheHelp(t *testing.T) {
	if !strings.Contains(Usage, "--upgrade") {
		t.Errorf("the usage does not name --upgrade:\n%s", Usage)
	}
}

func TestUpgradeStopsWhenTheServiceDoesNotAnswerStatus(t *testing.T) {
	u := newUpgradeHarness(t, func(s *Settings) { s.HTTPPort, s.MCPPort = 1, 2 }) // nothing listens there
	err := u.upgrade("--image", testImage, "--yes")
	if err == nil || !strings.Contains(err.Error(), "backup") || !strings.Contains(err.Error(), "status") {
		t.Fatalf("status.json is part of the backup, so a service that does not answer fails it: %v", err)
	}
	u.noChanges()
	if dirs, _ := filepath.Glob(filepath.Join(u.backups, "*")); len(dirs) != 0 {
		t.Errorf("a partial backup was left behind: %v", dirs)
	}
}

func TestInstallRefusesToChangeTheReleaseOfAStackThatRuns(t *testing.T) {
	h := newHarness(t, "linux")
	h.existingStack(colocatedSettings(), oldImage, nil)
	h.have(KindNetwork, NetworkName)
	h.have(KindVolume, VolumeData, VolumeDBData)
	h.busy[18081], h.busy[18091] = true, true                      // the running service holds its own ports
	err := h.podman("--http-port", "18081", "--mcp-port", "18091") // --image testImage: not the installed release
	if err == nil || !strings.Contains(err.Error(), "--upgrade") {
		t.Fatalf("a different release than the installed one is an upgrade, which backs up first: %v", err)
	}
	h.mustNotMutate()

	// The installed release itself is a plain re-run.
	h = newHarness(t, "linux")
	h.existingStack(colocatedSettings(), testImage, nil)
	h.have(KindNetwork, NetworkName)
	h.have(KindVolume, VolumeData, VolumeDBData)
	h.have(kindSecret, SecretDBAuth, SecretDatabaseOwnerURL, SecretDatabaseURL, SecretMCPAuth)
	h.first("secret inspect --showsecret --format {{.SecretData}} "+SecretDatabaseURL, DatabaseURL(appRole, "x")+"\n")
	h.roleExists = true
	h.busy[18081], h.busy[18091] = true, true
	if err := h.podman("--http-port", "18081", "--mcp-port", "18091"); err != nil {
		t.Fatalf("a re-run on the installed release must work: %v", err)
	}
}

func TestUpgradeColocatedKeepsItsBackupBesideTheTokenNotInTheRoamingProfile(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	tokens := filepath.Join(u.dir, "private")
	token := filepath.Join(tokens, "mcp-token")
	if err := os.MkdirAll(tokens, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(token, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	args := []string{"--settings", u.settingsPath(), "--token-file", token, "--upgrade", "--yes", "--runtime", "podman", "--connection", "pm", "--image", testImage}
	if err := Install(context.Background(), args, u.env()); err != nil {
		t.Fatal(err)
	}
	if dirs, _ := filepath.Glob(filepath.Join(tokens, "backups", "suiteward-backup-*")); len(dirs) != 1 {
		t.Errorf("the backup is not beside the token file: %v", dirs)
	}
	if dirs, _ := filepath.Glob(filepath.Join(u.dir, "backups", "*")); len(dirs) != 0 {
		t.Errorf("a backup was made beside the settings, which live in the roaming profile by default: %v", dirs)
	}
}

func TestUpgradeAsksForTheTierOfAHostWithoutSettingsOnLinux(t *testing.T) {
	h := newHarness(t, "linux")
	err := Install(context.Background(), []string{"--settings", h.settingsPath(), "--upgrade", "--yes", "--image", testImage}, h.newEnv())
	if err == nil || !strings.Contains(err.Error(), "--tier") {
		t.Fatalf("got %v, want a request for --tier", err)
	}
	if len(h.f.calls) != 0 {
		t.Errorf("something ran before the tier was known: %v", h.f.lines())
	}
}

func TestUpgradeColocatedBeforeMigrateSaysTheDatabaseIsUnchanged(t *testing.T) {
	u := newUpgradeHarness(t, nil)
	u.f.rules = append([]rule{{match: "--name suiteward-db ", err: errors.New("the database did not start")}}, u.f.rules...)
	err := u.upgrade("--image", testImage, "--yes")
	if err == nil {
		t.Fatal("the database start must fail")
	}
	for _, want := range []string{"schema and the data are unchanged", u.backup(), "If the service is not running, run `suiteward install` with the options of this run", "suiteward install --upgrade"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the advice lacks %q:\n%v", want, err)
		}
	}
	if strings.Contains(err.Error(), "restore") {
		t.Errorf("a restore loses everything after the backup and the database was not migrated:\n%v", err)
	}
}

func TestUpgradeColocatedFromMigrateOnKeepsTheRestoreAdvice(t *testing.T) {
	for name, fail := range map[string]string{"migrate": "--name suiteward-migrate ", "service": "--name suiteward --network"} {
		u := newUpgradeHarness(t, nil)
		u.f.rules = append([]rule{{match: fail, err: errors.New(name + " failed")}}, u.f.rules...)
		err := u.upgrade("--image", testImage, "--yes")
		if err == nil || !strings.Contains(err.Error(), "restore") || strings.Contains(err.Error(), "schema and the data are unchanged") {
			t.Fatalf("%s: once migrate ran the way back is the backup: %v", name, err)
		}
	}
}

// otherPort is a valid port that differs from p, which the harness takes from the free ports and may be near the top of the range.
func otherPort(p int) int {
	if p+100 <= 65535 {
		return p + 100
	}
	return p - 100
}

func TestUpgradeColocatedRefusesPortsAndTimeoutThatDifferFromTheInstalledOnes(t *testing.T) {
	// An upgrade keeps the installed ports and job timeout, so a run that asks for others stops before any change instead of
	// exiting 0 and ignoring them; on this tier a re-run of the install applies them.
	u := newUpgradeHarness(t, func(s *Settings) { s.JobTimeout = "3m" })
	other := strconv.Itoa(otherPort(u.port))
	err := u.upgrade("--image", testImage, "--yes", "--http-port", other, "--job-timeout", "9m")
	if err == nil {
		t.Fatal("an upgrade that is asked for other ports must not go on and ignore them")
	}
	for _, want := range []string{"suiteward install", "--http-port " + other, "--job-timeout 9m"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error lacks %q: %v", want, err)
		}
	}
	for _, not := range []string{"--mcp-port", "--tier isolated"} {
		if strings.Contains(err.Error(), not) {
			t.Errorf("the error must not say %q: %v", not, err)
		}
	}
	if len(u.f.find("pg_dump")) != 0 {
		t.Error("a backup was taken before the refusal")
	}
	u.noChanges()

	// The values that are installed are no difference, and an invalid one is refused as it is on an install.
	u = newUpgradeHarness(t, func(s *Settings) { s.JobTimeout = "3m" })
	if err := u.upgrade("--image", testImage, "--yes", "--http-port", strconv.Itoa(u.port), "--mcp-port", strconv.Itoa(u.port+1), "--job-timeout", "3m"); err != nil {
		t.Fatalf("asking for what is installed is not a change: %v", err)
	}
	u = newUpgradeHarness(t, nil)
	if err := u.upgrade("--image", testImage, "--yes", "--http-port", "70000"); err == nil {
		t.Fatal("an invalid port must be refused")
	}
	u.noChanges()
}
