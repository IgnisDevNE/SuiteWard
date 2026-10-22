package installer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"
)

const testPEM = "-----BEGIN PRIVATE KEY-----\nMIIEvQIBADANBgkq\n-----END PRIVATE KEY-----\n"

var mutatingVerbs = []string{"network create", "volume create", "secret create", "secret rm", "rm ", "run ", "stop ", "exec "}

// harness runs the install command against a fake engine. newHarness is Podman; dockerHarness is Docker.
type harness struct {
	t          *testing.T
	f          *fakeEngine
	out        bytes.Buffer
	dir        string
	busy       map[int]bool
	roleExists bool
	roleFails  bool
	stdin      string
}

func newHarness(t *testing.T, goos string) *harness {
	t.Helper()
	h := &harness{t: t, f: newFake(goos), dir: t.TempDir(), busy: map[int]bool{}}
	h.f.env["USERNAME"] = "tester"
	h.f.on("info --format {{.Host.Security.Rootless}}", "true\n")
	h.f.on("--format {{json .State}} suiteward-db", `{"Status":"running","Health":{"Status":"healthy"}}`)
	return h
}

func dockerHarness(t *testing.T, goos string) *harness {
	t.Helper()
	h := &harness{t: t, f: newFake(goos), dir: t.TempDir(), busy: map[int]bool{}}
	h.f.env["USERNAME"] = "tester"
	h.f.on("version --format", dockerDesktopServer).on("info --format", `["name=seccomp,profile=builtin"]`)
	h.f.on("--format {{json .State}} suiteward-db", `{"Status":"running","Health":{"Status":"healthy"}}`)
	return h
}

// first puts a rule before the ones that exist.
func (h *harness) first(match, out string) {
	h.f.rules = append([]rule{{match: match, out: out}}, h.f.rules...)
}

func (h *harness) settingsPath() string { return filepath.Join(h.dir, "settings.yaml") }
func (h *harness) tokenPath() string    { return filepath.Join(h.dir, "mcp-token") }

// Exists answers: objects of the stack that are already there.
func (h *harness) have(kind Kind, names ...string) {
	for _, name := range names {
		switch kind {
		case KindContainer:
			h.f.on("container ls --all --filter name="+name+" --format", name)
		default:
			h.f.on(string(kind)+" ls --filter name="+name+" --format", name)
		}
	}
}

// haveDockerSecret makes the Docker secret volume of every consumer answer that the file is there.
func (h *harness) haveDockerSecret(file string) { h.f.on("test -f /s/"+file, "yes") }

func (h *harness) install(args ...string) error {
	h.t.Helper()
	d := h.f.deps()
	inner := d.Exec
	d.Exec = func(ctx context.Context, name string, a []string, stdin io.Reader) ([]byte, error) {
		var data []byte
		if stdin != nil {
			var err error
			if data, err = io.ReadAll(stdin); err != nil {
				return nil, err
			}
			stdin = bytes.NewReader(data)
		}
		out, err := inner(ctx, name, a, stdin)
		if strings.Contains(strings.Join(a, " "), "psql") {
			sql := string(data)
			switch {
			case h.roleFails && strings.Contains(sql, " ROLE "):
				// A failing statement echoes itself, password included, as psql does.
				return nil, errors.New("psql: ERROR: LINE 1: " + sql)
			case strings.Contains(sql, "pg_roles") && h.roleExists:
				return []byte("1\n"), err
			}
		}
		return out, err
	}
	env := Env{
		Deps:     d,
		Term:     Terminal{In: strings.NewReader(h.stdin), Out: &h.out},
		PortFree: func(port int) error { return busyError(h.busy, port) },
		Sleep:    func(time.Duration) {},
	}
	base := []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath()}
	return Install(context.Background(), append(base, args...), env)
}

func busyError(busy map[int]bool, port int) error {
	if busy[port] {
		return errors.New("address already in use")
	}
	return nil
}

func (h *harness) podman(args ...string) error {
	h.t.Helper()
	return h.install(append([]string{"--runtime", "podman", "--connection", "pm", "--image", testImage}, args...)...)
}

func (h *harness) docker(args ...string) error {
	h.t.Helper()
	return h.install(append([]string{"--runtime", "docker", "--accept-docker", "--image", testImage}, args...)...)
}

// normalized is the argument line without the leading engine selector.
func normalized(c call) string {
	line := c.line()
	for _, selector := range []string{"--connection ", "--context "} {
		if rest, ok := strings.CutPrefix(line, selector); ok {
			_, line, _ = strings.Cut(rest, " ")
		}
	}
	return line
}

func (h *harness) mutations() []string {
	var out []string
	for _, c := range h.f.calls {
		line := normalized(c)
		if slices.ContainsFunc(mutatingVerbs, func(v string) bool { return strings.HasPrefix(line, v) }) {
			out = append(out, line)
		}
	}
	return out
}

func (h *harness) mustNotMutate() {
	h.t.Helper()
	if m := h.mutations(); len(m) != 0 {
		h.t.Fatalf("something was changed before the refusal: %v", m)
	}
}

