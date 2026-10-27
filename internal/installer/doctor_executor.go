package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"
)

// What `suiteward doctor` says of the executor (ADR 0028, section 10). On the isolated tier doctor runs as the service account, which cannot
// run nft, reach the manager of the executor's account or read its token, so every check is built from what that account can read: the files
// that the host step leaves world-readable, `id`, `loginctl`, `systemctl show` of a system unit and `pgrep`, and from what /status says the
// service saw.

// warn reports a pass that needs a look: it is counted and shown as a warning, and it does not change the exit code.
func (d *doctor) warn(name, detail, fix string) {
	d.results = append(d.results, doctorResult{Name: name, OK: true, Detail: oneLine(detail), Fix: fix, Warning: true})
}

// out runs a command and returns its trimmed output.
func (d *doctor) out(ctx context.Context, name string, args ...string) (string, error) {
	out, err := d.deps.Exec(ctx, name, args, nil)
	return strings.TrimSpace(string(out)), err
}

// hostStep is the command that makes the executor on an isolated host, as the fix of most of its results names it.
func (d *doctor) hostStep() string {
	return "as the administrator run `sudo " + d.deps.command() + " install host --sha256sums <the SHA256SUMS of the release>`"
}

// checkExecutorIsolated runs the checks of the executor on an isolated host.
func (d *doctor) checkExecutorIsolated(ctx context.Context) {
	uid := d.checkExecutorAccount(ctx)
	d.checkExecutorDelegation(ctx, uid)
	d.checkExecutorNetwork(ctx, uid)
	d.checkExecutorUnit(ctx)
	d.checkExecutorBinary(ctx)
	d.checkExecutorRunning(ctx, d.isolatedProcess, runningFixes{
		noToken:    d.hostStep() + ", then run `" + d.deps.command() + " install` (or `--upgrade`) as the service account, which gives the service the token",
		notRunning: "read why with `sudo journalctl _SYSTEMD_USER_UNIT=" + executorUnitName + " -n 50 --no-pager`; " + d.hostStep() + " starts it",
		notSeen:    "read the executor's log; " + d.hostStep() + " again, then the install as the service account, so that both hold one token and one port",
	})
	// The storage of the executor's Podman is its own, and this account cannot read it: the host step is what pulls the image and checks it by digest.
	d.pass("executor.image", "the Go image of the sandboxes is in the storage of "+ExecutorAccount+", which this account cannot read: it is not checked here, the host step pulls it and verifies its digest at every run (run `sudo "+d.deps.command()+" install host --sha256sums <the SHA256SUMS of the release>` again after an upgrade)")
}

// subidRange is the range an account has in /etc/subuid or /etc/subgid.
func subidRange(content, account string) (start, count int, found bool, err error) {
	for _, line := range strings.Split(content, "\n") {
		fields := strings.Split(strings.TrimSpace(line), ":")
		if len(fields) != 3 || fields[0] != account {
			continue
		}
		start, err1 := strconv.Atoi(fields[1])
		count, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil {
			return 0, 0, false, fmt.Errorf("cannot read the line %q", line)
		}
		return start, count, true, nil
	}
	return 0, 0, false, nil
}

