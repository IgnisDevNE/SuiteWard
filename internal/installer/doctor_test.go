package installer

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

const (
	okStatus     = `{"version":"v0.2.0","schemaVersion":20,"tier":"co-located","jobs":{"available":0,"completed":4,"discarded":0},"outbox":{"delivered":9,"failed":0}}`
	okCheck      = `[{"name":"github.app","ok":true,"detail":"App 1","fix":""},{"name":"database.schema","ok":true,"detail":"version 20","fix":""}]`
	failingCheck = `[{"name":"github.app","ok":false,"detail":"HTTP 401","fix":"check the private key"},{"name":"database.schema","ok":true,"detail":"","fix":""}]`
	machineList  = `[{"Name":"podman-machine-default","Running":true,"Starting":false,"RemoteUsername":"user"}]`
	stoppedList  = `[{"Name":"podman-machine-default","Running":false,"Starting":false,"RemoteUsername":"user"}]`
)

// serveHTTP answers /healthz, /readyz and /status like the service and returns the port. health is the /healthz body,
// answered with 200.
func serveHTTP(t *testing.T, ready int, status, health string) int {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(health)) })
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(ready) })
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(status)) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil {
		t.Fatal(err)
	}
	return port
}

// baseName is the file name of a command given by a Windows or a POSIX path.
func baseName(path string) string {
	path = path[strings.LastIndexAny(path, `/\`)+1:]
	return strings.TrimSuffix(strings.ToLower(path), ".exe")
}

// readOnlyViolation says why a command is not one of the probes doctor may run, or "" when it is. The allowlist is exact
// verbs: a new kind of command fails here until it is reviewed and added.
func readOnlyViolation(c call) string {
	args := c.args
	switch baseName(c.name) {
	case "podman", "docker":
		for len(args) >= 2 && slices.Contains([]string{"--connection", "--context", "--host"}, args[0]) {
			args = args[2:] // which engine, not what to do
		}
		return engineViolation(args)
	case "systemctl":
		if len(args) >= 2 && args[0] == "--user" && args[1] == "show" {
			return ""
		}
	case "loginctl":
		if len(args) >= 1 && args[0] == "show-user" {
			return ""
		}
	case "id":
		if slices.Equal(args, []string{"-un"}) {
			return ""
		}
	}
	if executorProbeAllowed(baseName(c.name), args) {
		return ""
	}
	return "not an allowed command"
}

// executorProbeAllowed says whether a command is one of the probes of the executor's checks, which only ask: the files of the executor, that are
// world-readable, and the controllers of its manager; `systemctl show` of a system unit; the operating system's own questions about the executor.
func executorProbeAllowed(name string, args []string) bool {
	has := func(prefix ...string) bool {
		return len(args) >= len(prefix) && slices.Equal(args[:len(prefix)], prefix)
	}
	under := func(prefixes ...string) bool {
		return len(args) == 1 && slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(args[0], p) })
	}
	switch name {
	case "systemctl":
		return has("show")
	case "cat":
		return under("/etc/", "/usr/local/libexec/suiteward/", "/sys/fs/cgroup/")
	case "sha256sum":
		return under("/usr/local/libexec/suiteward/")
	case "pgrep", "tasklist":
		return true
	case "powershell": // the listing of the programs that run (the executor's own, by path): a read, and only this script
		return slices.Equal(args, []string{"-NoProfile", "-NonInteractive", "-Command", listProcessesScript})
	case "schtasks":
		return has("/Query")
	case "launchctl":
		return has("print")
	case "id":
		return slices.Equal(args, []string{"-u"}) || has("-u", ServiceAccount) && len(args) == 2 || has("-u", ExecutorAccount) && len(args) == 2
	}
	return false
}

func engineViolation(args []string) string {
	has := func(prefix ...string) bool {
		return len(args) >= len(prefix) && slices.Equal(args[:len(prefix)], prefix)
	}
	switch {
	case has("inspect"), has("image", "inspect"), has("info"), has("version"), has("container", "ls"), has("system", "connection", "list"), has("machine", "list"):
		return ""
	case has("machine", "ssh"): // the last argument is the command run in the machine
		remote := args[len(args)-1]
		if strings.HasPrefix(remote, "systemctl --user show ") || strings.HasPrefix(remote, "curl -fsS -m 5 http://127.0.0.1:") {
			return ""
		}
	case has("exec", "-i"):
		if len(args) >= 6 && args[3] == "/suiteward" && slices.Equal(args[4:], []string{"check", "--json"}) {
			return ""
		}
		if len(args) >= 6 && args[3] == "bash" && args[4] == "-c" {
			return ""
		}
	}
	return "not an allowed verb"
}

// assertReadOnly fails when any command of the fake is not an allowlisted probe, or anything ran on the real terminal.
func assertReadOnly(t *testing.T, f *fakeEngine) {
	t.Helper()
	for _, c := range f.calls {
		if why := readOnlyViolation(c); why != "" {
			t.Errorf("doctor ran a command that is not a read-only probe (%s): %s %v", why, c.name, c.args)
		}
	}
	if len(f.tty) != 0 {
		t.Errorf("doctor used the terminal: %v", f.tty)
	}
}

func TestTheReadOnlyAllowlistRefusesWhatChangesThings(t *testing.T) {
	for _, c := range []call{
		{name: "podman", args: []string{"run", "--rm", "x"}},
		{name: "podman", args: []string{"--connection", "c", "rm", "suiteward"}},
		{name: "podman", args: []string{"container", "stop", "suiteward"}},
		{name: "podman", args: []string{"machine", "start"}},
		{name: "podman", args: []string{"machine", "ssh", "user", "m", "systemctl --user restart suiteward"}},
		{name: "podman", args: []string{"exec", "-i", "suiteward", "/suiteward", "serve"}},
		{name: `C:\Program Files\Docker\Docker\resources\bin\docker.exe`, args: []string{"create", "x"}},
		{name: "systemctl", args: []string{"--user", "restart", "suiteward.service"}},
		{name: "loginctl", args: []string{"enable-linger", "me"}},
		{name: "rm", args: []string{"-rf", "/"}},
	} {
		if readOnlyViolation(c) == "" {
			t.Errorf("%s %v passes the read-only allowlist", c.name, c.args)
		}
	}
}

// runDoctor writes the settings to a temporary file, runs doctor with --json and returns the exit code and results by name.
func runDoctor(t *testing.T, f *fakeEngine, s Settings) (int, map[string]doctorResult, []string) {
	t.Helper()
	code, out := runDoctorRaw(t, f, s, true)
	var list []doctorResult
	if err := json.Unmarshal([]byte(out), &list); err != nil {
		t.Fatalf("doctor --json printed %q: %v", out, err)
	}
	byName := map[string]doctorResult{}
	var names []string
	for _, r := range list {
		if _, dup := byName[r.Name]; dup {
			t.Errorf("%s is reported twice", r.Name)
		}
		byName[r.Name] = r
		names = append(names, r.Name)
	}
	return code, byName, names
}

func runDoctorRaw(t *testing.T, f *fakeEngine, s Settings, jsonOut bool) (int, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	tokenFile, _ := defaultTokenFile(f.deps()) // the tier of s may be the isolated one, which has no use for it
	var out bytes.Buffer
	code := Doctor(context.Background(), f.deps(), path, tokenFile, &out, jsonOut)
	assertReadOnly(t, f)
	return code, out.String()
}

func wantOK(t *testing.T, res map[string]doctorResult, names ...string) {
	t.Helper()
	for _, name := range names {
		if r, ok := res[name]; !ok || !r.OK {
			t.Errorf("%s: %+v, want a passing result", name, r)
		}
	}
}

func wantFail(t *testing.T, res map[string]doctorResult, name string, detail ...string) {
	t.Helper()
	r, ok := res[name]
	if !ok || r.OK {
		t.Errorf("%s: %+v, want a failing result", name, r)
		return
	}
	if strings.TrimSpace(r.Fix) == "" {
		t.Errorf("%s fails without a fix: %+v", name, r)
	}
	for _, text := range detail {
		if !strings.Contains(r.Detail, text) {
			t.Errorf("%s detail %q does not mention %q", name, r.Detail, text)
		}
	}
}

// stateOf is the State of an inspected container.
func doctorStateOf(status string, exit int, health string) map[string]any {
	state := map[string]any{"Status": status, "ExitCode": exit}
	if health != "" {
		state["Health"] = map[string]any{"Status": health}
	}
	return state
}

// podmanVersions is what `podman version --format {{json .}}` prints: the client's version and, when a server answers (a
// Podman machine), the server's.
func podmanVersions(client, server string) string {
	out := fmt.Sprintf(`{"Client":{"Version":%q}`, client)
	if server != "" {
		out += fmt.Sprintf(`,"Server":{"Version":%q}`, server)
	}
	return out + "}"
}

// colocated builds the fake of a healthy co-located stack; every field is a way to break it.
type colocated struct {
	goos     string
	engine   string // RuntimePodman by default
	over     []rule // checked before the healthy answers
	ready    int
	status   string
	health   string                    // the /healthz body
	state    map[string]map[string]any // by container
	edit     func(name string, obj map[string]any)
	migrate  map[string]any // State of a migrate container that exists
	checkOut string
	folder   string // a subfolder of the per-user directory, which the executor's files go in (a name with a percent sign, say)
}

func (c colocated) build(t *testing.T) (*fakeEngine, Settings) {
	t.Helper()
	if c.goos == "" {
		c.goos = "linux"
	}
	if c.ready == 0 {
		c.ready = http.StatusOK
	}
	if c.health == "" {
		c.health = `{"status":"ok"}`
	}
	if c.status == "" {
		c.status = coloStatus(5 * time.Second)
	}
	if c.checkOut == "" {
		c.checkOut = okCheck
	}
	s := colocatedSettings()
	s.Runtime = cmp.Or(c.engine, RuntimePodman)
	s.HTTPPort, s.MCPPort = serveHTTP(t, c.ready, c.status, c.health), DefaultMCPPort
	if s.HTTPPort == s.MCPPort {
		s.MCPPort++
	}
	f := newFake(c.goos)
	f.rules = append(f.rules, c.over...)

	states := map[string]map[string]any{ContainerDB: doctorStateOf("running", 0, "healthy"), ContainerServe: doctorStateOf("running", 0, "")}
	for name, state := range c.state {
		states[name] = state
	}
	stack, err := ColocatedStack(s, testImage)
	if err != nil {
		t.Fatal(err)
	}
	inspect := func(spec RunSpec, state map[string]any) []byte {
		return podmanInspect(t, spec, func(obj map[string]any) {
			obj["State"] = state
			if c.edit != nil {
				c.edit(spec.Name, obj)
			}
		})
	}
	f.on("inspect --type container "+ContainerDB, string(inspect(stack.DB, states[ContainerDB])))
	if c.migrate != nil {
		f.on("name="+ContainerMigrate, ContainerMigrate+"\n")
		f.on("inspect --type container "+ContainerMigrate, string(inspect(stack.Migrate, c.migrate)))
	}
	f.on("inspect --type container "+ContainerServe, string(inspect(stack.Serve, states[ContainerServe])))
	f.on("exec -i "+ContainerServe+" /suiteward check --json", c.checkOut)

	if s.Runtime == RuntimeDocker {
		f.on("version --format", dockerDesktopServer+"\n").on("info --format", `["name=seccomp"]`+"\n")
	} else {
		f.on("info --format", "true\n").on("version --format", podmanVersions("6.1.4", "6.1.4"))
	}
	f.on("podman-restart.service", "LoadState=loaded\nUnitFileState=enabled\n")
	f.on("-un", "suiteward\n").on("show-user suiteward", "Linger=yes\n")
	if c.goos == "windows" || c.goos == "darwin" {
		s.Connection = "podman-machine-default"
		f.on("machine list", machineList)
		f.on("/healthz", c.health).on("/readyz", `{"status":"ready"}`).on("/status", c.status)
	}
	healthyColocatedExecutor(t, f, c.folder)
	return f, s
}

func TestDoctorHealthyColocatedPodman(t *testing.T) {
	f, s := colocated{}.build(t)
	code, res, names := runDoctor(t, f, s)
	if code != 0 {
		t.Errorf("exit %d, results %+v", code, res)
	}
	wantOK(t, res, "settings", "runtime", "containers", "restart", "linger", "pins", "ports", "healthz", "status", "github.app", "database.schema")
	if slices.Contains(names, "machine") {
		t.Error("a machine check on Linux")
	}
	if i, j := slices.Index(names, "settings"), slices.Index(names, "github.app"); i != 0 || j < 0 {
		t.Errorf("settings first and the container results merged, got %v", names)
	}
}

func TestDoctorHealthyColocatedDocker(t *testing.T) {
	f, s := colocated{engine: RuntimeDocker}.build(t)
	code, res, _ := runDoctor(t, f, s)
	if code != 0 {
		t.Errorf("exit %d, results %+v", code, res)
	}
	wantOK(t, res, "runtime", "containers", "restart", "pins", "ports", "healthz", "status", "github.app")
	if !strings.Contains(res["runtime"].Detail, "Docker Desktop") {
		t.Errorf("the runtime result does not identify the engine: %q", res["runtime"].Detail)
	}
	if _, has := res["linger"]; has {
		t.Error("a linger check for Docker, which has its own daemon")
	}
}

const olderPodmanNote = "Podman 4.9.3 is older than 5.0. SuiteWard works with it, but a newer Podman says more clearly when a test is stopped for using too much memory."

// A co-located Podman older than 5.0 is neither a failure nor a warning (Ubuntu 24.04 ships 4.9.3, and a warning there would
// stay forever with nothing to act on): the runtime row passes and says so in its note. The version is the server's, else the client's.
func TestDoctorNotesAColocatedPodmanOlderThan5(t *testing.T) {
	cases := []struct {
		name    string
		goos    string
		answer  string
		noted   bool
		command string // the one version command, as its argument line
	}{
		{"a 4.9.3 server", "linux", podmanVersions("4.9.3", "4.9.3"), true, "version --format {{json .}}"},
		{"a client alone of 4.9.3", "linux", podmanVersions("4.9.3", ""), true, "version --format {{json .}}"},
		{"a machine of 4.9.3 under a client of 6.1.4", "windows", podmanVersions("6.1.4", "4.9.3"), true, "--connection podman-machine-default version --format {{json .}}"},
		{"a machine of 6.1.4 under a client of 4.9.3", "windows", podmanVersions("4.9.3", "6.1.4"), false, "--connection podman-machine-default version --format {{json .}}"},
		{"a 5.0.0 server", "linux", podmanVersions("5.0.0", "5.0.0"), false, "version --format {{json .}}"},
		{"a 6.1.4 server", "linux", podmanVersions("6.1.4", "6.1.4"), false, "version --format {{json .}}"},
		{"a client alone of 6.1.4", "linux", podmanVersions("6.1.4", ""), false, "version --format {{json .}}"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, s := colocated{goos: c.goos, over: []rule{{match: "version --format", out: c.answer}}}.build(t)
			code, res, _ := runDoctor(t, f, s)
			if code != 0 {
				t.Errorf("exit %d, results %+v", code, res)
			}
			wantOK(t, res, "runtime")
			r := res["runtime"]
			if r.needsALook() {
				t.Errorf("the runtime row is a pass that needs no look, so no warning is counted: %+v", r)
			}
			if got := r.Note == olderPodmanNote; got != c.noted {
				t.Errorf("the note is the older-Podman one: %v, want %v: %q", got, c.noted, r.Note)
			}
			if !c.noted && r.Note != "" {
				t.Errorf("a note without a reason: %q", r.Note)
			}
			// On Windows the probe of the Docker pipe asks `docker version` too: only Podman's own command counts here.
			asked := slices.DeleteFunc(f.find("version --format"), func(c call) bool { return c.name != "podman" })
			if len(asked) != 1 || asked[0].line() != c.command {
				t.Errorf("want the one command `podman %s`, got %v", c.command, asked)
			}
		})
	}
}

// olderPodman is the fake of a co-located Podman 4.9.3, which gets the note.
var olderPodman = rule{match: "version --format", out: podmanVersions("4.9.3", "4.9.3")}

// The note of an older Podman is on the line of the passing runtime row, in plain text: `ok runtime: <note>`. It is a pass, so the exit code is 0
// and no WARN line appears, and no other row changes: the output is the one of a newer Podman with that one line replaced.
func TestDoctorPlainOutputShowsThePodmanNoteOnTheRuntimeRow(t *testing.T) {
	f, s := colocated{}.build(t)
	code, newer := runDoctorRaw(t, f, s, false)
	if code != 0 || !strings.Contains(newer, "\nok runtime\n") {
		t.Fatalf("a newer Podman: exit %d, want 0 and the line `ok runtime` in:\n%s", code, newer)
	}
	f, s = colocated{over: []rule{olderPodman}}.build(t)
	code, text := runDoctorRaw(t, f, s, false)
	if code != 0 {
		t.Errorf("exit %d for a Podman 4.9.3, a note is not a failure:\n%s", code, text)
	}
	if want := strings.Replace(newer, "\nok runtime\n", "\nok runtime: "+olderPodmanNote+"\n", 1); text != want {
		t.Errorf("the plain output is\n%s\nwant the output of a newer Podman with the runtime line `ok runtime: <note>`:\n%s", text, want)
	}
	if strings.Contains(text, "WARN") {
		t.Errorf("a note is not a warning:\n%s", text)
	}
}

// On a full screen the note is on the check line of the runtime row, which the scrollback keeps, and the counts treat the row as the pass it is.
func TestDoctorFullScreenShowsThePodmanNoteOnTheRuntimeRow(t *testing.T) {
	run := func(t *testing.T, over ...rule) (code int, tail string) {
		t.Helper()
		f, s := colocated{over: over}.build(t)
		path := filepath.Join(t.TempDir(), "settings.yaml")
		if err := SaveSettings(path, s); err != nil {
			t.Fatal(err)
		}
		c := newTermConsole(&bytes.Buffer{})
		env := Env{Deps: f.deps(), Term: Terminal{In: strings.NewReader(""), Out: &bytes.Buffer{}}, OpenScreen: c.open}
		var stderr bytes.Buffer
		code = Main(liveContext(t), []string{"doctor", "--settings", path, "--token-file", coloTokenFile(t, f)}, env, &stderr)
		if stderr.Len() != 0 {
			t.Errorf("stderr is %q", stderr.String())
		}
		return code, c.tail()
	}
	_, tail := run(t)
	if !strings.Contains("\n"+tail, "\n✓ runtime\n") {
		t.Fatalf("a newer Podman: the check line of the runtime row is missing from:\n%s", tail)
	}
	code, tail := run(t, olderPodman)
	if code != 0 {
		t.Errorf("exit %d for a Podman 4.9.3, a note is not a failure", code)
	}
	if want := "\n✓ runtime: " + olderPodmanNote + "\n"; !strings.Contains("\n"+tail, want) {
		t.Errorf("the scrollback lacks %q:\n%s", want, tail)
	}
	checks := strings.Count("\n"+tail, "\n✓ ")
	if counts := fmt.Sprintf("%d ok, 0 warnings, 0 failures", checks); !strings.HasSuffix(strings.TrimSpace(tail), counts) {
		t.Errorf("the scrollback does not end with %q:\n%s", counts, tail)
	}
}

// `doctor --json` keeps the note apart from the detail, under `note`, and only the runtime row has the key: the rows that
// `suiteward check --json` sends have none, and they still decode and are listed as before.
func TestDoctorJSONCarriesThePodmanNoteUnderItsOwnKey(t *testing.T) {
	rows := func(over ...rule) map[string]map[string]any {
		f, s := colocated{over: over}.build(t)
		_, raw := runDoctorRaw(t, f, s, true)
		var list []map[string]any
		if err := json.Unmarshal([]byte(raw), &list); err != nil {
			t.Fatalf("doctor --json printed %q: %v", raw, err)
		}
		byName := map[string]map[string]any{}
		for _, r := range list {
			byName[r["name"].(string)] = r
		}
		return byName
	}
	older := rows(olderPodman)
	if got := older["runtime"]["note"]; got != olderPodmanNote {
		t.Errorf("the runtime row says note %v, want %q", got, olderPodmanNote)
	}
	if detail, _ := older["runtime"]["detail"].(string); strings.Contains(detail, "older than") {
		t.Errorf("the note is in the detail too: %q", detail)
	}
	if older["runtime"]["ok"] != true {
		t.Errorf("the runtime row is a pass: %v", older["runtime"])
	}
	for name, r := range older {
		if _, has := r["note"]; has && name != "runtime" {
			t.Errorf("the row %s has a note: %v", name, r)
		}
	}
	if r, ok := older["github.app"]; !ok || r["ok"] != true {
		t.Errorf("a row of `suiteward check --json` without a note is lost or changed: %v", r)
	}
	if _, has := rows()["runtime"]["note"]; has {
		t.Error("a newer Podman has a note key")
	}
}

// The version is asked of the connection that NewPodman chose, whoever named it: with no connection in the settings, off Linux that is the one
// rootless machine connection, and anywhere CONTAINER_CONNECTION. The fake answers 4.9.3 on that connection and 6.1.4 on the engine's default.
func TestDoctorAsksThePodmanVersionOfTheConnectionNewPodmanResolved(t *testing.T) {
	cases := []struct {
		name       string
		goos       string
		env        string
		connection string
		over       []rule
	}{
		{"the only rootless machine connection", "windows", "", "podman-machine-default", []rule{{match: "system connection list", out: machineConnections}}},
		{"CONTAINER_CONNECTION", "linux", "edge", "edge", nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			asked := "--connection " + c.connection + " version --format {{json .}}"
			over := slices.Concat(c.over, []rule{
				{match: "=" + asked, out: podmanVersions("4.9.3", "4.9.3")},
				{match: "=version --format {{json .}}", out: podmanVersions("6.1.4", "6.1.4")},
			})
			f, s := colocated{goos: c.goos, over: over}.build(t)
			s.Connection = ""
			f.env["CONTAINER_CONNECTION"] = c.env
			_, text := runDoctorRaw(t, f, s, false)
			if want := "\nok runtime: " + olderPodmanNote + "\n"; !strings.Contains("\n"+text, want) {
				t.Errorf("the output lacks %q, though the connection %s answers 4.9.3:\n%s", want, c.connection, text)
			}
			// On Windows the probe of the Docker pipe asks `docker version` too: only Podman's own command counts here.
			versions := slices.DeleteFunc(f.find("version --format"), func(c call) bool { return c.name != "podman" })
			if len(versions) != 1 || versions[0].line() != asked {
				t.Errorf("want the one command `podman %s`, got %v", asked, versions)
			}
		})
	}
}

// Docker has its own version, which nothing here judges, and the isolated tier has its floor of 5.5 at the host step: neither row changes.
func TestDoctorAddsNoPodmanNoteToDockerOrToTheIsolatedTier(t *testing.T) {
	f, s := colocated{engine: RuntimeDocker}.build(t)
	_, res, _ := runDoctor(t, f, s)
	wantOK(t, res, "runtime")
	if got, want := res["runtime"].Detail, "Docker Desktop 4.50.0 (209931) 29.9.2"; got != want {
		t.Errorf("the Docker runtime row is %q, want %q", got, want)
	}
	if calls := f.find("version --format"); len(calls) != 1 {
		t.Errorf("Docker is asked its version once, to be identified: %v", calls)
	}

	f, s = isolatedRig{}.build(t)
	_, res, _ = runDoctor(t, f, s)
	wantOK(t, res, "runtime")
	if calls := f.find("version --format"); len(calls) != 0 {
		t.Errorf("the isolated tier asks no version: %v", calls)
	}
}

func TestDoctorDockerPinsAreCheckedByImage(t *testing.T) {
	other := "ghcr.io/ignisdevne/suiteward@sha256:" + strings.Repeat("c", 64)
	f, s := colocated{engine: RuntimeDocker, migrate: doctorStateOf("exited", 0, ""), edit: func(name string, obj map[string]any) {
		if name == ContainerMigrate {
			obj["Config"].(map[string]any)["Image"] = other
		}
	}}.build(t)
	_, res, _ := runDoctor(t, f, s)
	wantFail(t, res, "pins", ContainerMigrate)
	wantOK(t, res, "containers")
}

func TestDoctorReportsEachFailureWithItsFix(t *testing.T) {
	driftedDB := func(name string, obj map[string]any) {
		if name == ContainerDB {
			obj["Config"].(map[string]any)["Image"] = "docker.io/library/postgres:99@sha256:" + strings.Repeat("d", 64)
		}
	}
	cases := []struct {
		name  string
		rig   colocated
		check string
		want  []string
	}{
		{"the engine does not answer", colocated{over: []rule{{match: "info --format", err: errors.New("connection refused")}}}, "runtime", []string{"connection refused"}},
		{"a rootful connection", colocated{over: []rule{{match: "info --format", out: "false\n"}}}, "runtime", []string{"rootless"}},
		{"an unhealthy database", colocated{state: map[string]map[string]any{ContainerDB: doctorStateOf("running", 0, "unhealthy")}}, "containers", []string{ContainerDB, "unhealthy"}},
		{"a stopped service", colocated{state: map[string]map[string]any{ContainerServe: doctorStateOf("exited", 137, "")}}, "containers", []string{ContainerServe, "exited"}},
		{"a migrate container that failed", colocated{migrate: doctorStateOf("exited", 3, "")}, "containers", []string{ContainerMigrate, "exit"}},
		{"a migrate container that still runs", colocated{migrate: doctorStateOf("running", 0, "")}, "containers", []string{ContainerMigrate, "running"}},
		{"a missing container", colocated{over: []rule{{match: "inspect --type container " + ContainerDB, err: errors.New("no such container")}}}, "containers", []string{ContainerDB, "no such container"}},
		{"restart through podman-restart.service off", colocated{over: []rule{{match: "podman-restart.service", out: "LoadState=loaded\nUnitFileState=disabled\n"}}}, "restart", []string{"podman-restart.service", "disabled"}},
		{"a user that does not linger", colocated{over: []rule{{match: "show-user", out: "Linger=no\n"}}}, "linger", []string{"linger"}},
		{"a drifted database image", colocated{edit: driftedDB}, "pins", []string{"image"}},
		{"a service that is not ready", colocated{ready: http.StatusServiceUnavailable}, "ports", []string{"/readyz", "503"}},
		{"a health answer that is not ok", colocated{health: `{"status":"degraded"}`}, "healthz", []string{"/healthz", "degraded"}},
		{"a health answer that is not JSON", colocated{health: `<html>`}, "healthz", []string{"/healthz"}},
		{"failed outbox messages", colocated{status: `{"version":"v","schemaVersion":20,"jobs":{"discarded":0},"outbox":{"failed":2}}`}, "status", []string{"outbox", "2"}},
		{"discarded jobs", colocated{status: `{"version":"v","schemaVersion":20,"jobs":{"discarded":1},"outbox":{"failed":0}}`}, "status", []string{"discarded", "1"}},
		{"no schema version", colocated{status: `{"version":"v","jobs":{"discarded":0},"outbox":{"failed":0}}`}, "status", []string{"database version"}},
		{"another tier than the settings", colocated{status: strings.Replace(okStatus, `"tier":"co-located"`, `"tier":"isolated"`, 1)}, "status", []string{"installation type", "isolated", "co-located"}},
		{"no tier", colocated{status: strings.Replace(okStatus, `"tier":"co-located",`, ``, 1)}, "status", []string{"installation type"}},
		{"a status that is not JSON", colocated{status: `<html>`}, "status", []string{"/status"}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, s := c.rig.build(t)
			code, res, _ := runDoctor(t, f, s)
			if code != 1 {
				t.Errorf("exit %d, want 1", code)
			}
			wantFail(t, res, c.check, c.want...)
			for _, after := range []string{"healthz", "status"} {
				if c.check == "ports" && !strings.Contains(res[after].Detail, "not checked") {
					t.Errorf("%s after a failed ports check: %+v", after, res[after])
				}
			}
		})
	}
}

// The fix of a disabled podman-restart.service names what works: in a Podman machine the user's own enable can be denied
// (the unit's directories belong to root), so the link is made as the default ssh user (root on a Windows WSL machine) and the user reloads; on Linux the user enables it.
func TestDoctorRestartFixNamesWhatWorks(t *testing.T) {
	off := []rule{{match: "podman-restart.service", out: "LoadState=loaded\nUnitFileState=disabled\n"}}
	t.Run("in a Podman machine", func(t *testing.T) {
		f, s := colocated{goos: "windows", over: off}.build(t)
		_, res, _ := runDoctor(t, f, s)
		wantFail(t, res, "restart", "podman-restart.service", "disabled")
		for _, want := range []string{
			"podman machine ssh podman-machine-default 'mkdir -p /home/user/.config/systemd/user/default.target.wants && ln -sf /usr/lib/systemd/user/podman-restart.service /home/user/.config/systemd/user/default.target.wants/podman-restart.service'",
			"podman machine ssh --username user podman-machine-default 'systemctl --user daemon-reload && systemctl --user is-enabled podman-restart.service'",
			"as the machine's default ssh user (root on a Windows WSL machine)",
		} {
			if !strings.Contains(res["restart"].Fix, want) {
				t.Errorf("the fix lacks %q:\n%s", want, res["restart"].Fix)
			}
		}
	})
	// A re-run leaves a running, healthy Docker container alone, so `suiteward install` alone would not change its policy: the
	// containers are removed first (the data stays), and then the install creates them as the release defines them.
	t.Run("a Docker container whose restart policy is not always", func(t *testing.T) {
		f, s := colocated{engine: RuntimeDocker, edit: func(name string, obj map[string]any) {
			if name == ContainerDB {
				obj["HostConfig"].(map[string]any)["RestartPolicy"] = map[string]any{"Name": "no"}
			}
		}}.build(t)
		_, res, _ := runDoctor(t, f, s)
		wantFail(t, res, "restart", ContainerDB, `restart policy is "no"`)
		if fix := res["restart"].Fix; !strings.Contains(fix, "suiteward uninstall") || !strings.Contains(fix, "suiteward install") {
			t.Errorf("the fix does not remove the containers before it installs again: %s", fix)
		}
	})
	t.Run("on Linux", func(t *testing.T) {
		f, s := colocated{over: off}.build(t)
		_, res, _ := runDoctor(t, f, s)
		wantFail(t, res, "restart", "podman-restart.service", "disabled")
		if fix := res["restart"].Fix; !strings.Contains(fix, "systemctl --user enable podman-restart.service") || strings.Contains(fix, "ln -sf") {
			t.Errorf("the fix on Linux: %s", fix)
		}
	})
}

func TestDoctorDoesNotCheckWhatNeedsAFailedRuntime(t *testing.T) {
	f, s := colocated{over: []rule{{match: "info --format", err: errors.New("connection refused")}}}.build(t)
	_, res, _ := runDoctor(t, f, s)
	for _, name := range []string{"containers", "restart", "pins", "instance.check"} {
		wantFail(t, res, name, "not checked: runtime")
	}
	wantOK(t, res, "ports", "healthz", "status") // they need only the HTTP port
}

func TestDoctorInstanceCheck(t *testing.T) {
	line := "exec -i " + ContainerServe + " /suiteward check --json"
	t.Run("results of a failing check are merged", func(t *testing.T) {
		// Exit 1 with the JSON on stdout: the contract of `check`, if the runtime hands the output over with the error.
		f, s := colocated{over: []rule{{match: line, out: failingCheck, err: errors.New("exit status 1")}}}.build(t)
		code, res, _ := runDoctor(t, f, s)
		if code != 1 {
			t.Errorf("exit %d, want 1", code)
		}
		wantFail(t, res, "github.app", "HTTP 401")
		wantOK(t, res, "database.schema")
		if _, extra := res["instance.check"]; extra {
			t.Error("an instance.check failure next to the merged results")
		}
	})
	t.Run("exit 2 with the usage on stderr is one failure", func(t *testing.T) {
		hunter := "hunter" + "2" // run-time concatenation keeps gosec from reading a credential in a URL
		stderr := `podman exec -i suiteward /suiteward check --json: exit status 2: suiteward: configuration: SUITEWARD_DATABASE_URL: parse "postgres://suiteward_app:` + hunter + `@db:5432/x": invalid port`
		f, s := colocated{over: []rule{{match: line, err: errors.New(stderr)}}}.build(t)
		code, out := runDoctorRaw(t, f, s, true)
		if code != 1 || strings.Contains(out, hunter) {
			t.Errorf("exit %d, output %s", code, out)
		}
		var list []doctorResult
		if err := json.Unmarshal([]byte(out), &list); err != nil {
			t.Fatal(err)
		}
		res := map[string]doctorResult{}
		for _, r := range list {
			res[r.Name] = r
		}
		wantFail(t, res, "instance.check", "SUITEWARD_DATABASE_URL")
		// Exit 1 reaches doctor as this same error while the runtime drops stdout, so the fix must not blame the image.
		if fix := res["instance.check"].Fix; !strings.Contains(fix, "podman exec suiteward /suiteward check") {
			t.Errorf("the fix does not show how to read the results: %q", fix)
		}
	})
	t.Run("a password with a slash is hidden too", func(t *testing.T) {
		secret := "ab/" + "cd"
		f, s := colocated{over: []rule{{match: line, err: errors.New("exit status 2: parse \"postgres://app:" + secret + "@db/x\": bad")}}}.build(t)
		_, out := runDoctorRaw(t, f, s, true)
		if strings.Contains(out, secret) || strings.Contains(out, "cd@") {
			t.Errorf("the output still has the password: %s", out)
		}
	})
	t.Run("output that is not JSON is one failure", func(t *testing.T) {
		f, s := colocated{checkOut: "usage: suiteward serve\n"}.build(t)
		_, res, _ := runDoctor(t, f, s)
		wantFail(t, res, "instance.check", "readable list")
	})
	t.Run("an empty list is one failure", func(t *testing.T) {
		f, s := colocated{checkOut: "[]"}.build(t)
		_, res, _ := runDoctor(t, f, s)
		wantFail(t, res, "instance.check")
	})
}

func TestDoctorStopsAtMissingOrInvalidSettings(t *testing.T) {
	f := newFake("linux")
	var out bytes.Buffer
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if code := Doctor(context.Background(), f.deps(), path, "", &out, true); code != 1 {
		t.Errorf("exit %d for a missing file", code)
	}
	var list []doctorResult
	if err := json.Unmarshal(out.Bytes(), &list); err != nil || len(list) != 1 || list[0].Name != "settings" || list[0].OK || list[0].Fix == "" {
		t.Errorf("got %s (%v)", out.String(), err)
	}
	if err := os.WriteFile(path, []byte("tier: wrong\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if code := Doctor(context.Background(), f.deps(), path, "", &out, true); code != 1 || !strings.Contains(out.String(), "tier") {
		t.Errorf("exit %d for invalid settings: %s", code, out.String())
	}
	if len(f.calls) != 0 {
		t.Errorf("commands ran without usable settings: %v", f.lines())
	}
}

func TestDoctorOutputFormats(t *testing.T) {
	f, s := colocated{ready: http.StatusServiceUnavailable}.build(t)
	code, text := runDoctorRaw(t, f, s, false)
	if code != 1 {
		t.Errorf("exit %d", code)
	}
	lines := strings.Split(strings.TrimSpace(text), "\n")
	for _, l := range lines {
		if strings.HasPrefix(l, "ok ") && strings.Contains(l, ":") {
			t.Errorf("an ok line carries a detail, which `check` does not print: %q", l)
		}
	}
	if lines[0] != "ok settings" {
		t.Errorf("first line %q", lines[0])
	}
	var failed string
	for _, l := range lines {
		if strings.HasPrefix(l, "FAIL ports: ") {
			failed = l
		}
		if !strings.HasPrefix(l, "ok ") && !strings.HasPrefix(l, "FAIL ") {
			t.Errorf("line %q is neither ok nor FAIL", l)
		}
	}
	if !strings.Contains(failed, " — ") {
		t.Errorf("a failure reads %q, want `FAIL name: detail — fix`", failed)
	}

	f, s = colocated{}.build(t)
	_, raw := runDoctorRaw(t, f, s, true)
	var list []map[string]any
	if err := json.Unmarshal([]byte(raw), &list); err != nil {
		t.Fatal(err)
	}
	for _, r := range list {
		for _, key := range []string{"name", "ok", "detail", "fix"} {
			if _, has := r[key]; !has {
				t.Errorf("result %v lacks %q", r, key)
			}
		}
	}
}

// ---- Podman machine (Windows and macOS) ----

func TestDoctorPodmanMachineReachesThePortsAndTheRestartServiceThroughSSH(t *testing.T) {
	f, s := colocated{goos: "windows"}.build(t)
	code, res, _ := runDoctor(t, f, s)
	if code != 0 {
		t.Errorf("exit %d, results %+v", code, res)
	}
	wantOK(t, res, "machine", "runtime", "restart", "ports", "healthz", "status")
	for _, want := range []string{"/healthz", "/readyz", "/status", "podman-restart.service"} {
		calls := f.find(want)
		if len(calls) == 0 || calls[0].name != "podman" || !strings.HasPrefix(calls[0].line(), "machine ssh --username user podman-machine-default ") {
			t.Errorf("%s is not reached through `podman machine ssh --username user <machine>`: %v", want, calls)
		}
	}
	if got := f.find("http://127.0.0.1:" + strconv.Itoa(s.HTTPPort) + "/readyz"); len(got) == 0 {
		t.Errorf("the ssh curl does not use the settings' HTTP port %d: %v", s.HTTPPort, f.lines())
	}
}

func TestDoctorStoppedMachine(t *testing.T) {
	f, s := colocated{goos: "windows", over: []rule{{match: "machine list", out: stoppedList}}}.build(t)
	code, res, _ := runDoctor(t, f, s)
	if code != 1 {
		t.Errorf("exit %d", code)
	}
	wantFail(t, res, "machine", "podman-machine-default", "not running")
	if !strings.Contains(res["machine"].Fix, "podman machine start") {
		t.Errorf("fix %q", res["machine"].Fix)
	}
	for _, name := range []string{"runtime", "containers", "restart", "pins", "ports", "instance.check"} {
		wantFail(t, res, name, "not checked: machine")
	}
	if len(f.find("info --format")) != 0 {
		t.Error("the runtime was probed with a stopped machine")
	}
}

func TestDoctorMachineMustExist(t *testing.T) {
	f, s := colocated{goos: "windows", over: []rule{{match: "machine list", out: `[]`}}}.build(t)
	_, res, _ := runDoctor(t, f, s)
	wantFail(t, res, "machine", "podman-machine-default")
}

func TestDoctorSaysWhoOwnsTheDockerPipeOnWindows(t *testing.T) {
	const pipe = "--host npipe:////./pipe/docker_engine"
	t.Run("another engine", func(t *testing.T) {
		f, s := colocated{goos: "windows", over: []rule{{match: pipe, out: podmanBehindDocker + "\n"}}}.build(t)
		_, res, _ := runDoctor(t, f, s)
		wantOK(t, res, "runtime")
		if d := res["runtime"].Detail; !strings.Contains(d, "docker_engine") || !strings.Contains(strings.ToLower(d), "not docker") {
			t.Errorf("the runtime result does not say who owns the Docker pipe: %q", d)
		}
	})
	t.Run("asks Docker Desktop's own CLI, not the docker command of the PATH", func(t *testing.T) {
		f, s := colocated{goos: "windows", over: []rule{{match: pipe + " info", out: `["name=seccomp"]` + "\n"}, {match: pipe, out: dockerDesktopServer + "\n"}}}.build(t)
		_, res, _ := runDoctor(t, f, s)
		if d := res["runtime"].Detail; !strings.Contains(d, "docker_engine") || !strings.Contains(d, "Docker Desktop") {
			t.Errorf("the runtime result does not name Docker Desktop as the pipe's owner: %q", d)
		}
		calls := f.find(pipe)
		if len(calls) == 0 || calls[0].name != dockerDesktopCLI {
			t.Errorf("the pipe was asked through %v, want Docker Desktop's CLI %q", calls, dockerDesktopCLI)
		}
	})
	// Information only: an unanswered pipe is no failure.
	for name, err := range map[string]error{"nothing answers": errors.New("pipe not found"), "no Docker CLI": fmt.Errorf("start docker: %w", exec.ErrNotFound)} {
		t.Run(name, func(t *testing.T) {
			f, s := colocated{goos: "windows", over: []rule{{match: pipe, err: err}}}.build(t)
			code, res, _ := runDoctor(t, f, s)
			if code != 0 {
				t.Errorf("exit %d, an unanswered pipe failed doctor: %+v", code, res)
			}
			if d := res["runtime"].Detail; name == "no Docker CLI" && !strings.Contains(d, "no Docker program") {
				t.Errorf("a missing CLI is not told apart from a silent pipe: %q", d)
			}
		})
	}
}

// ---- isolated tier ----

type isolatedRig struct {
	over       []rule
	files      func(units map[string][]byte)
	noHome     bool
	serveImage string // what the running service container reports as its image
	dbImage    string // and the database container

	// The executor (doctor_executor_test.go): by default a healthy one, which the service saw 5 seconds ago.
	lastSeen     time.Duration // how long ago the service saw an executor
	executionOff bool          // the service has no executor token
	noExecution  bool          // /status has no execution object: the service is older than M2.1
}

// taglessPostgres is the pinned database image as Podman reports it: repository and digest, the tag dropped.
func taglessPostgres(t *testing.T) string {
	t.Helper()
	pinned, err := PostgresImage()
	if err != nil {
		t.Fatal(err)
	}
	repo, digest, _ := strings.Cut(pinned, "@")
	return repo[:strings.LastIndex(repo, ":")] + "@" + digest
}

func unitShow(active, sub, fileState string) string {
	return fmt.Sprintf("LoadState=loaded\nActiveState=%s\nSubState=%s\nResult=success\nUnitFileState=%s\nWantedBy=default.target\n", active, sub, fileState)
}

func (r isolatedRig) build(t *testing.T) (*fakeEngine, Settings) {
	t.Helper()
	s := DefaultSettings()
	s.Tier = config.TierIsolated
	status := isolatedStatus(cmp.Or(r.lastSeen, 5*time.Second))
	switch {
	case r.noExecution:
		status = strings.Replace(okStatus, "co-located", "isolated", 1)
	case r.executionOff:
		status = isolatedStatusWith(`{"enabled":false,"jobs":{"pending":0},"lastSeenAt":"2026-10-27T00:00:00Z"}`)
	}
	s.HTTPPort, s.MCPPort = serveHTTP(t, http.StatusOK, status, `{"status":"ok"}`), DefaultMCPPort
	if s.HTTPPort == s.MCPPort {
		s.MCPPort++
	}
	units, err := RenderUnits(UnitParams{Digest: testDigest, Tier: config.TierIsolated, HTTPPort: s.HTTPPort, MCPPort: s.MCPPort})
	if err != nil {
		t.Fatal(err)
	}
	if r.files != nil {
		r.files(units)
	}
	f := newFake("linux")
	if !r.noHome {
		home := t.TempDir()
		f.env["HOME"] = home
		if _, err := WriteUnits(filepath.Join(home, ".config", "containers", "systemd"), units); err != nil {
			t.Fatal(err)
		}
	}
	f.rules = append(f.rules, r.over...)
	f.on("suiteward-db.service", unitShow("active", "running", "generated"))
	f.on("suiteward-migrate.service", unitShow("active", "exited", "generated"))
	f.on("suiteward.service", unitShow("active", "running", "generated"))
	f.on("-un", "suiteward\n").on("show-user suiteward", "Linger=yes\n")
	f.on("exec -i "+ContainerDB+" bash", "blocked\n")
	f.on("exec -i "+ContainerServe+" /suiteward check --json", okCheck)
	f.on("NetworkBackend", "netavark\n")
	f.on("inspect --type container "+ContainerDB, fmt.Sprintf(`[{"Config":{"Image":%q},"HostConfig":{"Memory":402653184}}]`, cmp.Or(r.dbImage, taglessPostgres(t))))
	f.on("inspect --type container "+ContainerServe, fmt.Sprintf(`[{"Config":{"Image":%q}}]`, cmp.Or(r.serveImage, testImage)))
	f.on("info --format", "true\n")
	f.rules = append(f.rules, healthyIsolatedExecutor(t, s)...)
	return f, s
}

func TestDoctorHealthyIsolated(t *testing.T) {
	f, s := isolatedRig{}.build(t)
	code, res, _ := runDoctor(t, f, s)
	if code != 0 {
		t.Errorf("exit %d, results %+v", code, res)
	}
	wantOK(t, res, "runtime", "units", "linger", "restart", "pins", "ports", "healthz", "status", "metadata", "network", "memory", "github.app", "executor.account", "executor.running")
	if d := res["pins"].Detail; !strings.Contains(d, "match this release") || strings.Contains(d, "equals this release") {
		t.Errorf("the pins detail overstates what was compared: %q", d)
	}
	if _, has := res["containers"]; has {
		t.Error("a containers result for the isolated tier, whose containers are units")
	}
}

func TestDoctorIsolatedFailures(t *testing.T) {
	driftedUnit := func(units map[string][]byte) {
		units["suiteward-migrate.container"] = bytes.Replace(units["suiteward-migrate.container"], []byte("Type=oneshot"), []byte("Type=simple"), 1)
	}
	cases := []struct {
		name  string
		rig   isolatedRig
		check string
		want  []string
		fix   string
	}{
		{"the service is down", isolatedRig{over: []rule{{match: "suiteward.service", out: unitShow("inactive", "dead", "generated")}}}, "units", []string{"suiteward.service", "inactive"}, ""},
		{"migrate failed", isolatedRig{over: []rule{{match: "suiteward-migrate.service", out: unitShow("failed", "failed", "generated")}}}, "units", []string{"suiteward-migrate.service", "failed"}, ""},
		{"migrate is not finished", isolatedRig{over: []rule{{match: "suiteward-migrate.service", out: unitShow("active", "running", "generated")}}}, "units", []string{"suiteward-migrate.service", "running"}, ""},
		{"the user does not linger", isolatedRig{over: []rule{{match: "show-user", out: "Linger=no\n"}}}, "linger", []string{"linger"}, "loginctl enable-linger"},
		{"a unit is not enabled", isolatedRig{over: []rule{{match: "suiteward-db.service", out: unitShow("active", "running", "disabled")}}}, "restart", []string{"suiteward-db.service", "disabled"}, ""},
		{"a drifted unit file", isolatedRig{files: driftedUnit}, "pins", []string{"Type="}, ""},
		{"no unit directory", isolatedRig{noHome: true}, "pins", []string{"unit"}, ""},
		{"the service runs another digest than its unit", isolatedRig{serveImage: "ghcr.io/ignisdevne/suiteward@sha256:" + strings.Repeat("e", 64)}, "pins", []string{ContainerServe, "unit"}, ""},
		{"the database runs another image than its unit", isolatedRig{dbImage: "docker.io/library/postgres:99@sha256:" + strings.Repeat("f", 64)}, "pins", []string{ContainerDB, "unit"}, ""},
		{"another network backend", isolatedRig{over: []rule{{match: "NetworkBackend", out: "cni\n"}}}, "network", []string{"cni", "netavark"}, "netavark"},
		{"the network backend cannot be read", isolatedRig{over: []rule{{match: "NetworkBackend", err: errors.New("podman: not found")}}}, "network", []string{"not found"}, ""},
		{"another memory limit on the database", isolatedRig{over: []rule{{match: "inspect --type container " + ContainerDB, out: `[{"HostConfig":{"Memory":0}}]`}}}, "memory", []string{"402653184", "0"}, ""},
		{"the metadata address answers", isolatedRig{over: []rule{{match: "exec -i " + ContainerDB + " bash", out: "reachable\n"}}}, "metadata", []string{"169.254.169.254"}, "--metadata-rule yes"},
		{"the control connection fails", isolatedRig{over: []rule{{match: "exec -i " + ContainerDB + " bash", out: "control-failed\n"}}}, "metadata", []string{"database's own port"}, ""},
		{"an answer nobody expected", isolatedRig{over: []rule{{match: "exec -i " + ContainerDB + " bash", out: "maybe\n"}}}, "metadata", []string{"maybe"}, ""},
		{"the metadata probe cannot run", isolatedRig{over: []rule{{match: "exec -i " + ContainerDB + " bash", err: errors.New("container is not running")}}}, "metadata", []string{"not running"}, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f, s := c.rig.build(t)
			code, res, _ := runDoctor(t, f, s)
			if code != 1 {
				t.Errorf("exit %d, want 1", code)
			}
			wantFail(t, res, c.check, c.want...)
			if !strings.Contains(res[c.check].Fix, c.fix) {
				t.Errorf("fix %q does not mention %q", res[c.check].Fix, c.fix)
			}
		})
	}
}

func TestDoctorMetadataProbeHasAPositiveControl(t *testing.T) {
	f, s := isolatedRig{}.build(t)
	runDoctor(t, f, s)
	calls := f.find("exec -i " + ContainerDB + " bash")
	if len(calls) != 1 {
		t.Fatalf("the probe ran %d times", len(calls))
	}
	script := calls[0].args[len(calls[0].args)-1]
	control, probe := strings.Index(script, "/dev/tcp/127.0.0.1/5432"), strings.Index(script, "/dev/tcp/169.254.169.254/80")
	if control < 0 || probe < 0 || control > probe || !strings.Contains(script, "control-failed") {
		t.Errorf("the script does not check the database port before it probes the metadata address: %s", script)
	}
}

func TestDoctorAcceptsQuadletsThatSystemdEnabledThroughInstall(t *testing.T) {
	f, s := isolatedRig{over: []rule{{match: "suiteward.service", out: unitShow("active", "running", "enabled")}}}.build(t)
	_, res, _ := runDoctor(t, f, s)
	wantOK(t, res, "restart")
}

func TestDoctorNeverPrintsEnvironmentValuesOfADiff(t *testing.T) {
	password, token := "pw-"+"3f9a0c", "tok-"+"77e1d2"
	secrets := []string{password, token}
	assertHidden := func(t *testing.T, out string, names ...string) {
		t.Helper()
		for _, secret := range secrets {
			if strings.Contains(out, secret) {
				t.Errorf("the output has the value %q: %s", secret, out)
			}
		}
		for _, name := range names {
			if !strings.Contains(out, name) {
				t.Errorf("the output does not name %s: %s", name, out)
			}
		}
	}
	t.Run("containers", func(t *testing.T) {
		f, s := colocated{edit: func(name string, obj map[string]any) {
			if name == ContainerServe {
				cfg := obj["Config"].(map[string]any)
				cfg["Env"] = append(cfg["Env"].([]string), "POSTGRES_PASSWORD="+password, "SUITEWARD_MCP_TOKEN="+token)
			}
		}}.build(t)
		_, out := runDoctorRaw(t, f, s, true)
		assertHidden(t, out, "POSTGRES_PASSWORD", "SUITEWARD_MCP_TOKEN")
		if !strings.Contains(out, `"name":"pins","ok":false`) {
			t.Errorf("the planted variables are not a pins failure: %s", out)
		}
	})
	t.Run("units", func(t *testing.T) {
		f, s := isolatedRig{files: func(units map[string][]byte) {
			units["suiteward.container"] = bytes.Replace(units["suiteward.container"], []byte("[Service]"),
				[]byte("Environment=SUITEWARD_MCP_TOKEN="+token+"\n\n[Service]"), 1)
		}}.build(t)
		_, out := runDoctorRaw(t, f, s, true)
		assertHidden(t, out, "SUITEWARD_MCP_TOKEN")
		if !strings.Contains(out, `"name":"pins","ok":false`) {
			t.Errorf("the planted line is not a pins failure: %s", out)
		}
	})
	t.Run("the message of a DiffError", func(t *testing.T) {
		e := &DiffError{Diffs: []Diff{
			{Source: ContainerServe, Field: "environment", Want: "SUITEWARD_INSTALLATION_TIER=co-located", Got: "POSTGRES_PASSWORD=" + password + ", SUITEWARD_MCP_TOKEN=" + token},
			{Source: "suiteward.container", Field: "[Service]", Got: "Environment=SUITEWARD_MCP_TOKEN=" + token},
			{Source: "suiteward.container", Field: "[Container]", Want: "ReadOnly=true", Got: "ReadOnly=false"},
		}}
		assertHidden(t, e.Error(), "POSTGRES_PASSWORD", "SUITEWARD_MCP_TOKEN")
		if !strings.Contains(e.Error(), "ReadOnly=false") {
			t.Errorf("a line that is not an environment value is hidden too: %s", e.Error())
		}
	})
}

func TestDoctorBoundsEveryQuery(t *testing.T) {
	old := queryTimeout
	queryTimeout = 50 * time.Millisecond
	t.Cleanup(func() { queryTimeout = old })
	f, s := colocated{goos: "windows"}.build(t)
	deps := f.deps()
	inner := deps.Exec
	deps.Exec = func(ctx context.Context, name string, args []string, stdin io.Reader) ([]byte, error) {
		if slices.Contains(args, "list") && slices.Contains(args, "machine") {
			<-ctx.Done() // a Podman machine that never answers
			return nil, ctx.Err()
		}
		return inner(ctx, name, args, stdin)
	}
	path := filepath.Join(t.TempDir(), "settings.yaml")
	if err := SaveSettings(path, s); err != nil {
		t.Fatal(err)
	}
	done := make(chan string)
	go func() {
		var out bytes.Buffer
		Doctor(context.Background(), deps, path, "", &out, true)
		done <- out.String()
	}()
	select {
	case out := <-done:
		if !strings.Contains(out, `"name":"machine","ok":false`) {
			t.Errorf("a hung query is not a machine failure: %s", out)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("doctor did not return: a query has no deadline")
	}
}

// An instance installed without GitHub is a valid install: its `check` reports GitHub as not configured, and doctor shows
// those results as warnings, not failures.
func TestDoctorShowsAnInstanceWithoutGitHubAsWarnings(t *testing.T) {
	const noGitHub = `[{"name":"github.app","ok":true,"warning":true,"detail":"not configured: GitHub is off in this instance","fix":"set GitHub up"},` +
		`{"name":"owner.enrolled","ok":true,"warning":true,"detail":"no owner is enrolled","fix":"set GitHub up first"},` +
		`{"name":"database.schema","ok":true,"detail":"schema version 20","fix":""}]`
	f, s := colocated{checkOut: noGitHub}.build(t)
	code, res, _ := runDoctor(t, f, s)
	if code != 0 {
		t.Errorf("exit %d: a warning is not a failure, results %+v", code, res)
	}
	for _, name := range []string{"github.app", "owner.enrolled"} {
		if r := res[name]; !r.OK || !r.Warning {
			t.Errorf("%s = %+v, want a passing warning", name, r)
		}
	}
	if r := res["database.schema"]; !r.OK || r.Warning {
		t.Errorf("database.schema = %+v, want a plain pass", r)
	}
	if _, failed := res["instance.check"]; failed {
		t.Error("instance.check failed although check printed its results")
	}

	f, s = colocated{checkOut: noGitHub}.build(t)
	_, text := runDoctorRaw(t, f, s, false)
	for _, want := range []string{"WARN github.app: not configured: GitHub is off in this instance — set GitHub up\n", "WARN owner.enrolled: no owner is enrolled — set GitHub up first\n", "ok database.schema\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("doctor's output lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "FAIL") {
		t.Errorf("doctor printed a failure:\n%s", text)
	}
}
