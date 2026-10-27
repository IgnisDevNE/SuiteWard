package installer

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
)

// hostFake is a machine for PrepareHost: it answers every command from its state and records each call.
type hostFake struct {
	calls []call
	goos  string
	euid  string

	podman      string // version text; empty means podman is not installed (dnf install makes it present)
	changelog   string
	account     bool
	subuid      string
	subgid      string
	metadata    string // the HTTP code the metadata probe prints; empty means the probe fails (no answer)
	nftOutput   string
	paths       map[string]bool // paths `test -e` finds
	symlinks    map[string]bool // paths `test -L` finds
	ruleOnDisk  string          // the nft rule file as it is on the host; empty means there is none
	envsDiffer  bool
	dnfFixesCVE bool
	failOn      map[string]error // command name + " " + arguments containing the key -> its error

	// The executor (host_executor_test.go).
	execAccount       bool              // suiteward-exec exists (useradd makes it)
	files             map[string]string // what cat prints for the files of the executor
	hashes            map[string]string // what sha256sum prints for the installed binary (install makes it)
	controllers       string            // the cgroup controllers of the executor's manager, before a restart
	afterRestart      string            // and after a restart of it
	execUnitState     string            // the ActiveState of the executor's unit
	exe               string            // the program of this run
	tornCopy          bool              // install leaves a file that is not the one it was given
	execRunning       bool              // the executor's unit is running when the step starts
	execEnabled       bool              // enable --now has started the executor's unit
	imagePulled       bool              // the Go image is in the executor's rootless storage
	pullFails         bool              // podman pull as the executor fails
	pullLeavesNothing bool              // podman pull succeeds but the image is not there after it
}

func newHostFake() *hostFake {
	return &hostFake{
		goos: "linux", euid: "0", podman: "podman version 5.8.2", changelog: "* Mon - Resolves CVE-2026-57231\n", account: true,
		subuid: "suiteward:100000:65536\nsuiteward-exec:165536:65536\n", subgid: "suiteward:100000:65536\nsuiteward-exec:165536:65536\n",
		nftOutput:   "table inet suiteward_imds { chain output { meta skuid suiteward ip daddr 169.254.169.254 reject } }",
		paths:       map[string]bool{"/etc/suiteward/github.env": true},
		symlinks:    map[string]bool{},
		execAccount: true, files: map[string]string{}, hashes: map[string]string{}, controllers: "cpu cpuset io memory pids", afterRestart: "cpu cpuset io memory pids", execUnitState: "active",
	}
}

func (h *hostFake) deps() Deps {
	return Deps{
		GOOS:        h.goos,
		Getenv:      func(string) string { return "" },
		Interactive: func(context.Context, string, []string) error { return nil },
		Executable: func() (string, error) {
			if h.exe != "" {
				return h.exe, nil
			}
			return os.Executable() // the test binary: a file that exists, for the tests that do not care which program is copied
		},
		Exec: func(_ context.Context, name string, args []string, stdin io.Reader) ([]byte, error) {
			c := call{name: name, args: append([]string(nil), args...)}
			if stdin != nil {
				data, err := io.ReadAll(stdin)
				if err != nil {
					return nil, err
				}
				c.stdin = string(data)
			}
			h.calls = append(h.calls, c)
			return h.answer(c)
		},
	}
}

func (h *hostFake) answer(c call) ([]byte, error) {
	line := c.line()
	for match, err := range h.failOn {
		if strings.Contains(c.name+" "+line, match) {
			return nil, err
		}
	}
	if out, ok, err := h.answerExecutor(c); ok {
		return out, err
	}
	fail := errors.New(c.name + ": exit status 1")
	switch {
	case c.name == "id" && line == "-u":
		return []byte(h.euid + "\n"), nil
	case c.name == "id" && line == "-u suiteward":
		if !h.account {
			return nil, fail
		}
		return []byte("1001\n"), nil
	case c.name == "useradd":
		h.account = true
	case c.name == "getent":
		return []byte("suiteward:x:1001:1001::/home/suiteward:/usr/sbin/nologin\n"), nil
	case c.name == "podman" && line == "--version":
		if h.podman == "" {
			return nil, fail
		}
		return []byte(h.podman + "\n"), nil
	case c.name == "rpm":
		return []byte(h.changelog), nil
	case c.name == "dnf":
		if line == "install -y podman" && h.podman == "" {
			h.podman = "podman version 5.8.2"
		}
		if line == "upgrade -y podman" && h.dnfFixesCVE {
			h.changelog = "CVE-2026-57231"
		}
	default:
		return h.answerSystem(c)
	}
	return nil, nil
}