// checkExecutorAccount checks that the executor has an account of its own: another uid than the service account, subordinate id ranges that
// overlap none of the service account's (the two rootless Podmans must not share a user namespace) and linger. It returns the executor's uid, or
// "" when there is none.
func (d *doctor) checkExecutorAccount(ctx context.Context) string {
	fix := d.hostStep() + ", which makes the account with ranges of its own and linger"
	uid, err := d.out(ctx, "id", "-u", ExecutorAccount)
	if err != nil {
		d.fail("executor.account", "the account "+ExecutorAccount+" does not exist: "+err.Error(), fix)
		return ""
	}
	var problems []string
	if service, err := d.out(ctx, "id", "-u", ServiceAccount); err == nil && service == uid {
		problems = append(problems, ExecutorAccount+" has the same uid as the service account "+ServiceAccount+", so it is not apart from it")
	}
	for _, file := range []string{"/etc/subuid", "/etc/subgid"} {
		content, err := d.out(ctx, "cat", file)
		if err != nil {
			problems = append(problems, "read "+file+": "+err.Error())
			continue
		}
		exStart, exCount, exFound, exErr := subidRange(content, ExecutorAccount)
		svStart, svCount, svFound, svErr := subidRange(content, ServiceAccount)
		switch {
		case exErr != nil || svErr != nil:
			problems = append(problems, file+" is not readable: "+errors.Join(exErr, svErr).Error())
		case !exFound:
			problems = append(problems, ExecutorAccount+" has no range in "+file+" (the "+strings.TrimPrefix(file, "/etc/")+" range)")
		case svFound && exStart < svStart+svCount && svStart < exStart+exCount:
			problems = append(problems, "the ranges of "+ExecutorAccount+" and "+ServiceAccount+" in "+file+" overlap")
		}
	}
	linger, err := d.out(ctx, "loginctl", "show-user", ExecutorAccount, "-p", "Linger")
	if err != nil || linger != "Linger=yes" {
		problems = append(problems, ExecutorAccount+" does not linger (no linger), so its executor stops at logout and does not start at boot")
	}
	if len(problems) > 0 {
		d.fail("executor.account", strings.Join(problems, "; "), fix)
		return uid
	}
	d.pass("executor.account", fmt.Sprintf("%s (uid %s) is not the service account, has subuid and subgid ranges that overlap none of its, and lingers", ExecutorAccount, uid))
	return uid
}

// checkExecutorDelegation checks the drop-in that delegates the cgroup controllers, and that the executor's manager has them: a drop-in that
// was written after the manager started changes nothing until the manager restarts.
func (d *doctor) checkExecutorDelegation(ctx context.Context, uid string) {
	if uid == "" {
		d.skip("executor.delegation", "executor.account")
		return
	}
	var problems []string
	on, err := d.out(ctx, "cat", executorDropIn)
	switch {
	case err != nil:
		problems = append(problems, "read "+executorDropIn+": "+err.Error())
	case on != strings.TrimSpace(executorDropInText):
		problems = append(problems, executorDropIn+" is not the drop-in of the release (Delegate=cpu cpuset io memory pids)")
	}
	controllers, err := d.out(ctx, "cat", "/sys/fs/cgroup/user.slice/user-"+uid+".slice/user@"+uid+".service/cgroup.controllers")
	have := strings.Fields(controllers)
	var missing []string
	for _, c := range []string{"cpu", "memory", "pids"} {
		if !slices.Contains(have, c) {
			missing = append(missing, c)
		}
	}
	switch {
	case err != nil:
		problems = append(problems, "read the controllers of the user manager of "+ExecutorAccount+": "+err.Error())
	case len(missing) > 0:
		problems = append(problems, "the user manager of "+ExecutorAccount+" has the controllers "+strings.Join(have, " ")+", and lacks "+strings.Join(missing, " ")+": its sandboxes cannot be limited")
	}
	if len(problems) > 0 {
		d.fail("executor.delegation", strings.Join(problems, "; "), d.hostStep()+", which writes the drop-in; a manager that started before it has its controllers only after `sudo systemctl restart user@"+uid+".service` (it holds no container of the stack) or a reboot")
		return
	}
	d.pass("executor.delegation", "the manager of "+ExecutorAccount+" has "+strings.Join(have, " ")+", which "+executorDropIn+" delegates")
}

// checkExecutorNetwork checks the rules that refuse the executor the instance-metadata addresses. nft cannot be read without root, so what is
// read is the rule file and the state of the unit that loads it: a proxy for the loaded rules, and the detail says so.
func (d *doctor) checkExecutorNetwork(ctx context.Context, uid string) {
	if uid == "" {
		d.skip("executor.network", "executor.account")
		return
	}
	var problems []string
	rule, err := d.out(ctx, "cat", executorRuleFile)
	switch {
	case err != nil:
		problems = append(problems, "read "+executorRuleFile+": "+err.Error())
	case rule != strings.TrimSpace(executorRulesText(uid)):
		problems = append(problems, executorRuleFile+" is not the rule of the release for uid "+uid)
	}
	state, err := d.out(ctx, "systemctl", "show", "--property=ActiveState", "--value", executorBlockUnit)
	switch {
	case err != nil:
		problems = append(problems, "read "+executorBlockUnit+": "+err.Error())
	case state != "active":
		problems = append(problems, executorBlockUnit+" is "+state+", so the rules are not loaded")
	}
	if len(problems) > 0 {
		d.fail("executor.network", strings.Join(problems, "; "), d.hostStep()+", which writes the rules and loads them")
		return
	}
	d.pass("executor.network", fmt.Sprintf("%s refuses %s and %s to uid %s on every port, and %s is active (the rule file and its unit, not the loaded table: nft cannot be read without root)",
		executorRuleFile, metadataAddress, metadataAddress6, uid, executorBlockUnit))
}