// assertOrder fails unless the markers each appear in some call, in this order.
func (h *harness) assertOrder(markers ...string) {
	h.t.Helper()
	at := 0
	for _, m := range markers {
		found := -1
		for i := at; i < len(h.f.calls); i++ {
			if strings.Contains(h.f.calls[i].line(), m) {
				found = i
				break
			}
		}
		if found < 0 {
			h.t.Fatalf("no call with %q after call %d; calls:\n%s", m, at, strings.Join(h.f.lines(), "\n"))
		}
		at = found + 1
	}
}

var passwordInSQL = regexp.MustCompile(`PASSWORD '([0-9a-f]+)'`)

// secretValues are the values this install generated or was given: whatever went to a secret store or into the role SQL.
func (h *harness) secretValues() []string {
	var values []string
	for _, c := range h.f.calls {
		line := c.line()
		switch {
		case strings.Contains(line, "secret create") || strings.Contains(line, "cat > /s/"):
			values = append(values, c.stdin)
		case strings.Contains(line, "psql"):
			if m := passwordInSQL.FindStringSubmatch(c.stdin); m != nil {
				values = append(values, m[1])
			}
		}
	}
	return values
}

// secretWrites returns the stdin of every secret write whose line mentions name.
func (h *harness) secretWrites(name string) []string {
	var out []string
	for _, c := range h.f.calls {
		if line := c.line(); (strings.Contains(line, "secret create") && strings.Contains(line, " "+name+" ")) ||
			(strings.Contains(line, "cat > /s/") && strings.Contains(line, "/s/"+secretFileOf(name)+" ")) {
			out = append(out, c.stdin)
		}
	}
	return out
}

func secretFileOf(name string) string {
	spec, err := lookupSecret(name)
	if err != nil {
		panic(err)
	}
	return spec.file
}

func (h *harness) assertNoLeak() {
	h.t.Helper()
	values := h.secretValues()
	if len(values) == 0 {
		h.t.Fatal("no secret was written, so the argument check proves nothing")
	}
	h.f.assertNoSecret(h.t, values...)
	for _, v := range values {
		if v != "" && strings.Contains(h.out.String(), v) {
			h.t.Errorf("a secret value was printed")
		}
	}
}

func (h *harness) settings() Settings {
	h.t.Helper()
	s, err := LoadSettings(h.settingsPath())
	if err != nil {
		h.t.Fatal(err)
	}
	return s
}

// ---- Podman ----

func TestInstallPodmanBringsTheStackUpInOrder(t *testing.T) {
	h := newHarness(t, "linux")
	if err := h.podman("--http-port", "18081", "--mcp-port", "18091"); err != nil {
		t.Fatal(err)
	}
	h.assertOrder(
		"pull "+testImage, "network create suiteward",
		"volume create suiteward-data", "volume create suiteward-db-data",
		"secret create suiteward-db-password -", "secret create suiteward-database-owner-url -", "secret create suiteward-mcp-token -",
		"rm --force --ignore --time 45 suiteward", "rm --force --ignore --time 45 suiteward-migrate",
		"run --detach --replace --name suiteward-db ",
		"--format {{json .State}} suiteward-db",
		"exec -i suiteward-db psql", "exec -i suiteward-db psql",
		"secret create --replace suiteward-database-url -",
		"run --rm --replace --name suiteward-migrate ",
		"run --detach --replace --name suiteward --network",
	)
	for _, c := range h.f.calls {
		if c.args[0] != "--connection" || c.args[1] != "pm" {
			t.Errorf("a call does not use the selected connection: %v", c.args)
		}
	}
	h.assertNoLeak()
	serve := h.f.find("--name suiteward --network")[0].line()
	for _, want := range []string{
		"--publish 127.0.0.1:18081:8080", "--publish 127.0.0.1:18091:8090", "--env SUITEWARD_INSTALLATION_TIER=co-located",
		"--secret suiteward-database-url,type=mount,target=/run/secrets/database-url,uid=65532,mode=0400",
		"--secret suiteward-mcp-token,type=mount,target=/run/secrets/mcp-token,uid=65532,mode=0400", " " + testImage,
	} {
		if !strings.Contains(serve, want) {
			t.Errorf("serve lacks %q: %s", want, serve)
		}
	}
	if strings.Contains(serve, "owner") || strings.Contains(serve, "MIGRATION") {
		t.Errorf("serve sees the owner URL: %s", serve)
	}
	migrate := h.f.find("--name suiteward-migrate ")[0].line()
	if !strings.Contains(migrate, "database-owner-url") || !strings.HasSuffix(migrate, testImage+" migrate") {
		t.Errorf("migrate: %s", migrate)
	}
	s := h.settings()
	if s.Runtime != RuntimePodman || s.Connection != "pm" || s.HTTPPort != 18081 || s.MCPPort != 18091 {
		t.Errorf("saved settings: %+v", s)
	}
	if !strings.Contains(h.out.String(), "GitHub is not configured") {
		t.Errorf("the next steps do not say that setup needs GitHub:\n%s", h.out.String())
	}
}