// answerSystem answers the commands that read or probe the machine.
func (h *hostFake) answerSystem(c call) ([]byte, error) {
	line := c.line()
	fail := errors.New(c.name + ": exit status 1")
	switch {
	case c.name == "cat" && line == "/etc/subuid":
		return []byte(h.subuid), nil
	case c.name == "cat" && line == "/etc/subgid":
		return []byte(h.subgid), nil
	case c.name == "curl" && line == "--version":
		return []byte("curl 7"), nil
	case c.name == "curl":
		if h.metadata == "" {
			return nil, fail
		}
		return []byte(h.metadata), nil
	case c.name == "sh" && strings.Contains(line, "command -v nft"):
		return []byte("/usr/sbin/nft\n"), nil
	case c.name == "nft":
		return []byte(h.nftOutput), nil
	case c.name == "cat" && line == imdsRuleFile:
		if h.ruleOnDisk == "" {
			return nil, fail
		}
		return []byte(h.ruleOnDisk), nil
	case c.name == "test" && strings.HasPrefix(line, "-L "):
		if !h.symlinks[strings.TrimPrefix(line, "-L ")] {
			return nil, fail
		}
	case c.name == "test":
		if !h.paths[strings.TrimPrefix(line, "-e ")] {
			return nil, fail
		}
	case c.name == "cmp":
		if h.envsDiffer {
			return nil, fail
		}
	}
	return nil, nil
}

// has reports whether a call named name had an argument line that contains text.
func (h *hostFake) has(name, text string) bool {
	for _, c := range h.calls {
		if c.name == name && strings.Contains(c.line(), text) {
			return true
		}
	}
	return false
}

func (h *hostFake) callsTo(name string) []call {
	var out []call
	for _, c := range h.calls {
		if c.name == name {
			out = append(out, c)
		}
	}
	return out
}

func (h *hostFake) lines() []string {
	var out []string
	for _, c := range h.calls {
		out = append(out, c.name+" "+c.line())
	}
	return out
}

// runHost runs PrepareHost and returns everything it printed.
func runHost(t *testing.T, h *hostFake, flags map[string]string, tty bool, input string) (string, error) {
	t.Helper()
	if h.exe == "" {
		h.exe = writeProgram(t)
	}
	term, termOut := fakeTerminal(tty, input)
	r := newTestResolver(t, flags, nil, term)
	var out bytes.Buffer
	err := PrepareHost(context.Background(), r, h.deps(), &out)
	return out.String() + termOut.String(), err
}

// declineMetadataRule is the answer that declines the metadata rule, for tests about other steps.
var declineMetadataRule = map[string]string{"metadata-rule": "no"}

func TestPrepareHostRefusesOtherSystemsAndNonRoot(t *testing.T) {
	h := newHostFake()
	h.goos = "windows"
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err == nil || len(h.calls) != 0 {
		t.Fatalf("a non-Linux system must be refused before any command: %v, calls %v", err, h.lines())
	}

	h = newHostFake()
	h.euid = "1000"
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("a non-root run must be refused naming root, got %v", err)
	}
	if h.has("useradd", "") || h.has("dnf", "") || h.has("loginctl", "") {
		t.Fatalf("a refused run changed the host: %v", h.lines())
	}
}

