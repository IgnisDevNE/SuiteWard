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
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// Doctor is the host half of `suiteward doctor`:
//
//	func Doctor(ctx context.Context, deps Deps, settingsPath string, out io.Writer, jsonOut bool) (exitCode int)
//
// It reads the settings at settingsPath (the default location when empty), checks the engine, the containers or units, the
// pins, the ports and /status of the instance those settings describe, runs `suiteward check --json` in the service
// container and merges its results. It prints `ok <name>` or `FAIL <name>: <detail> — <fix>` per result (the same style as
// `check`), or with jsonOut the array of {name, ok, detail, fix}, which also carries the detail of the passing checks. The
// exit code is 0 when everything passed and 1 otherwise. Doctor only reads: it never creates, starts, stops or removes anything.
func Doctor(ctx context.Context, deps Deps, settingsPath string, out io.Writer, jsonOut bool) int {
	d := &doctor{deps: deps, raw: map[string][]byte{}, units: map[string]map[string]string{}}
	if err := deps.validate(); err != nil {
		d.fail("settings", err.Error(), "this is a bug of the installer: report it")
	} else {
		d.deps.Exec = boundExec(deps.Exec)
		d.run(ctx, settingsPath)
	}
	if err := writeDoctor(out, d.results, jsonOut); err != nil {
		return 1 // a failed write to the output cannot be reported anywhere else
	}
	if slices.ContainsFunc(d.results, func(r doctorResult) bool { return !r.OK }) {
		return 1
	}
	return 0
}

// doctorResult is one line of `suiteward doctor`: the JSON keys are those of `suiteward check --json`, whose results are
// merged into the same list.
type doctorResult struct {
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	Detail string `json:"detail"`
	Fix    string `json:"fix"`
}