func TestInstallTheOwnerURLUsesTheOwnerPasswordAndTheAppURLTheRolePassword(t *testing.T) {
	h := newHarness(t, "linux")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	dbPassword := h.secretWrites(SecretDBAuth)
	owner := h.secretWrites(SecretDatabaseOwnerURL)
	app := h.secretWrites(SecretDatabaseURL)
	if len(dbPassword) != 1 || len(owner) != 1 || len(app) != 1 {
		t.Fatalf("writes: %d %d %d", len(dbPassword), len(owner), len(app))
	}
	if want := DatabaseURL("suiteward", dbPassword[0]); owner[0] != want {
		t.Errorf("owner URL %q, want %q", owner[0], want)
	}
	m := passwordInSQL.FindStringSubmatch(strings.Join(sqlOf(h), "\n"))
	if len(m) < 2 || app[0] != DatabaseURL(appRole, m[1]) {
		t.Errorf("the app URL %q does not carry the password of the role", app[0])
	}
}

func sqlOf(h *harness) []string {
	var out []string
	for _, c := range h.f.calls {
		if strings.Contains(c.line(), "psql") {
			out = append(out, c.stdin)
		}
	}
	return out
}

func TestInstallCreatesTheRoleThroughStdinWithLoggingOff(t *testing.T) {
	h := newHarness(t, "linux")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	var create string
	for _, c := range h.f.calls {
		if !strings.Contains(c.line(), "psql") {
			continue
		}
		if !strings.HasSuffix(c.line(), "psql -X -q -t -A -v ON_ERROR_STOP=1 -U suiteward -d suiteward -f -") {
			t.Errorf("psql is not fed on stdin: %s", c.line())
		}
		if strings.Contains(c.stdin, "CREATE ROLE") {
			create = c.stdin
		}
	}
	prefix := "SET log_statement = 'none'; SET log_min_error_statement = 'panic'; SET log_min_duration_statement = -1; CREATE ROLE suiteward_app LOGIN PASSWORD '"
	if !strings.HasPrefix(create, prefix) {
		t.Fatalf("role statement: %q", create)
	}
}

func TestInstallAltersAnExistingRoleInsteadOfCreatingIt(t *testing.T) {
	h := newHarness(t, "linux")
	h.roleExists = true
	if err := h.podman(); err != nil { // the application URL secret does not exist, so the role gets a new password
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(sqlOf(h), "\n"), "ALTER ROLE suiteward_app PASSWORD '") {
		t.Fatal("an existing role was not altered")
	}
}

func TestInstallRoleFailureDoesNotLeakThePassword(t *testing.T) {
	h := newHarness(t, "linux")
	h.roleFails = true
	err := h.podman()
	if err == nil {
		t.Fatal("a failing role statement did not stop the install")
	}
	m := passwordInSQL.FindStringSubmatch(strings.Join(sqlOf(h), "\n"))
	if m == nil {
		t.Fatal("the role statement did not run")
	}
	if strings.Contains(err.Error(), m[1]) || strings.Contains(h.out.String(), m[1]) {
		t.Fatalf("the password reached the error or the output: %v", err)
	}
	if len(h.f.find("--name suiteward-migrate ")) != 0 {
		t.Fatal("migrate ran after the role failed")
	}
}

func TestInstallRerunCreatesOnlyWhatIsMissing(t *testing.T) {
	h := newHarness(t, "linux")
	h.have(KindNetwork, NetworkName)
	h.have(KindVolume, VolumeData, VolumeDBData)
	for _, name := range []string{SecretDBAuth, SecretDatabaseOwnerURL, SecretDatabaseURL, SecretMCPAuth} {
		h.have(kindSecret, name)
	}
	h.first("secret inspect --showsecret --format {{.SecretData}} "+SecretDatabaseURL, DatabaseURL(appRole, "x")+"\n")
	h.roleExists = true
	if err := os.WriteFile(h.tokenPath(), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	for _, m := range h.mutations() {
		if strings.HasPrefix(m, "network create") || strings.HasPrefix(m, "volume create") || strings.HasPrefix(m, "secret create") {
			t.Errorf("a re-run created what exists: %s", m)
		}
	}
	if strings.Contains(strings.Join(sqlOf(h), "\n"), " ROLE ") {
		t.Error("a re-run touched the role")
	}
	if len(h.f.find("--name suiteward-migrate ")) != 1 || len(h.f.find("--name suiteward --network")) != 1 {
		t.Error("a re-run does not migrate and start the service again")
	}
	if got, _ := os.ReadFile(h.tokenPath()); string(got) != "old" {
		t.Error("the existing MCP token copy was replaced")
	}
}

func TestInstallConvertsAV01xStack(t *testing.T) {
	h := newHarness(t, "linux")
	h.have(kindSecret, SecretDBAuth, SecretDatabaseURL, SecretMCPAuth)
	h.have(KindNetwork, NetworkName)
	h.have(KindVolume, VolumeData, VolumeDBData)
	h.first("secret inspect --showsecret --format {{.SecretData}} "+SecretDBAuth, "ownerpw\n")
	h.first("secret inspect --showsecret --format {{.SecretData}} "+SecretDatabaseURL, DatabaseURL("suiteward", "ownerpw")+"\n")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.out.String(), "Upgrading an existing stack") {
		t.Errorf("the conversion was not announced:\n%s", h.out.String())
	}
	if owner := h.secretWrites(SecretDatabaseOwnerURL); len(owner) != 1 || owner[0] != DatabaseURL("suiteward", "ownerpw") {
		t.Errorf("owner URL writes: %q", owner)
	}
	if len(h.secretWrites(SecretDBAuth)) != 0 {
		t.Error("the owner password was replaced")
	}
	app := h.secretWrites(SecretDatabaseURL)
	if len(app) != 1 || !strings.HasPrefix(app[0], "postgres://suiteward_app:") {
		t.Errorf("the application URL was not written: %q", app)
	}
	if !strings.Contains(strings.Join(sqlOf(h), "\n"), "CREATE ROLE suiteward_app") {
		t.Error("the application role was not created")
	}
	h.assertOrder("secret create suiteward-database-owner-url -", "secret create --replace suiteward-database-url -", "--name suiteward-migrate ")
}