func TestPrepareHostInstallsPodmanOnlyWithConsent(t *testing.T) {
	h := newHostFake()
	h.podman = ""
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err == nil || h.has("dnf", "") {
		t.Fatalf("without consent dnf must not run and the run must stop: %v, calls %v", err, h.lines())
	}

	h = newHostFake()
	h.podman = ""
	if _, err := runHost(t, h, declineMetadataRule, true, "n\n"); err == nil || h.has("dnf", "") {
		t.Fatalf("a declined prompt must not run dnf: %v", err)
	}

	h = newHostFake()
	h.podman = ""
	if _, err := runHost(t, h, map[string]string{"yes": "true", "metadata-rule": "no"}, false, ""); err != nil {
		t.Fatal(err)
	}
	if !h.has("dnf", "install -y podman") {
		t.Fatalf("--yes must install podman: %v", h.lines())
	}
}

func TestPrepareHostReportsAndChecksThePodmanVersion(t *testing.T) {
	h := newHostFake()
	out, err := runHost(t, h, declineMetadataRule, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "5.8.2") {
		t.Fatalf("the Podman version must be reported:\n%s", out)
	}
	if h.has("dnf", "") {
		t.Fatalf("a current Podman must not be touched: %v", h.lines())
	}

	h = newHostFake()
	h.podman = "podman version 5.4.0"
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err == nil || !strings.Contains(err.Error(), "5.5") {
		t.Fatalf("Podman older than 5.5 and no consent must stop naming 5.5, got %v", err)
	}
}

func TestPrepareHostRequiresTheFixForCVE202657231(t *testing.T) {
	h := newHostFake()
	h.changelog = "* Mon - something else\n"
	if _, err := runHost(t, h, map[string]string{"yes": "true", "metadata-rule": "no"}, false, ""); err == nil || !strings.Contains(err.Error(), "CVE-2026-57231") {
		t.Fatalf("a Podman without the fix must stop naming the CVE even after an upgrade, got %v", err)
	}
	if !h.has("dnf", "upgrade -y podman") {
		t.Fatalf("with consent the host must try dnf upgrade first: %v", h.lines())
	}
	if h.has("loginctl", "") || h.has("usermod", "") {
		t.Fatalf("nothing else may happen on a host with a vulnerable Podman: %v", h.lines())
	}

	h = newHostFake()
	h.changelog = ""
	h.dnfFixesCVE = true
	if _, err := runHost(t, h, map[string]string{"yes": "true", "metadata-rule": "no"}, false, ""); err != nil {
		t.Fatalf("an upgrade that brings the fix must let the run continue: %v", err)
	}
}

func TestPrepareHostCreatesTheAccountOnceExactlyAsTheGuideDoes(t *testing.T) {
	h := newHostFake()
	h.account = false
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if !h.has("useradd", "--create-home --shell /usr/sbin/nologin suiteward") {
		t.Fatalf("the account must be created as install.md 10.1 says: %v", h.lines())
	}

	h = newHostFake()
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if h.has("useradd", "") {
		t.Fatalf("an existing account must not be created again: %v", h.lines())
	}
}

func TestPrepareHostAddsSubordinateRangesOnlyWhereMissingAndFree(t *testing.T) {
	h := newHostFake()
	h.subuid = "other:100000:65536\n"
	h.subgid = "suiteward:231072:65536\nother:100000:65536\n"
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	var usermod []call // the executor's account has its own ranges in these files: the service account's are the question here
	for _, c := range h.callsTo("usermod") {
		if strings.HasSuffix(c.line(), " suiteward") {
			usermod = append(usermod, c)
		}
	}
	if len(usermod) != 1 {
		t.Fatalf("one usermod call for the one missing range, got %v", h.lines())
	}
	line := usermod[0].line()
	if !strings.Contains(line, "--add-subuids 165536-231071") || strings.Contains(line, "--add-subgids") || !strings.HasSuffix(line, " suiteward") {
		t.Fatalf("the missing subuid range must start after the last used one and subgid must be left alone: %q", line)
	}

	h = newHostFake()
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if h.has("usermod", "") {
		t.Fatalf("existing ranges must not change: %v", h.lines())
	}
}