var executorUnitPort = regexp.MustCompile(`(?m)^Environment=` + executorURLEnv + `=http://127\.0\.0\.1:([0-9]+)$`)

// checkExecutorUnit checks the executor's unit against the release's, with the port the service publishes. A unit that names another port
// is the one drift that has a command of its own.
func (d *doctor) checkExecutorUnit(ctx context.Context) {
	want, err := RenderExecutorUnit(d.s.HTTPPort)
	if err != nil {
		d.fail("executor.unit", err.Error(), "this is a bug of the installer: report it")
		return
	}
	on, err := d.out(ctx, "cat", executorUnitPath)
	if err != nil {
		d.fail("executor.unit", "read "+executorUnitPath+": "+err.Error(), d.hostStep())
		return
	}
	if on == strings.TrimSpace(string(want)) {
		d.pass("executor.unit", executorUnitPath+" is the unit of the release, pointed at http://127.0.0.1:"+strconv.Itoa(d.s.HTTPPort))
		return
	}
	if m := executorUnitPort.FindStringSubmatch(on); len(m) > 1 && m[1] != strconv.Itoa(d.s.HTTPPort) {
		if fixed, err := replaceLine(on, lineEdit{old: executorUnitURLLine(mustAtoi(m[1])), new: executorUnitURLLine(d.s.HTTPPort)}); err == nil && fixed == strings.TrimSpace(string(want)) {
			d.fail("executor.unit", fmt.Sprintf("the unit points the executor at http://127.0.0.1:%s and the service publishes its HTTP listener on port %d", m[1], d.s.HTTPPort),
				d.hostStep()+fmt.Sprintf(" --http-port %d", d.s.HTTPPort))
			return
		}
	}
	d.fail("executor.unit", executorUnitPath+" differs from the unit of the release", d.hostStep())
}

// mustAtoi reads digits that a pattern already checked.
func mustAtoi(digits string) int {
	n, _ := strconv.Atoi(digits)
	return n
}

// checkExecutorBinary checks the executor's binary: it is there, it is the one that was installed (the pin beside it), it is this program's
// build, and the pin is a line of the release's SHA256SUMS, which the host step requires. A pin that no SHA256SUMS gave (one that was made by hand) fails: on
// this tier the pin is the signed release's.
func (d *doctor) checkExecutorBinary(ctx context.Context) {
	fix := d.hostStep() + " with this program"
	listed, err := d.out(ctx, "sha256sum", executorBinary)
	if err != nil || len(strings.Fields(listed)) == 0 {
		d.fail("executor.binary", "no executor binary at "+executorBinary+": "+fmt.Sprint(err), fix)
		return
	}
	sum := strings.Fields(listed)[0]
	pin, err := d.out(ctx, "cat", executorPin)
	fields := strings.Fields(pin)
	if err != nil || len(fields) < 2 {
		d.fail("executor.binary", "no pin of the executor's binary at "+executorPin+": "+fmt.Sprint(err), fix)
		return
	}
	pinned, name := fields[0], fields[1]
	own, err := d.ownHash()
	switch {
	case err != nil:
		d.fail("executor.binary", "hash this program: "+err.Error(), "this is a bug of the installer: report it")
	case sum != pinned:
		d.fail("executor.binary", fmt.Sprintf("the executor's binary has changed since it was installed (SHA-256 %s, pinned %s)", sum, pinned), fix)
	case sum != own:
		d.fail("executor.binary", fmt.Sprintf("the executor runs another build than this program (the executor's SHA-256 %s, this program's %s)", sum, own), fix)
	case !releaseAssetOf("linux").MatchString(name):
		d.fail("executor.binary", "the executor's binary (SHA-256 "+sum+") is pinned without a line of a release's SHA256SUMS (the pin names "+name+"), so the release does not list it: on this tier the pin is the signed release's",
			d.hostStep()+" with that release's binary")
	default:
		d.pass("executor.binary", "the executor's binary (SHA-256 "+sum+") is the pinned one, this program's, and the release's line of SHA256SUMS "+name)
	}
}