func TestInstallRefusesDatabaseSecretsWithoutThePassword(t *testing.T) {
	h := newHarness(t, "linux")
	h.have(kindSecret, SecretDatabaseURL)
	err := h.podman()
	if err == nil || !strings.Contains(err.Error(), SecretDBAuth) {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()
}

func TestInstallRefusesAURLThatIsNotPostgres(t *testing.T) {
	h := newHarness(t, "linux")
	h.have(kindSecret, SecretDBAuth, SecretDatabaseOwnerURL, SecretDatabaseURL)
	h.first("secret inspect --showsecret --format {{.SecretData}} "+SecretDatabaseURL, "mysql://u:hunter2@db/x\n")
	err := h.podman()
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()
}

func TestInstallStopsWhenMigrateFails(t *testing.T) {
	h := newHarness(t, "linux")
	h.f.fail("--name suiteward-migrate ", errors.New("exit status 1"))
	err := h.podman()
	if err == nil || !strings.Contains(err.Error(), "migrate") {
		t.Fatalf("got %v", err)
	}
	if len(h.f.find("--name suiteward --network")) != 0 {
		t.Error("the service started after a failed migrate")
	}
	if _, err := os.Stat(h.settingsPath()); err == nil {
		t.Error("settings were saved for an install that did not finish")
	}
}

func TestInstallStopsWhenTheDatabaseExits(t *testing.T) {
	h := newHarness(t, "linux")
	h.first("--format {{json .State}} suiteward-db", `{"Status":"exited"}`)
	err := h.podman()
	if err == nil || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("got %v", err)
	}
	if len(h.f.find("psql")) != 0 {
		t.Error("the role was created against a database that exited")
	}
}

func TestInstallGivesTheDatabaseNinetySeconds(t *testing.T) {
	h := newHarness(t, "linux")
	h.first("--format {{json .State}} suiteward-db", `{"Status":"running","Health":{"Status":"starting"}}`)
	err := h.podman()
	if err == nil || !strings.Contains(err.Error(), "90") {
		t.Fatalf("got %v", err)
	}
	if n := len(h.f.find("--format {{json .State}} suiteward-db")); n != 90 {
		t.Errorf("the database was polled %d times, want 90", n)
	}
}

// ---- preflight ----