func TestPrepareHostEnablesLinger(t *testing.T) {
	h := newHostFake()
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if !h.has("loginctl", "enable-linger suiteward") {
		t.Fatalf("linger must be enabled: %v", h.lines())
	}
}

func TestPrepareHostAsksAboutTheMetadataRuleAndNeverAssumes(t *testing.T) {
	h := newHostFake()
	h.metadata = "200"
	if _, err := runHost(t, h, nil, false, ""); err == nil || !strings.Contains(err.Error(), "metadata-rule") {
		t.Fatalf("a non-interactive run that finds a metadata service must name --metadata-rule, got %v", err)
	}
	if writeOf(h, imdsRuleFile) != "" || h.has("systemctl", "enable --now "+imdsUnit) {
		t.Fatalf("no rule may be installed without an answer: %v", h.lines())
	}

	h = newHostFake()
	h.metadata = "200"
	out, err := runHost(t, h, nil, true, "n\n")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "169.254.169.254") || writeOf(h, imdsRuleFile) != "" {
		t.Fatalf("a declined prompt must install nothing and the prompt must name the address:\n%s\n%v", out, h.lines())
	}

	h = newHostFake() // the probe gets no answer: no cloud metadata service
	out, err = runHost(t, h, nil, false, "")
	if err != nil {
		t.Fatalf("a host without a metadata service must not need an answer: %v", err)
	}
	if writeOf(h, imdsRuleFile) != "" || strings.Contains(out, "[y/N]") {
		t.Fatalf("no question and no rule without a metadata service:\n%s", out)
	}
}

func TestPrepareHostProbesTheMetadataAddressWithAShortTimeout(t *testing.T) {
	h := newHostFake()
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if !h.has("curl", "-m 3") || !h.has("curl", "http://169.254.169.254/latest/api/token") {
		t.Fatalf("the probe must reach for the metadata address with a timeout: %v", h.lines())
	}
}

func TestPrepareHostInstallsTheMetadataRuleAsTheGuideWritesIt(t *testing.T) {
	h := newHostFake()
	h.metadata = "200"
	if _, err := runHost(t, h, map[string]string{"metadata-rule": "yes"}, false, ""); err != nil {
		t.Fatal(err)
	}
	var rule, unit string
	for _, c := range h.callsTo("tee") {
		switch {
		case strings.HasSuffix(c.line(), "imds-block.nft"):
			rule = c.stdin
		case strings.HasSuffix(c.line(), "suiteward-imds-block.service"):
			unit = c.stdin
		}
	}
	for _, want := range []string{"table inet suiteward_imds", "meta skuid suiteward ip daddr 169.254.169.254 reject", "delete table inet suiteward_imds"} {
		if !strings.Contains(rule, want) {
			t.Errorf("the nft file lacks %q:\n%s", want, rule)
		}
	}
	for _, want := range []string{"ExecStart=/usr/sbin/nft -f /etc/suiteward/imds-block.nft", "ExecStop=-/usr/sbin/nft delete table inet suiteward_imds", "Before=systemd-user-sessions.service", "WantedBy=multi-user.target"} {
		if !strings.Contains(unit, want) {
			t.Errorf("the unit lacks %q:\n%s", want, unit)
		}
	}
	if !h.has("systemctl", "daemon-reload") || !h.has("systemctl", "enable --now suiteward-imds-block.service") || !h.has("nft", "list table inet suiteward_imds") {
		t.Fatalf("the unit must be loaded, enabled and the table listed: %v", h.lines())
	}
	if h.has("dnf", "") {
		t.Fatalf("nft is present, nothing to install: %v", h.lines())
	}
}