// ownHash is the SHA-256 of the program that runs doctor.
func (d *doctor) ownHash() (string, error) {
	if d.deps.Executable == nil {
		return "", errors.New("the installer cannot find its own program")
	}
	exe, err := d.deps.Executable()
	if err != nil {
		return "", err
	}
	return hashFile(exe)
}

// executionStatus is the execution object of /status: whether the service has an executor token, and when it last saw an executor.
type executionStatus struct {
	Enabled    bool      `json:"enabled"`
	LastSeenAt time.Time `json:"lastSeenAt"`
}

// executorSeenWithin is how long ago the service may have last seen an executor for the executor to count as running: an idle one asks every
// 5 seconds, and after an error it waits up to 60.
const executorSeenWithin = 2 * time.Minute

// isolatedProcess says whether a process of the executor's account runs the installed binary: `pgrep` sees every account's processes, and the
// executor's command line holds no secret.
func (d *doctor) isolatedProcess(ctx context.Context) error {
	_, err := d.out(ctx, "pgrep", "-u", ExecutorAccount, "-f", "--", "^"+regexp.QuoteMeta(executorBinary)+" execute$")
	if err != nil {
		return fmt.Errorf("no process of %s runs %s execute (%v)", ExecutorAccount, executorBinary, err)
	}
	return nil
}

// runningFixes are the fixes of the ways the executor fails to run, which each tier words for its own commands: the isolated tier has an
// administrator and a service account, the co-located tier one user.
type runningFixes struct {
	noToken    string // the service has no executor token
	notRunning string // the process is not running
	notSeen    string // it runs, and the service has not seen it lately
}

// checkExecutorRunning pairs the process of the executor with what /status says the service saw. The service starts the clock of its last sight
// when it starts, so a service that was just restarted looks as if it saw an executor: the process is the evidence that the executor exists, and
// the age of the last sight that it is the service's executor, with the service's token. A /status without the execution object is the answer
// of a service older than M2.1.
func (d *doctor) checkExecutorRunning(ctx context.Context, process func(context.Context) error, fixes runningFixes) {
	body, ok := d.raw["status"]
	if !ok {
		d.skip("executor.running", "status")
		return
	}
	var doc struct {
		Execution *executionStatus `json:"execution"`
	}
	if err := json.Unmarshal(body, &doc); err != nil {
		d.fail("executor.running", "read the execution object of /status: "+err.Error(), "read the service log for why /status fails")
		return
	}
	upgrade := "run `" + d.deps.command() + " install --upgrade` with this release"
	switch {
	case doc.Execution == nil:
		d.fail("executor.running", "/status has no execution object: the service is older than M2.1 and cannot run an executor", upgrade)
		return
	case !doc.Execution.Enabled:
		d.fail("executor.running", "the service has no executor token (EXECUTOR_TOKEN is unset), so it serves no executor and every execution job is unavailable", fixes.noToken)
		return
	}
	if err := process(ctx); err != nil {
		d.fail("executor.running", err.Error(), fixes.notRunning)
		return
	}
	age := time.Since(doc.Execution.LastSeenAt).Round(time.Second)
	if age > executorSeenWithin {
		d.fail("executor.running", fmt.Sprintf("the executor runs, but the service last saw an executor %s ago: it cannot reach the service at http://127.0.0.1:%d, or it sends another token than the service's", age, d.s.HTTPPort), fixes.notSeen)
		return
	}
	d.pass("executor.running", fmt.Sprintf("the executor runs, and it was last seen %s ago by the service (the service starts that clock when it starts, so the process is the evidence of the executor)", age))
}