func TestInstallRefusesARootfulConnectionBeforeChangingAnything(t *testing.T) {
	h := newHarness(t, "linux")
	h.first("info --format {{.Host.Security.Rootless}}", "false\n")
	if err := h.podman(); !errors.Is(err, ErrRootful) {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()
}

func TestInstallRefusesAPortThatIsInUse(t *testing.T) {
	h := newHarness(t, "linux")
	h.busy[18091] = true
	err := h.podman("--http-port", "18081", "--mcp-port", "18091")
	if err == nil || !strings.Contains(err.Error(), "18091") {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()
}

func TestInstallRefusesAnImageThatIsNotPinnedByDigest(t *testing.T) {
	h := newHarness(t, "linux")
	err := h.install("--runtime", "podman", "--connection", "pm", "--image", "ghcr.io/ignisdevne/suiteward:v0.2.0")
	if !errors.Is(err, ErrNotDigestImage) {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()
}

func TestInstallNeedsTheImage(t *testing.T) {
	h := newHarness(t, "linux")
	err := h.install("--runtime", "podman", "--connection", "pm")
	if err == nil || !strings.Contains(err.Error(), "image") {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()
}

func TestInstallTheIsolatedTierIsNotAvailableYet(t *testing.T) {
	h := newHarness(t, "linux")
	err := h.install("--tier", "isolated", "--image", testImage)
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("got %v", err)
	}
	if len(h.f.calls) != 0 {
		t.Errorf("an engine was called: %v", h.f.lines())
	}
}

func TestInstallRefusesAnUnknownRuntimeAndBadFlags(t *testing.T) {
	h := newHarness(t, "linux")
	if err := h.install("--runtime", "nerdctl", "--image", testImage); err == nil || !strings.Contains(err.Error(), "runtime") {
		t.Errorf("runtime: %v", err)
	}
	if err := h.install("--no-such-flag"); !errors.Is(err, ErrUsage) {
		t.Errorf("flag: %v", err)
	}
	if err := h.install("--http-port", "8091", "--mcp-port", "8091", "--image", testImage); err == nil {
		t.Error("the same port twice was accepted")
	}
	h.mustNotMutate()
}

func TestInstallSelectsTheRootlessMachineConnectionAndRecordsIt(t *testing.T) {
	h := newHarness(t, "windows")
	h.f.on("system connection list", machineConnections)
	if err := h.install("--runtime", "podman", "--image", testImage); err != nil {
		t.Fatal(err)
	}
	if got := h.settings().Connection; got != "podman-machine-default" {
		t.Errorf("recorded connection %q", got)
	}
}

// ---- adoption ----

func (h *harness) existingStack(s Settings, image string, edit func(name string, obj map[string]any)) {
	h.have(KindContainer, ContainerDB, ContainerServe)
	inspected := inspectedStack(h.t, s, image, edit)
	// The database rule comes first: the service's name is a prefix of it.
	for _, name := range []string{ContainerDB, ContainerServe} {
		h.f.on("inspect --type container "+name, string(inspected[name]))
	}
}

func TestInstallAdoptionStopsOnADifferenceAndChangesNothing(t *testing.T) {
	h := newHarness(t, "linux")
	h.existingStack(colocatedSettings(), testImage, func(name string, obj map[string]any) {
		if name == ContainerServe {
			obj["HostConfig"].(map[string]any)["Memory"] = int64(512 << 20)
		}
	})
	err := h.podman()
	var diff *DiffError
	if !errors.As(err, &diff) || !diffMentions(diff, "memory") {
		t.Fatalf("got %v, want a *DiffError about memory", err)
	}
	h.mustNotMutate()
}

func TestInstallAdoptionCarriesTheKnownFields(t *testing.T) {
	h := newHarness(t, "linux")
	want := localPS1Settings()
	h.existingStack(want, testImage, nil)
	h.have(KindNetwork, NetworkName)
	h.have(KindVolume, VolumeData, VolumeDBData)
	h.have(kindSecret, SecretDBAuth, SecretDatabaseOwnerURL, SecretDatabaseURL, SecretMCPAuth, SecretGitHubAppKey)
	h.first("secret inspect --showsecret --format {{.SecretData}} "+SecretDatabaseURL, DatabaseURL(appRole, "x")+"\n")
	h.roleExists = true
	h.busy[18081], h.busy[18091] = true, true // the running service holds its own ports
	if err := h.install("--runtime", "podman", "--connection", "pm"); err != nil {
		t.Fatal(err) // no --image: the installed one is the default of a re-run
	}
	got := h.settings()
	if got.HTTPPort != 18081 || got.MCPPort != 18091 || got.JobTimeout != "3m" || got.GitHub.AppID != 1234 || got.GitHub.Owner != "Acme" ||
		!slices.Equal(got.GitHub.Repositories, want.GitHub.Repositories) {
		t.Errorf("saved settings: %+v", got)
	}
	serve := h.f.find("--name suiteward --network")[0].line()
	for _, w := range []string{"--publish 127.0.0.1:18081:8080", "SUITEWARD_JOB_TIMEOUT=3m", "SUITEWARD_GITHUB_APP_ID=1234", " " + testImage} {
		if !strings.Contains(serve, w) {
			t.Errorf("serve lacks %q: %s", w, serve)
		}
	}
}

func TestInstallDoesNotRefusePortsHeldByItsOwnService(t *testing.T) {
	h := newHarness(t, "linux")
	h.existingStack(colocatedSettings(), testImage, nil)
	h.busy[18081], h.busy[18091] = true, true
	if err := h.podman("--http-port", "18081", "--mcp-port", "18091"); err != nil {
		t.Fatal(err)
	}
	h2 := newHarness(t, "linux")
	h2.existingStack(colocatedSettings(), testImage, nil)
	h2.busy[18082] = true
	if err := h2.podman("--http-port", "18082", "--mcp-port", "18091"); err == nil || !strings.Contains(err.Error(), "18082") {
		t.Fatalf("a new port in use was accepted: %v", err)
	}
}

// ---- GitHub ----

func (h *harness) writeKey() string {
	path := filepath.Join(h.dir, "app.pem")
	if err := os.WriteFile(path, []byte(testPEM), 0o600); err != nil {
		h.t.Fatal(err)
	}
	return path
}

var githubFlags = []string{
	"--github-app-id", "1234", "--github-installation-id", "5678", "--github-owner", "Acme", "--github-repositories", "Acme/api,Acme/web",
}

func TestInstallConfiguresGitHubAndStoresTheKeyFromAFile(t *testing.T) {
	h := newHarness(t, "linux")
	key := h.writeKey()
	if err := h.podman(append(githubFlags, "--github-app-key-file", key)...); err != nil {
		t.Fatal(err)
	}
	if writes := h.secretWrites(SecretGitHubAppKey); len(writes) != 1 || writes[0] != testPEM {
		t.Fatalf("key writes: %q", writes)
	}
	h.assertOrder("secret create suiteward-github-app-key -", "rm --force --ignore --time 45 suiteward")
	serve := h.f.find("--name suiteward --network")[0].line()
	for _, w := range []string{
		"--env SUITEWARD_GITHUB_APP_ID=1234", "--env SUITEWARD_GITHUB_INSTALLATION_ID=5678",
		"--env SUITEWARD_GITHUB_REPOSITORIES=Acme/api,Acme/web", "--env SUITEWARD_GITHUB_APP_KEY_FILE=/run/secrets/github-app-key",
		"--secret suiteward-github-app-key,type=mount,target=/run/secrets/github-app-key,uid=65532,mode=0400",
	} {
		if !strings.Contains(serve, w) {
			t.Errorf("serve lacks %q", w)
		}
	}
	if strings.Contains(h.f.find("--name suiteward-migrate ")[0].line(), "github") {
		t.Error("migrate gets GitHub")
	}
	if g := h.settings().GitHub; g.AppID != 1234 || g.InstallationID != 5678 || g.Owner != "Acme" || len(g.Repositories) != 2 {
		t.Errorf("saved GitHub settings: %+v", g)
	}
	h.f.assertNoSecret(t, testPEM, "MIIEvQIBADANBgkq")
	want := "podman --connection pm exec -it suiteward /suiteward setup --owner Acme --test-check 'Acme/api=<check name>' --test-check 'Acme/web=<check name>'"
	if !strings.Contains(h.out.String(), want) {
		t.Errorf("the next steps lack %q:\n%s", want, h.out.String())
	}
}

func TestInstallReadsTheKeyFromStdin(t *testing.T) {
	h := newHarness(t, "linux")
	h.stdin = testPEM
	if err := h.podman(append(githubFlags, "--github-app-key-file", "-")...); err != nil {
		t.Fatal(err)
	}
	if writes := h.secretWrites(SecretGitHubAppKey); len(writes) != 1 || writes[0] != testPEM {
		t.Fatalf("key writes: %q", writes)
	}
}

func TestInstallKeepsAnExistingKeyAndDoesNotAskForIt(t *testing.T) {
	h := newHarness(t, "linux")
	h.have(kindSecret, SecretGitHubAppKey)
	if err := h.podman(githubFlags...); err != nil {
		t.Fatal(err)
	}
	if len(h.secretWrites(SecretGitHubAppKey)) != 0 {
		t.Error("the existing key was replaced")
	}
}

func TestInstallGitHubNeedsAKeyWhenItsSecretIsMissing(t *testing.T) {
	h := newHarness(t, "linux")
	err := h.podman(githubFlags...)
	if err == nil || !strings.Contains(err.Error(), "github-app-key-file") {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()
}

func TestInstallRefusesAKeyThatIsNotAPEM(t *testing.T) {
	h := newHarness(t, "linux")
	path := filepath.Join(h.dir, "not-a-key")
	if err := os.WriteFile(path, []byte("hello"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := h.podman(append(githubFlags, "--github-app-key-file", path)...)
	if err == nil || !strings.Contains(err.Error(), "PEM") {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()
}

func TestInstallGitHubIsAllOrNothing(t *testing.T) {
	h := newHarness(t, "linux")
	err := h.podman("--github-app-id", "1234")
	if err == nil || !strings.Contains(err.Error(), "installation") {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()
}

func TestInstallWithoutGitHubStartsTheMCPListenerAndAsksNothing(t *testing.T) {
	h := newHarness(t, "linux")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	serve := h.f.find("--name suiteward --network")[0].line()
	if strings.Contains(serve, "GITHUB") || strings.Contains(serve, "github-app-key") {
		t.Errorf("serve has GitHub without it being configured: %s", serve)
	}
	if h.settings().GitHub.AppID != 0 {
		t.Error("GitHub settings were saved")
	}
}

func TestInstallTakesTheJobTimeoutForServeOnly(t *testing.T) {
	h := newHarness(t, "linux")
	if err := h.podman("--job-timeout", "3m"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.f.find("--name suiteward --network")[0].line(), "SUITEWARD_JOB_TIMEOUT=3m") {
		t.Error("serve lacks the job timeout")
	}
	if strings.Contains(h.f.find("--name suiteward-migrate ")[0].line(), "JOB_TIMEOUT") {
		t.Error("migrate has the job timeout")
	}
}

func TestInstallReadsAnswersAndFlagsOverrideThem(t *testing.T) {
	h := newHarness(t, "linux")
	answers := filepath.Join(h.dir, "answers.yaml")
	body := "image: " + testImage + "\nhttp-port: \"18081\"\nmcp-port: \"18091\"\nruntime: podman\nconnection: pm\n"
	if err := os.WriteFile(answers, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := h.install("--answers", answers, "--mcp-port", "18092"); err != nil {
		t.Fatal(err)
	}
	if s := h.settings(); s.HTTPPort != 18081 || s.MCPPort != 18092 {
		t.Errorf("ports %d %d", s.HTTPPort, s.MCPPort)
	}
}

// ---- the MCP token ----

func TestInstallWritesTheMCPTokenCopyAndRestrictsItOnWindows(t *testing.T) {
	h := newHarness(t, "windows")
	h.f.env["USERNAME"] = "tester"
	h.f.on("system connection list", machineConnections)
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	secret := h.secretWrites(SecretMCPAuth)
	if len(secret) != 1 || len(secret[0]) != 64 {
		t.Fatalf("token writes: %q", secret)
	}
	got, err := os.ReadFile(h.tokenPath())
	if err != nil || string(got) != secret[0] {
		t.Fatalf("the token copy %q does not hold the secret (%v)", got, err)
	}
	var acl []call
	for _, c := range h.f.calls {
		if c.name == "icacls" {
			acl = append(acl, c)
		}
	}
	if len(acl) != 1 || !slices.Equal(acl[0].args, []string{h.tokenPath(), "/inheritance:r", "/grant:r", "tester:F"}) {
		t.Fatalf("icacls calls: %+v", acl)
	}
	// The copy is restricted before the token is in it, and the secret is made after the copy.
	order := h.f.lines()
	icacls, create := -1, -1
	for i, c := range h.f.calls {
		if c.name == "icacls" {
			icacls = i
		}
		if strings.Contains(order[i], "secret create suiteward-mcp-token") {
			create = i
		}
	}
	if icacls < 0 || create < icacls {
		t.Errorf("icacls at %d, token secret at %d", icacls, create)
	}
}

func TestInstallTheTokenCopyIsPrivateOffWindows(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes")
	}
	h := newHarness(t, "linux")
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(h.tokenPath())
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("token copy: %v %v", info, err)
	}
	for _, c := range h.f.calls {
		if c.name == "icacls" {
			t.Error("icacls ran off Windows")
		}
	}
}

func TestInstallWarnsWhenTheTokenSecretExistsWithoutItsCopy(t *testing.T) {
	h := newHarness(t, "linux")
	h.have(kindSecret, SecretMCPAuth)
	if err := h.podman(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(h.out.String(), "mcp-token") || len(h.secretWrites(SecretMCPAuth)) != 0 {
		t.Errorf("output:\n%s", h.out.String())
	}
}

// ---- restart after a reboot ----

func TestInstallEnablesPodmanRestartInTheMachine(t *testing.T) {
	h := newHarness(t, "windows")
	h.f.env["USERNAME"] = "tester"
	h.f.on("system connection list", machineConnections)
	if err := h.podman("--connection", "podman-machine-default"); err != nil {
		t.Fatal(err)
	}
	got := h.f.find("machine ssh")
	want := "machine ssh --username user podman-machine-default systemctl --user enable podman-restart.service"
	if len(got) != 1 || got[0].line() != want {
		t.Fatalf("got %v, want %s", got, want)
	}
	if !strings.Contains(h.out.String(), "podman machine start") {
		t.Errorf("the machine-at-logon gap is not noted:\n%s", h.out.String())
	}
}

func TestInstallReportsWhenPodmanRestartCannotBeEnabled(t *testing.T) {
	h := newHarness(t, "windows")
	h.f.env["USERNAME"] = "tester"
	h.f.on("system connection list", machineConnections)
	h.f.fail("machine ssh", errors.New("ssh: connection refused"))
	if err := h.podman("--connection", "podman-machine-default"); err != nil {
		t.Fatalf("a failed enable stopped the install: %v", err)
	}
	if !strings.Contains(h.out.String(), "podman-restart.service") {
		t.Errorf("the failure was not reported:\n%s", h.out.String())
	}
}

func TestInstallEnablesPodmanRestartDirectlyOnLinux(t *testing.T) {
	h := newHarness(t, "linux")
	if err := h.install("--runtime", "podman", "--image", testImage); err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, c := range h.f.calls {
		if c.name == "systemctl" && slices.Equal(c.args, []string{"--user", "enable", "podman-restart.service"}) {
			found = true
		}
	}
	if !found {
		t.Errorf("calls: %v", h.f.lines())
	}
	if len(h.f.find("machine ssh")) != 0 {
		t.Error("a machine was addressed on Linux")
	}
}

// ---- Docker ----

func TestInstallDockerNeedsConfirmationAndNamesTheReasons(t *testing.T) {
	h := dockerHarness(t, "windows")
	err := h.install("--runtime", "docker", "--image", testImage)
	if err == nil || !strings.Contains(err.Error(), "--accept-docker") {
		t.Fatalf("got %v", err)
	}
	if len(h.f.calls) != 0 {
		t.Errorf("the engine was called before the confirmation: %v", h.f.lines())
	}
	for _, want := range []string{"Podman", "rootful", "secrets"} {
		if !strings.Contains(h.out.String(), want) {
			t.Errorf("the warning lacks %q:\n%s", want, h.out.String())
		}
	}
}

func TestInstallDockerAsksInAnInteractiveSession(t *testing.T) {
	h := dockerHarness(t, "linux")
	args := []string{"--settings", h.settingsPath(), "--token-file", h.tokenPath(), "--runtime", "docker", "--image", testImage}
	// Every question has a default: the tier, the confirmation (yes), the two ports and no GitHub.
	env := Env{
		Deps: h.f.deps(), PortFree: func(int) error { return nil }, Sleep: func(time.Duration) {},
		Term: Terminal{
			In: strings.NewReader("\nyes\n" + strings.Repeat("\n", 10)), Out: &h.out, IsTerminal: true,
			ReadPassword: func() ([]byte, error) { return nil, errors.New("no secret is asked for") },
		},
	}
	if err := Install(context.Background(), args, env); err != nil {
		t.Fatal(err)
	}
	if len(h.f.find("--name suiteward --network")) != 1 {
		t.Error("the service did not start after the confirmation")
	}
}

func TestInstallDockerBringsTheStackUpWithSecretVolumes(t *testing.T) {
	h := dockerHarness(t, "windows")
	if err := h.docker("--http-port", "18081", "--mcp-port", "18091"); err != nil {
		t.Fatal(err)
	}
	h.assertOrder(
		"network create suiteward", "volume create suiteward-data", "volume create suiteward-db-data",
		"cat > /s/postgres-password", "cat > /s/database-owner-url", "cat > /s/mcp-token",
		"run --detach --name suiteward-db ",
		"--format {{json .State}} suiteward-db",
		"exec -i suiteward-db psql", "exec -i suiteward-db psql",
		"cat > /s/database-url",
		"run --rm --name suiteward-migrate ",
		"run --detach --name suiteward --network",
	)
	for _, c := range h.f.calls {
		if c.name == "icacls" {
			continue
		}
		if c.name != dockerDesktopCLI || c.args[0] != "--context" || c.args[1] != "desktop-linux" {
			t.Errorf("a call is not addressed to Docker Desktop: %s %v", c.name, c.args)
		}
		line := c.line()
		if strings.Contains(line, "--replace") || strings.Contains(line, "--secret") {
			t.Errorf("Docker was given a Podman-only flag: %s", line)
		}
	}
	checks := map[string]string{
		"--name suiteward-db ":       "--volume suiteward-secrets-suiteward-db:/run/secrets:ro",
		"--name suiteward-migrate ":  "--volume suiteward-secrets-suiteward-migrate:/run/secrets:ro",
		"--name suiteward --network": "--volume suiteward-secrets-suiteward:/run/secrets:ro",
	}
	for marker, want := range checks {
		if line := h.f.find(marker)[0].line(); !strings.Contains(line, want) {
			t.Errorf("%s lacks %q: %s", marker, want, line)
		}
	}
	h.assertNoLeak()
	if s := h.settings(); s.Runtime != RuntimeDocker || s.Connection != "" && s.Connection != "desktop-linux" {
		t.Errorf("saved settings: %+v", s)
	}
}

func TestInstallDockerDoesNotAdoptAndRerunsFromItsSettings(t *testing.T) {
	h := dockerHarness(t, "windows")
	if err := h.docker(); err != nil {
		t.Fatal(err)
	}
	// Second run: every object exists, and the settings file of the first run is there.
	again := dockerHarness(t, "windows")
	again.dir = h.dir
	again.have(KindContainer, ContainerDB, ContainerServe)
	again.have(KindNetwork, NetworkName)
	again.have(KindVolume, VolumeData, VolumeDBData, "suiteward-secrets-suiteward-db", "suiteward-secrets-suiteward-migrate", "suiteward-secrets-suiteward")
	for _, file := range []string{"postgres-password", "database-owner-url", "database-url", "mcp-token"} {
		again.haveDockerSecret(file)
	}
	again.first("cat /s/database-url", DatabaseURL(appRole, "x"))
	again.roleExists = true
	if err := os.WriteFile(again.tokenPath(), []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := again.docker(); err != nil {
		t.Fatal(err)
	}
	for _, c := range again.f.calls {
		if line := normalized(c); strings.HasPrefix(line, "inspect --type container suiteward") && !strings.Contains(line, "--format") {
			t.Errorf("a Docker stack was inspected for adoption: %s", line)
		}
		if strings.Contains(c.line(), "cat > /s/") {
			t.Errorf("a re-run rewrote a secret: %s", c.line())
		}
	}
	if len(again.f.find("--name suiteward-migrate ")) != 1 {
		t.Error("a re-run does not migrate again")
	}
	if len(again.f.find("stop --time 45 suiteward")) == 0 {
		t.Error("the running service was not stopped before it was replaced")
	}
}

func TestInstallDockerRefusesAContainerItDidNotMake(t *testing.T) {
	h := dockerHarness(t, "windows")
	h.have(KindContainer, ContainerServe)
	err := h.docker()
	if err == nil || !strings.Contains(err.Error(), ContainerServe) {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()
}

func TestInstallDockerRefusesPodmansShim(t *testing.T) {
	h := dockerHarness(t, "windows")
	h.first("version --format", podmanBehindDocker)
	if err := h.docker(); !errors.Is(err, ErrNotDocker) {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()
}

func TestInstallDockerDoesNotTakeAPodmanInstallsSettingsAsItsOwn(t *testing.T) {
	h := dockerHarness(t, "windows")
	if err := SaveSettings(h.settingsPath(), DefaultSettings()); err != nil { // the settings of a Podman install
		t.Fatal(err)
	}
	h.have(KindContainer, ContainerServe)
	if err := h.docker(); !errors.Is(err, ErrForeignContainer) {
		t.Fatalf("got %v", err)
	}
	h.mustNotMutate()
}