func TestPrepareHostInstallsNftablesOnlyWithConsent(t *testing.T) {
	missingNft := func(h *hostFake) Deps {
		d := h.deps()
		base := d.Exec
		d.Exec = func(ctx context.Context, name string, args []string, stdin io.Reader) ([]byte, error) {
			if name == "sh" && !h.has("dnf", "install -y nftables") {
				h.calls = append(h.calls, call{name: name, args: args})
				return nil, errors.New("exit status 1")
			}
			return base(ctx, name, args, stdin)
		}
		return d
	}
	run := func(h *hostFake, flags map[string]string) error {
		term, _ := fakeTerminal(false, "")
		return PrepareHost(context.Background(), newTestResolver(t, flags, nil, term), missingNft(h), io.Discard)
	}
	h := newHostFake()
	h.metadata = "200"
	if err := run(h, map[string]string{"metadata-rule": "yes"}); err == nil || h.has("dnf", "nftables") {
		t.Fatalf("without consent nftables must not be installed: %v", err)
	}
	h = newHostFake()
	h.metadata = "200"
	h.exe = writeProgram(t)
	if err := run(h, map[string]string{"metadata-rule": "yes", "yes": "true"}); err != nil || !h.has("dnf", "install -y nftables") {
		t.Fatalf("with consent nftables is installed and the step goes on: %v, %v", err, h.lines())
	}
}

func TestPrepareHostFailsWhenTheRuleIsNotLoaded(t *testing.T) {
	h := newHostFake()
	h.metadata = "200"
	h.nftOutput = "table inet suiteward_imds {}"
	if _, err := runHost(t, h, map[string]string{"metadata-rule": "yes"}, false, ""); err == nil || !strings.Contains(err.Error(), "reject") {
		t.Fatalf("a table without the reject rule must fail the step, got %v", err)
	}
}

func TestPrepareHostReappliesAnInstalledRuleWithoutAsking(t *testing.T) {
	h := newHostFake()
	h.metadata = "200"
	h.paths["/etc/systemd/system/suiteward-imds-block.service"] = true
	if _, err := runHost(t, h, nil, false, ""); err != nil {
		t.Fatalf("an installed rule needs no new answer: %v", err)
	}
	if !h.has("nft", "list table inet suiteward_imds") {
		t.Fatalf("an installed rule is still verified: %v", h.lines())
	}
}

func TestPrepareHostOwnsTheAccountConfigDirectoryAndCopiesTheEtcEnvFileLeavingTheSource(t *testing.T) {
	h := newHostFake()
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if !h.has("install", "-d -o suiteward -g suiteward -m 0700 /home/suiteward/.config /home/suiteward/.config/suiteward") {
		t.Fatalf("both directories must be named with owner and mode (install -d owns only the last component): %v", h.lines())
	}
	if !h.has("install", "-o suiteward -g suiteward -m 0600 /etc/suiteward/github.env /home/suiteward/.config/suiteward/github.env") {
		t.Fatalf("the /etc env file must move with owner and mode: %v", h.lines())
	}
	if h.has("rm", "") {
		t.Fatalf("the source must stay: a running hand-installed service reads it until the service step re-renders its units: %v", h.lines())
	}
}

func TestPrepareHostLeavesAHostWithoutAnEtcEnvFileAlone(t *testing.T) {
	h := newHostFake()
	delete(h.paths, "/etc/suiteward/github.env")
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if h.has("install", "github.env") || h.has("rm", "") {
		t.Fatalf("without a manual env file nothing moves: %v", h.lines())
	}
}

func TestPrepareHostStopsOnADifferentExistingEnvFileAndLeavesAnIdenticalOneAlone(t *testing.T) {
	const dest = "/home/suiteward/.config/suiteward/github.env"
	h := newHostFake()
	h.paths[dest] = true
	h.envsDiffer = true
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err == nil || h.has("rm", "") || h.has("install", "-m 0600 /etc/suiteward/github.env") {
		t.Fatalf("two different env files must stop without deleting or overwriting anything: %v", err)
	}

	h = newHostFake()
	h.paths[dest] = true
	if _, err := runHost(t, h, declineMetadataRule, false, ""); err != nil {
		t.Fatal(err)
	}
	if h.has("install", "-m 0600 /etc/suiteward/github.env") || h.has("rm", "") {
		t.Fatalf("an identical destination needs neither a copy nor a removal: %v", h.lines())
	}
}