func writeDoctor(w io.Writer, results []doctorResult, jsonOut bool) error {
	if jsonOut {
		return json.NewEncoder(w).Encode(results)
	}
	var b strings.Builder
	for _, r := range results {
		switch {
		case r.OK:
			b.WriteString("ok " + r.Name + "\n")
		default:
			b.WriteString("FAIL " + r.Name + ": " + r.Detail + " — " + r.Fix + "\n")
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

const (
	dockerPipe     = "npipe:////./pipe/docker_engine"
	defaultMachine = "podman-machine-default"
	machineUser    = "user" // the rootless user inside a Podman machine; ssh defaults to root, whose store is rootful
	probeTimeout   = 5 * time.Second
	metadataFix    = "refuse 169.254.169.254 for the service account (docs/install.md, step 10.5), then run doctor again"
	// Connects to the instance metadata address from the database container, which sits on the service network. The probe
	// answers on its own output, so that an exec that fails is not read as "blocked", and first makes the same kind of
	// connection to the database's own port: when that fails the probe would be "blocked" for any reason.
	metadataProbe = `timeout 3 bash -c 'exec 3<>/dev/tcp/127.0.0.1/5432' 2>/dev/null || { echo control-failed; exit 0; }; ` +
		`timeout 3 bash -c 'exec 3<>/dev/tcp/169.254.169.254/80' 2>/dev/null && echo reachable || echo blocked`
)

// The units of the isolated tier and the state each must be in.
var isolatedUnits = []struct{ name, active, sub string }{
	{"suiteward-db.service", "active", "running"},
	{"suiteward-migrate.service", "active", "exited"}, // a one-shot that stays active after it exited 0
	{"suiteward.service", "active", "running"},
}

// secretInURL hides the credentials of a URL that an error quoted.
var secretInURL = regexp.MustCompile(`://[^\s"]*@`) // up to the last @, so a password with a slash is hidden too

// oneLine makes a command error fit one line of a result: no credentials of a quoted URL, no line breaks, bounded.
func oneLine(text string) string {
	text = secretInURL.ReplaceAllString(strings.Join(strings.Fields(text), " "), "://***@")
	if r := []rune(text); len(r) > 300 {
		text = string(r[:300]) + "..."
	}
	return text
}

type machineInfo struct{ Name, User string }

// machineEntry is what `podman machine list --format json` says of one machine.
type machineEntry struct {
	Name           string
	Running        bool
	Starting       bool
	RemoteUsername string
}

type doctor struct {
	deps    Deps
	s       Settings
	rt      Runtime
	why     string // the check that failed when rt is nil
	machine machineInfo
	viaSSH  bool // co-located Podman off Linux: the instance lives in a Podman machine
	raw     map[string][]byte
	units   map[string]map[string]string // isolated: `systemctl show` of each unit
	results []doctorResult
}

func (d *doctor) pass(name, detail string) {
	d.results = append(d.results, doctorResult{Name: name, OK: true, Detail: detail})
}

func (d *doctor) fail(name, detail, fix string) {
	d.results = append(d.results, doctorResult{Name: name, Detail: oneLine(detail), Fix: fix})
}

// skip reports a check that needs another one to pass first.
func (d *doctor) skip(name, dependency string) {
	d.fail(name, "not checked: "+dependency+" failed", "fix "+dependency+" first")
}

func (d *doctor) needRuntime(name string) bool {
	if d.rt == nil {
		d.skip(name, d.why)
	}
	return d.rt != nil
}

func (d *doctor) needMachine(name string) bool {
	if d.viaSSH && d.machine.Name == "" {
		d.skip(name, "machine")
		return false
	}
	return true
}

func (d *doctor) run(ctx context.Context, settingsPath string) {
	if settingsPath == "" {
		var err error
		if settingsPath, err = DefaultSettingsPath(); err != nil {
			d.fail("settings", err.Error(), "pass the settings file with --settings PATH")
			return
		}
	}
	s, err := LoadSettings(settingsPath)
	if err != nil {
		fix := "fix the file named above, or write it again with `suiteward install`"
		if errors.Is(err, ErrNoSettings) {
			fix = "run `suiteward install` first: it writes the settings file doctor reads"
		}
		d.fail("settings", err.Error(), fix)
		return
	}
	d.s = s
	d.pass("settings", settingsPath)
	isolated := s.Tier == config.TierIsolated
	d.viaSSH = !isolated && s.Runtime == RuntimePodman && d.deps.GOOS != "linux"

	if d.viaSSH {
		d.checkMachine(ctx)
	}
	if d.viaSSH && d.machine.Name == "" {
		d.why = "machine"
		d.skip("runtime", "machine")
	} else {
		d.checkRuntime(ctx)
	}
	if isolated {
		d.checkUnits(ctx)
		d.checkLinger(ctx)
		d.checkUnitsEnabled()
	} else {
		d.checkContainers(ctx)
		d.checkRestart(ctx)
		if s.Runtime == RuntimePodman && d.deps.GOOS == "linux" {
			d.checkLinger(ctx) // rootless Podman restarts the containers at boot only while the user's manager runs
		}
	}
	d.checkPins(ctx)
	d.checkPorts(ctx)
	if isolated {
		d.checkHost(ctx)
		d.checkMetadata(ctx)
	}
	d.checkInstance(ctx)
}

// ---- machine and runtime ----

func (d *doctor) checkMachine(ctx context.Context) {
	out, err := d.deps.Exec(ctx, "podman", []string{"machine", "list", "--format", "json"}, nil)
	if err != nil {
		d.fail("machine", "list the Podman machines: "+err.Error(), "check that Podman is installed and on the PATH")
		return
	}
	var list []machineEntry
	if err := json.Unmarshal(out, &list); err != nil {
		d.fail("machine", "read the Podman machines: "+err.Error(), "check that `podman machine list --format json` works")
		return
	}
	want := d.s.Connection
	if want == "" {
		want = d.deps.Getenv("CONTAINER_CONNECTION")
	}
	want = strings.TrimSuffix(want, "-root") // the rootless connection is named after the machine, the rootful one adds -root
	pick := slices.IndexFunc(list, func(m machineEntry) bool {
		return m.Name == want || (want == "" && m.Running)
	})
	if pick < 0 && want == "" && len(list) > 0 {
		pick = 0
	}
	if pick < 0 {
		what := "no Podman machine exists"
		if want != "" {
			what = "no Podman machine is called " + want
		}
		d.fail("machine", what, "create and start it with `podman machine init` and `podman machine start`, or select the right machine with connection: in the settings")
		return
	}
	m := list[pick]
	start := "podman machine start"
	if m.Name != defaultMachine {
		start += " " + m.Name
	}
	switch {
	case m.Starting:
		d.fail("machine", "the Podman machine "+m.Name+" is still starting", "wait until it runs, then run doctor again")
	case !m.Running:
		d.fail("machine", "the Podman machine "+m.Name+" is not running", start+" (starting it at logon is not part of the installer)")
	default:
		d.machine = machineInfo{Name: m.Name, User: cmp.Or(m.RemoteUsername, machineUser)}
		d.pass("machine", m.Name+" is running")
	}
}

// machineSSH runs one command in the Podman machine as its rootless user.
func (d *doctor) machineSSH(ctx context.Context, command string) ([]byte, error) {
	return d.deps.Exec(ctx, "podman", []string{"machine", "ssh", "--username", d.machine.User, d.machine.Name, command}, nil)
}

func (d *doctor) checkRuntime(ctx context.Context) {
	var rt Runtime
	var err error
	if d.s.Runtime == RuntimeDocker {
		rt, err = NewDocker(ctx, d.deps, DockerOptions{Context: d.s.Connection})
	} else {
		rt, err = NewPodman(ctx, d.deps, d.s.Connection)
	}
	if err != nil {
		d.why = "runtime"
		d.fail("runtime", err.Error(), d.runtimeFix(err))
		return
	}
	d.rt = rt
	e := rt.Engine()
	detail := strings.TrimSpace(e.Name + " " + e.Version)
	if e.Rootless {
		detail += ", rootless"
	}
	if d.s.Connection != "" {
		detail += ", connection " + d.s.Connection
	}
	if d.deps.GOOS == "windows" {
		detail += "; the Docker pipe docker_engine is served by " + d.pipeOwner(ctx)
	}
	d.pass("runtime", detail)
}

func (d *doctor) runtimeFix(err error) string {
	switch {
	case errors.Is(err, ErrRootful):
		return "select the machine's rootless connection with connection: in the settings file"
	case errors.Is(err, ErrNotDocker):
		return "set connection: in the settings to Docker's own context, or install with --runtime podman"
	case errors.Is(err, ErrNoConnection):
		return "set connection: in the settings file to the machine's rootless connection"
	case d.s.Runtime == RuntimeDocker:
		return "start Docker (Docker Desktop on Windows and macOS) and check that `docker version` answers"
	case d.deps.GOOS != "linux":
		return "check the Podman machine with `podman machine list`, and start it with `podman machine start`"
	default:
		return "check that `podman info` works for this user"
	}
}

// pipeOwner says which engine answers on the default Docker pipe; whichever engine started first owns it (spike S2). It is
// information: the installer addresses its engine by its own endpoint and never uses the pipe.
func (d *doctor) pipeOwner(ctx context.Context) string {
	// Docker Desktop's CLI by its path, as NewDocker does: the docker command of the PATH may be Podman's shim, which cannot
	// address a named pipe.
	programFiles := d.deps.Getenv("ProgramFiles")
	if programFiles == "" {
		programFiles = `C:\Program Files`
	}
	dk := &Docker{cli: cli{bin: programFiles + dockerDesktopRelativeCLI, base: []string{"--host", dockerPipe}, d: d.deps}}
	engine, err := dk.identify(ctx)
	switch {
	case err == nil:
		return strings.TrimSpace(engine.Name + " " + engine.Version)
	case errors.Is(err, ErrNotDocker):
		return "an engine that is not Docker, normally the Podman machine"
	case errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist):
		return "unknown: there is no Docker CLI to ask"
	default:
		return "nothing that answers"
	}
}

// ---- containers (co-located) ----

// containerStatus is what `inspect` reports of a container's State, on Podman and Docker alike.
type containerStatus struct {
	State struct {
		Status   string
		ExitCode int
		Health   *struct{ Status string }
	}
}

func parseContainerStatus(name string, raw []byte) (containerStatus, error) {
	var list []containerStatus
	if err := json.Unmarshal(bytes.TrimSpace(raw), &list); err != nil || len(list) != 1 {
		return containerStatus{}, fmt.Errorf("read the state of %s from its inspect output", name)
	}
	return list[0], nil
}

func (d *doctor) checkContainers(ctx context.Context) {
	if !d.needRuntime("containers") {
		return
	}
	var problems, states []string
	for _, name := range []string{ContainerDB, ContainerServe} {
		raw, err := d.rt.Inspect(ctx, name)
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		d.raw[name] = raw
		st, err := parseContainerStatus(name, raw)
		switch {
		case err != nil:
			problems = append(problems, err.Error())
		case st.State.Status != "running":
			problems = append(problems, name+" is "+st.State.Status)
		case name == ContainerDB && (st.State.Health == nil || st.State.Health.Status != "healthy"):
			health := "has no health check"
			if st.State.Health != nil {
				health = st.State.Health.Status
			}
			problems = append(problems, name+" is running but "+health)
		default:
			states = append(states, name+" running")
		}
	}
	// migrate runs once, in the foreground, and the installer removes it; a stack from an older installer keeps its exited container.
	if exists, err := d.rt.Exists(ctx, KindContainer, ContainerMigrate); err != nil {
		problems = append(problems, err.Error())
	} else if exists {
		raw, err := d.rt.Inspect(ctx, ContainerMigrate)
		if err == nil {
			d.raw[ContainerMigrate] = raw
			var st containerStatus
			if st, err = parseContainerStatus(ContainerMigrate, raw); err == nil && (st.State.Status != "exited" || st.State.ExitCode != 0) {
				err = fmt.Errorf("%s is %s with exit code %d, want exited with 0", ContainerMigrate, st.State.Status, st.State.ExitCode)
			}
		}
		if err != nil {
			problems = append(problems, err.Error())
		}
	}
	if len(problems) > 0 {
		n := d.rt.Name()
		d.fail("containers", strings.Join(problems, "; "), fmt.Sprintf("read the container's log with `%s logs <container>`, fix the cause and start it with `%s start <container>`", n, n))
		return
	}
	d.pass("containers", strings.Join(states, ", "))
}

// checkRestart checks that the containers come back after a reboot: the restart policy on both engines, and on Podman
// the user's podman-restart.service, which rootless Podman needs to honour it (spike S2).
func (d *doctor) checkRestart(ctx context.Context) {
	if !d.needRuntime("restart") || !d.needMachine("restart") {
		return
	}
	var problems []string
	for _, name := range []string{ContainerDB, ContainerServe} {
		raw, ok := d.raw[name]
		if !ok {
			d.skip("restart", "containers")
			return
		}
		in, err := parseInspect(name, raw)
		switch {
		case err != nil:
			problems = append(problems, err.Error())
		case in.HostConfig.RestartPolicy.Name != "always":
			problems = append(problems, fmt.Sprintf("%s has restart policy %q, want always", name, in.HostConfig.RestartPolicy.Name))
		}
	}
	detail := "restart policy always"
	if d.s.Runtime == RuntimePodman {
		props, err := d.systemctlShow(ctx, d.viaSSH, "podman-restart.service")
		switch {
		case err != nil:
			problems = append(problems, "read podman-restart.service: "+err.Error())
		case props["UnitFileState"] != "enabled":
			state := cmp.Or(props["UnitFileState"], "not installed")
			problems = append(problems, "podman-restart.service is "+state+" for the rootless user, so a reboot leaves the containers stopped")
		default:
			detail += ", podman-restart.service enabled"
		}
	}
	if len(problems) > 0 {
		fix := "set the restart policy with `suiteward install`, which recreates the containers as the release defines them"
		if d.s.Runtime == RuntimePodman {
			fix = "run `systemctl --user enable podman-restart.service` as the rootless user (in a Podman machine: `podman machine ssh --username user <machine> systemctl --user enable podman-restart.service`)"
		}
		d.fail("restart", strings.Join(problems, "; "), fix)
		return
	}
	d.pass("restart", detail)
}

// systemctlShow reads the properties of one user unit; `show` succeeds for any state, where is-active and is-enabled fail.
func (d *doctor) systemctlShow(ctx context.Context, inMachine bool, unit string) (map[string]string, error) {
	argv := []string{"--user", "show", "-p", "LoadState", "-p", "ActiveState", "-p", "SubState", "-p", "UnitFileState", "-p", "WantedBy", unit}
	var out []byte
	var err error
	if inMachine {
		out, err = d.machineSSH(ctx, "systemctl "+strings.Join(argv, " "))
	} else {
		out, err = d.deps.Exec(ctx, "systemctl", argv, nil)
	}
	if err != nil {
		return nil, err
	}
	props := map[string]string{}
	for _, line := range strings.Split(string(out), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(line), "="); ok {
			props[k] = v
		}
	}
	return props, nil
}

// ---- units (isolated) ----

func (d *doctor) checkUnits(ctx context.Context) {
	var problems []string
	for _, u := range isolatedUnits {
		props, err := d.systemctlShow(ctx, false, u.name)
		switch {
		case err != nil:
			problems = append(problems, "read "+u.name+": "+err.Error())
		case props["LoadState"] != "loaded":
			problems = append(problems, fmt.Sprintf("%s is %s, not loaded", u.name, cmp.Or(props["LoadState"], "unknown")))
		case props["ActiveState"] != u.active || props["SubState"] != u.sub:
			d.units[u.name] = props
			problems = append(problems, fmt.Sprintf("%s is %s (%s), want %s (%s)", u.name, props["ActiveState"], props["SubState"], u.active, u.sub))
		default:
			d.units[u.name] = props
		}
	}
	if len(problems) > 0 {
		d.fail("units", strings.Join(problems, "; "), "read the log with `journalctl --user -u suiteward.service -u suiteward-migrate.service -u suiteward-db.service`, fix the cause and run `systemctl --user restart suiteward.service`")
		return
	}
	d.pass("units", "suiteward-db and suiteward active, suiteward-migrate active (exited)")
}

func (d *doctor) checkLinger(ctx context.Context) {
	out, err := d.deps.Exec(ctx, "id", []string{"-un"}, nil)
	user := strings.TrimSpace(string(out))
	if err == nil && user == "" {
		err = errors.New("id printed no user name")
	}
	if err == nil {
		out, err = d.deps.Exec(ctx, "loginctl", []string{"show-user", user, "-p", "Linger"}, nil)
	}
	switch {
	case err != nil:
		d.fail("linger", "read the linger setting: "+err.Error(), "check that `loginctl show-user $(id -un) -p Linger` works in this session")
	case strings.TrimSpace(string(out)) != "Linger=yes":
		d.fail("linger", "user "+user+" does not linger, so the service stops when the last session ends", "run `sudo loginctl enable-linger "+user+"`")
	default:
		d.pass("linger", user+" lingers")
	}
}

// checkUnitsEnabled is the restart check of the isolated tier: the units start at boot through the [Install] of their quadlet,
// which systemd reports as generated (or enabled) with WantedBy=default.target.
func (d *doctor) checkUnitsEnabled() {
	var problems []string
	for _, u := range isolatedUnits {
		props, ok := d.units[u.name]
		if !ok {
			d.skip("restart", "units")
			return
		}
		state := props["UnitFileState"]
		generated := state == "generated" && slices.Contains(strings.Fields(props["WantedBy"]), "default.target")
		if state != "enabled" && !generated {
			problems = append(problems, fmt.Sprintf("%s is %s, not enabled to start at boot", u.name, cmp.Or(state, "unknown")))
		}
	}
	if len(problems) > 0 {
		d.fail("restart", strings.Join(problems, "; "), "check that each quadlet has `WantedBy=default.target` under [Install], then run `systemctl --user daemon-reload`")
		return
	}
	d.pass("restart", "the units start at boot through their quadlet [Install]")
}

// ---- pins ----

func (d *doctor) checkPins(ctx context.Context) {
	var err error
	var image string
	detail := "the release images are one digest and the database image is the pinned one: "
	switch {
	case d.s.Tier == config.TierIsolated:
		image, err = d.unitPins()
		if err == nil {
			detail, err = d.runningMatchesUnits(ctx, image)
		}
	case !d.needRuntime("pins"):
		return
	case d.raw[ContainerDB] == nil || d.raw[ContainerServe] == nil:
		d.skip("pins", "containers")
		return
	case d.s.Runtime == RuntimePodman:
		image, err = adoptedImage(AdoptContainers(d.raw))
	default:
		image, err = dockerPins(d.raw)
	}
	if err != nil {
		d.fail("pins", err.Error(), "install the release again with `suiteward install --upgrade`, or undo the edit it names")
		return
	}
	d.pass("pins", detail+image)
}

// runningMatchesUnits compares the images the service and the database containers run with the images of the units. The units
// are what the next restart will run; this says the running containers are those. It returns the detail of a passing check,
// which ends with the release image.
func (d *doctor) runningMatchesUnits(ctx context.Context, image string) (string, error) {
	detail := "the installed units are consistent with the units of this release, and so are the images the containers run: "
	if d.rt == nil {
		return "the installed units are consistent with the units of this release; the running containers were not compared because the runtime failed: ", nil
	}
	postgres, err := PostgresImage()
	if err != nil {
		return "", err
	}
	var problems []string
	for _, c := range []struct{ name, want string }{{ContainerServe, image}, {ContainerDB, postgres}} {
		raw, err := d.rt.Inspect(ctx, c.name)
		var in inspected
		if err == nil {
			in, err = parseInspect(c.name, raw)
		}
		switch {
		case err != nil:
			problems = append(problems, err.Error())
		case !sameImage(in.Config.Image, c.want):
			problems = append(problems, fmt.Sprintf("%s runs %s but its unit says %s", c.name, in.Config.Image, c.want))
		}
	}
	if len(problems) > 0 {
		return "", errors.New(strings.Join(problems, "; "))
	}
	return detail, nil
}

func adoptedImage(a Adopted, err error) (string, error) {
	var diff *DiffError
	if errors.As(err, &diff) {
		return "", fmt.Errorf("the installed stack differs from this release: %s", formatDiffs(diff.Diffs))
	}
	return a.Image, err
}

func formatDiffs(diffs []Diff) string {
	const shown = 4
	var parts []string
	for _, diff := range diffs[:min(len(diffs), shown)] {
		want, got := diff.printable()
		parts = append(parts, fmt.Sprintf("%s %s: want %q, got %q", diff.Source, diff.Field, want, got))
	}
	if len(diffs) > shown {
		parts = append(parts, fmt.Sprintf("and %d more", len(diffs)-shown))
	}
	return strings.Join(parts, "; ")
}

// unitPins reads the quadlets of the service account and compares them with the units of this release.
func (d *doctor) unitPins() (string, error) {
	dir := d.deps.Getenv("XDG_CONFIG_HOME")
	if dir == "" {
		home := d.deps.Getenv("HOME")
		if home == "" {
			return "", errors.New("cannot find the unit directory: HOME is not set")
		}
		dir = filepath.Join(home, ".config")
	}
	dir = filepath.Join(dir, "containers", "systemd")
	embedded, err := embeddedUnits()
	if err != nil {
		return "", err
	}
	installed := map[string][]byte{}
	for name := range embedded {
		raw, err := os.ReadFile(filepath.Clean(filepath.Join(dir, name)))
		switch {
		case errors.Is(err, os.ErrNotExist): // AdoptUnits reports the missing file
		case err != nil:
			return "", fmt.Errorf("read the unit %s: %w", name, err)
		default:
			installed[name] = raw
		}
	}
	return adoptedImage(AdoptUnits(installed, nil))
}

// dockerPins compares the images of a Docker stack: AdoptContainers does not adopt Docker's volume-held secrets, so the
// pins are checked on their own.
func dockerPins(raw map[string][]byte) (string, error) {
	images := map[string]string{}
	for name, data := range raw {
		in, err := parseInspect(name, data)
		if err != nil {
			return "", err
		}
		images[name] = in.Config.Image
	}
	postgres, err := PostgresImage()
	if err != nil {
		return "", err
	}
	var problems []string
	if !sameImage(images[ContainerDB], postgres) {
		problems = append(problems, fmt.Sprintf("%s runs %s, want the pinned %s", ContainerDB, images[ContainerDB], postgres))
	}
	if !digestImage.MatchString(images[ContainerServe]) {
		problems = append(problems, fmt.Sprintf("%s runs %s, which is not pinned by a digest", ContainerServe, images[ContainerServe]))
	}
	if migrate, ok := images[ContainerMigrate]; ok && !sameImage(migrate, images[ContainerServe]) {
		problems = append(problems, fmt.Sprintf("%s runs %s but %s runs %s", ContainerMigrate, migrate, ContainerServe, images[ContainerServe]))
	}
	if len(problems) > 0 {
		return "", errors.New(strings.Join(problems, "; "))
	}
	return images[ContainerServe], nil
}

// ---- ports and /status ----

// get reads one path of the service's HTTP port: from this host, or from inside the Podman machine where the port is published.
func (d *doctor) get(ctx context.Context, path string) ([]byte, error) {
	address := "http://127.0.0.1:" + strconv.Itoa(d.s.HTTPPort) + path
	if d.viaSSH {
		return d.machineSSH(ctx, "curl -fsS -m 5 "+address)
	}
	ctx, cancel := context.WithTimeout(ctx, probeTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

func (d *doctor) checkPorts(ctx context.Context) {
	if !d.needMachine("ports") {
		d.skip("healthz", "machine")
		d.skip("status", "machine")
		return
	}
	if _, err := d.get(ctx, "/readyz"); err != nil {
		d.fail("ports", fmt.Sprintf("GET /readyz on port %d: %v", d.s.HTTPPort, err),
			"check the containers or units above, and that http_port in the settings is the port the service publishes")
		d.skip("healthz", "ports")
		d.skip("status", "ports")
		return
	}
	d.pass("ports", fmt.Sprintf("/readyz answers on 127.0.0.1:%d", d.s.HTTPPort))

	var health struct{ Status string }
	body, err := d.get(ctx, "/healthz")
	if err == nil {
		if err = json.Unmarshal(body, &health); err != nil {
			err = errors.New("the answer is not JSON")
		} else if health.Status != "ok" {
			err = fmt.Errorf("status %q, want ok", health.Status)
		}
	}
	if err != nil {
		d.fail("healthz", fmt.Sprintf("GET /healthz on port %d: %v", d.s.HTTPPort, err), "read the service log for why the process is not healthy")
	} else {
		d.pass("healthz", "/healthz answers ok")
	}

	body, err = d.get(ctx, "/status")
	var status struct {
		Version       string
		Tier          string
		SchemaVersion *int
		Jobs          struct{ Discarded *int }
		Outbox        struct{ Failed *int }
	}
	if err == nil {
		if err = json.Unmarshal(body, &status); err != nil {
			err = errors.New("the answer is not JSON")
		}
	}
	if err != nil {
		d.fail("status", fmt.Sprintf("GET /status on port %d: %v", d.s.HTTPPort, err), "read the service log for why /status fails")
		return
	}
	var problems []string
	if status.Version == "" {
		problems = append(problems, "no version")
	}
	if status.SchemaVersion == nil {
		problems = append(problems, "no schemaVersion")
	}
	if status.Tier == "" {
		problems = append(problems, "no tier")
	} else if status.Tier != string(d.s.Tier) {
		problems = append(problems, fmt.Sprintf("tier %s where the settings say %s", status.Tier, d.s.Tier))
	}
	if n := status.Jobs.Discarded; n == nil {
		problems = append(problems, "no jobs.discarded")
	} else if *n > 0 {
		problems = append(problems, fmt.Sprintf("%d discarded jobs", *n))
	}
	if n := status.Outbox.Failed; n == nil {
		problems = append(problems, "no outbox.failed")
	} else if *n > 0 {
		problems = append(problems, fmt.Sprintf("%d failed outbox messages", *n))
	}
	if len(problems) > 0 {
		d.fail("status", "/status reports "+strings.Join(problems, ", "), "read the service log, and `suiteward probe` shows whether the worker still completes jobs")
		return
	}
	d.pass("status", fmt.Sprintf("version %s, schema version %d, no failed or discarded", status.Version, *status.SchemaVersion))
}

// ---- metadata and the instance ----

// The values the isolated host must apply to the stack (docs/install.md, step 10.14).
const (
	networkBackend = "netavark"
	dbMemoryBytes  = 402653184 // 384 MiB, the limit of suiteward-db.container
)

// checkHost reads what Podman applies on the isolated host: the network backend, and the memory limit of the database.
func (d *doctor) checkHost(ctx context.Context) {
	args := []string{"info", "--format", "{{.Host.NetworkBackend}}"}
	if d.s.Connection != "" {
		args = append([]string{"--connection", d.s.Connection}, args...)
	}
	out, err := d.deps.Exec(ctx, "podman", args, nil)
	switch backend := strings.TrimSpace(string(out)); {
	case err != nil:
		d.fail("network", "read the Podman network backend: "+err.Error(), "check that `podman info` works for this user")
	case backend != networkBackend:
		d.fail("network", fmt.Sprintf("the Podman network backend is %q, want %s", backend, networkBackend),
			"install netavark and check the Podman setup (docs/install.md, step 10.14); the metadata rule and the service network rely on it")
	default:
		d.pass("network", "the network backend is "+networkBackend)
	}

	if !d.needRuntime("memory") {
		return
	}
	raw, err := d.rt.Inspect(ctx, ContainerDB)
	var in inspected
	if err == nil {
		in, err = parseInspect(ContainerDB, raw)
	}
	switch {
	case err != nil:
		d.fail("memory", "read the memory limit of "+ContainerDB+": "+err.Error(), "start the database container, then run doctor again")
	case in.HostConfig.Memory != dbMemoryBytes:
		d.fail("memory", fmt.Sprintf("%s has memory limit %d bytes, want %d", ContainerDB, in.HostConfig.Memory, dbMemoryBytes),
			"check that suiteward-db.container sets PodmanArgs=--memory=384m and that Podman applies it (docs/install.md, step 10.14)")
	default:
		d.pass("memory", fmt.Sprintf("%s is limited to %d bytes", ContainerDB, dbMemoryBytes))
	}
}

// checkMetadata probes from the database container, which is on the service network. Every host of the isolated tier is
// checked: where no metadata service exists the connection simply fails.
func (d *doctor) checkMetadata(ctx context.Context) {
	if !d.needRuntime("metadata") {
		return
	}
	out, err := d.rt.Exec(ctx, ContainerDB, []string{"bash", "-c", metadataProbe}, nil)
	switch answer := strings.TrimSpace(string(out)); {
	case err != nil:
		d.fail("metadata", "the metadata probe could not run in "+ContainerDB+": "+err.Error(), "start the database container, then run doctor again")
	case answer == "reachable":
		d.fail("metadata", "169.254.169.254 answers from the service network, so the service can read the instance's credentials", metadataFix)
	case answer == "blocked":
		d.pass("metadata", "169.254.169.254 is not reachable from the service network")
	case answer == "control-failed":
		d.fail("metadata", "the control connection to the database port failed inside "+ContainerDB+", so the metadata probe proves nothing", "check that the database is running and healthy, then run doctor again")
	default:
		d.fail("metadata", "the metadata probe printed "+strconv.Quote(answer), "check that the database image has bash and timeout, then run doctor again")
	}
}

// checkInstance runs `suiteward check --json` in the service container and merges its results. Exit 1 means some check
// failed and its results are on stdout; anything else (exit 2 for settings errors, a binary without `check`, text instead of
// JSON) is one failure that summarizes what the command said.
func (d *doctor) checkInstance(ctx context.Context) {
	if !d.needRuntime("instance.check") {
		return
	}
	out, err := d.rt.Exec(ctx, ContainerServe, []string{"/suiteward", "check", "--json"}, nil)
	var list []doctorResult
	jerr := json.Unmarshal(bytes.TrimSpace(out), &list)
	if jerr == nil && (len(list) == 0 || slices.ContainsFunc(list, func(r doctorResult) bool { return r.Name == "" })) {
		jerr = errors.New("the list is empty or has an unnamed result")
	}
	failed := slices.ContainsFunc(list, func(r doctorResult) bool { return !r.OK })
	switch {
	case jerr == nil && (err == nil || failed):
		d.results = append(d.results, list...)
	case err != nil:
		d.fail("instance.check", "`suiteward check --json` in "+ContainerServe+" failed without a result list: "+err.Error(),
			d.readFix())
	default:
		d.fail("instance.check", "`suiteward check --json` printed no JSON result list: "+oneLine(string(out)), d.readFix())
	}
}

// readFix points to the command whose output says why the instance check has no result list: the runtime hands doctor
// the error of a command that exited non-zero, but not what it printed, and `check` exits 1 whenever one of its checks fails.
func (d *doctor) readFix() string {
	return fmt.Sprintf("read the results with `%s exec %s /suiteward check`; if the command is unknown, update the service image to a release that has it", d.rt.Name(), ContainerServe)
}

// queryTimeout bounds one runtime, systemctl or pipe query, so a hung engine or machine is one failure and not a hang.
var queryTimeout = 20 * time.Second

// instanceTimeout bounds `suiteward check` in the container, which reads GitHub once per repository.
const instanceTimeout = 2 * time.Minute

// boundExec gives every command a deadline.
func boundExec(inner Exec) Exec {
	return func(ctx context.Context, name string, args []string, stdin io.Reader) ([]byte, error) {
		timeout := queryTimeout
		if slices.Contains(args, "/suiteward") {
			timeout = max(timeout, instanceTimeout)
		}
		ctx, cancel := context.WithTimeout(ctx, timeout)
		defer cancel()
		return inner(ctx, name, args, stdin)
	}
}