func TestPrepareHostPrintsTheNextCommandAndNeverRunsTheServiceStep(t *testing.T) {
	h := newHostFake()
	out, err := runHost(t, h, declineMetadataRule, false, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"sudo -u suiteward -H env XDG_RUNTIME_DIR=/run/user/1001", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1001/bus", " install"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output lacks %q:\n%s", want, out)
		}
	}
	for _, c := range h.calls {
		if c.name == "sudo" || c.name == "su" || c.name == "runuser" {
			t.Fatalf("the service step must never run from the host step: %v", c)
		}
	}
}

func TestPrepareHostRefusesASymlinkWhereRootWouldChownAndCopy(t *testing.T) {
	for _, link := range []string{"/home/suiteward/.config", "/home/suiteward/.config/suiteward", "/home/suiteward/.config/suiteward/github.env"} {
		h := newHostFake()
		h.symlinks[link] = true
		_, err := runHost(t, h, declineMetadataRule, false, "")
		if err == nil || !strings.Contains(err.Error(), "symbolic link") || !strings.Contains(err.Error(), link) {
			t.Fatalf("a symbolic link at %s must stop the run naming it, got %v", link, err)
		}
		if h.has("install", "-m 0700") && link != "/home/suiteward/.config/suiteward/github.env" || h.has("install", "-m 0600 /etc/suiteward/github.env") {
			t.Fatalf("root must not chown or copy through a link at %s: %v", link, h.lines())
		}
	}
}

func TestPrepareHostRestartsAnInstalledRuleUnitOnlyWhenTheRuleChanged(t *testing.T) {
	first := newHostFake()
	first.metadata = "200"
	if _, err := runHost(t, first, map[string]string{"metadata-rule": "yes"}, false, ""); err != nil {
		t.Fatal(err)
	}
	var rule string
	for _, c := range first.callsTo("tee") {
		if strings.HasSuffix(c.line(), "imds-block.nft") {
			rule = c.stdin
		}
	}
	if first.has("systemctl", "restart") {
		t.Fatalf("a first install starts the unit with enable --now, no restart: %v", first.lines())
	}

	changed := newHostFake()
	changed.paths["/etc/systemd/system/suiteward-imds-block.service"] = true
	changed.ruleOnDisk = "table inet suiteward_imds { an older rule }\n"
	if _, err := runHost(t, changed, nil, false, ""); err != nil {
		t.Fatal(err)
	}
	if !changed.has("systemctl", "restart suiteward-imds-block.service") {
		t.Fatalf("enable --now does nothing for a unit that is active, so a changed rule needs a restart: %v", changed.lines())
	}

	same := newHostFake()
	same.paths["/etc/systemd/system/suiteward-imds-block.service"] = true
	same.ruleOnDisk = rule
	if _, err := runHost(t, same, nil, false, ""); err != nil {
		t.Fatal(err)
	}
	if same.has("systemctl", "restart") {
		t.Fatalf("an unchanged rule needs no restart: %v", same.lines())
	}
}

func TestPrepareHostPrintsTheServiceStepWithItsTierAndAnImageOnlyForADevelopmentBuild(t *testing.T) {
	was := ImageDigest
	t.Cleanup(func() { ImageDigest = was })

	ImageDigest = ""
	out, err := runHost(t, newHostFake(), declineMetadataRule, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, " install --tier isolated --image sha256:<signed index digest>") {
		t.Errorf("a development build carries no digest, so the command names the tier and asks for --image:\n%s", out)
	}

	ImageDigest = testDigest
	out, err = runHost(t, newHostFake(), declineMetadataRule, false, "")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, " install --tier isolated\n") {
		t.Errorf("the command must name the tier:\n%s", out)
	}
	if strings.Contains(out, "--image") || strings.Contains(out, "until the release binary") {
		t.Errorf("a release binary carries its own digest and the command must not ask for one:\n%s", out)
	}
}
