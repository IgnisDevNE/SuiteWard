package installer

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// ServiceAccount is the account the isolated stack runs as.
const ServiceAccount = "suiteward"

const (
	metadataAddress = "169.254.169.254"
	imdsUnit        = "suiteward-imds-block.service"
	imdsRuleFile    = "/etc/suiteward/imds-block.nft"
	podmanCVE       = "CVE-2026-57231"
	subidCount      = 65536
	subidFloor      = 100000
	etcEnvFile      = "/etc/suiteward/github.env"
)

var podmanVersionText = regexp.MustCompile(`\d+\.\d+(?:\.\d+)?`)

// host holds what the steps of PrepareHost share. Every command goes through Deps.Exec, so a fake machine tests them.
type host struct {
	r       *Resolver
	d       Deps
	out     io.Writer
	release releaseProgram // this program as the release's SHA256SUMS lists it, checked before anything is changed
}

// PrepareHost is `sudo suiteward install host`, the administrator half of the isolated install (docs/install.md, section 10).
// It checks or installs Podman, creates the service account with its subordinate id ranges, enables linger, offers the rule
// that refuses the cloud metadata address, makes the account's configuration directory and moves a hand-made /etc env file
// into it. It never runs the service-account step: it prints the command for it. It asks before any dnf call (answer key
// "yes") and before the metadata rule (answer key "metadata-rule", yes or no).
func PrepareHost(ctx context.Context, r *Resolver, d Deps, out io.Writer) error {
	if err := d.validate(); err != nil {
		return err
	}
	if d.GOOS != "linux" {
		return fmt.Errorf("install host prepares an AlmaLinux server, but this system is %s. Nothing was changed", d.GOOS)
	}
	h := &host{r: r, d: d, out: out}
	scr := r.screen
	scr.Steps("Podman", "Account", "Metadata", "Executor", "Next")
	uid, err := h.run(ctx, "id", "-u")
	if err != nil {
		return fmt.Errorf("could not find out which user is running the installer: %w", err)
	}
	if uid != "0" {
		return errors.New("install host changes the system, so it must run as root. Run it with sudo, then run the next step as the service account. Nothing was changed")
	}
	if h.release, err = h.verifyRelease(ctx); err != nil {
		return err
	}
	for i, step := range []func(context.Context) error{h.podman, h.account, h.metadata, h.executor, h.configDir} { // the last one and next are step 5
		scr.Step(i + 1)
		if err := step(ctx); err != nil {
			return err
		}
	}
	if err := h.next(ctx); err != nil {
		return err
	}
	return holdPage(scr) // the command to run next is the output: it stays on the screen until it is read
}

func (h *host) say(format string, args ...any) { _, _ = fmt.Fprintf(h.out, format, args...) }

// check reports a step that went well and warn a line that the reader should notice, on the Screen that h.out is.
func (h *host) check(format string, args ...any) { screenOn(h.out).Check(format, args...) }
func (h *host) warn(format string, args ...any)  { screenOn(h.out).Warn(format, args...) }

// run runs a command and returns its trimmed output.
func (h *host) run(ctx context.Context, name string, args ...string) (string, error) {
	out, err := h.d.Exec(ctx, name, args, nil)
	return strings.TrimSpace(string(out)), err
}

func (h *host) exists(ctx context.Context, p string) bool {
	_, err := h.run(ctx, "test", "-e", p)
	return err == nil
}

// dnf runs dnf with the user's consent: it never changes packages on its own.
func (h *host) dnf(ctx context.Context, why string, args ...string) error {
	command := "dnf " + strings.Join(args, " ")
	ok, err := h.r.confirm("yes", why+". Run `"+command+"` now? If you answer no, the step stops and you can run it yourself.")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s. Run `%s` yourself, or pass `--yes` so that this step runs it", why, command)
	}
	if err := working(screenOn(h.out), "Running "+command, func() error { _, err := h.run(ctx, "dnf", args...); return err }); err != nil {
		return fmt.Errorf("`%s` failed: %w", command, err)
	}
	return nil
}

// podman makes sure Podman is there, is 5.5 or later and has the fix for CVE-2026-57231.
func (h *host) podman(ctx context.Context) error {
	out, err := h.run(ctx, "podman", "--version")
	if err != nil {
		if err := h.dnf(ctx, "Podman is not installed", "install", "-y", "podman"); err != nil {
			return err
		}
		if out, err = h.run(ctx, "podman", "--version"); err != nil {
			return fmt.Errorf("Podman still does not run after it was installed: %w", err)
		}
	}
	if !podmanAtLeast(out, 5, 5) {
		if err := h.dnf(ctx, "Podman "+podmanVersionText.FindString(out)+" is older than 5.5", "upgrade", "-y", "podman"); err != nil {
			return err
		}
		if out, err = h.run(ctx, "podman", "--version"); err != nil || !podmanAtLeast(out, 5, 5) {
			return fmt.Errorf("SuiteWard needs Podman 5.5 or later, but this server has %q", out)
		}
	}
	fixed := func() (bool, error) {
		changelog, err := h.run(ctx, "rpm", "-q", "--changelog", "podman")
		return strings.Contains(changelog, podmanCVE), err
	}
	ok, err := fixed()
	if err != nil {
		return fmt.Errorf("could not read the changelog of the Podman package: %w", err)
	}
	if !ok {
		if err := h.dnf(ctx, "this Podman version has no fix for the security flaw "+podmanCVE, "upgrade", "-y", "podman"); err != nil {
			return err
		}
		if ok, err = fixed(); err != nil || !ok {
			return fmt.Errorf("the Podman package still has no fix for the security flaw %s, so SuiteWard must not run on this server. Update Podman, then run this again", podmanCVE)
		}
	}
	h.check("Podman %s, with the fix for %s.\n", podmanVersionText.FindString(out), podmanCVE)
	return nil
}

func podmanAtLeast(versionOutput string, major, minor int) bool {
	parts := strings.Split(podmanVersionText.FindString(versionOutput), ".")
	if len(parts) < 2 {
		return false
	}
	ma, _ := strconv.Atoi(parts[0])
	mi, _ := strconv.Atoi(parts[1])
	return ma > major || ma == major && mi >= minor
}

// account makes the two accounts of the stack: the service account and the executor's. The cgroup controllers that the executor's limits need
// are delegated first, before any account lingers: a user manager that starts afterwards has them, and restarting one stops its containers.
func (h *host) account(ctx context.Context) error {
	if err := h.delegation(ctx); err != nil {
		return err
	}
	for _, name := range []string{ServiceAccount, ExecutorAccount} {
		if err := h.ensureAccount(ctx, name); err != nil {
			return err
		}
	}
	return h.executorManager(ctx)
}

// ensureAccount creates the account if it is missing, gives it subordinate id ranges no other account uses, and enables linger so its
// services start at boot and run without a login.
func (h *host) ensureAccount(ctx context.Context, name string) error {
	if _, err := h.run(ctx, "id", "-u", name); err != nil {
		if _, err := h.run(ctx, "useradd", "--create-home", "--shell", "/usr/sbin/nologin", name); err != nil {
			return fmt.Errorf("could not create the account %s: %w", name, err)
		}
		h.check("Created the account %s with no login shell.\n", name)
	}
	var add []string
	for _, f := range []struct{ file, flag string }{{"/etc/subuid", "--add-subuids"}, {"/etc/subgid", "--add-subgids"}} {
		raw, err := h.run(ctx, "cat", f.file)
		if err != nil {
			return fmt.Errorf("read %s: %w", f.file, err)
		}
		start, has, err := freeSubidRange(raw, name)
		if err != nil {
			return fmt.Errorf("%s: %w", f.file, err)
		}
		if !has {
			add = append(add, f.flag, fmt.Sprintf("%d-%d", start, start+subidCount-1))
		}
	}
	if len(add) > 0 {
		if _, err := h.run(ctx, "usermod", append(add, name)...); err != nil {
			return fmt.Errorf("could not add the id ranges (subuid and subgid) for the account: %w", err)
		}
		h.check("Added the id ranges (subuid and subgid) for the account: %s.\n", strings.Join(add, " "))
	}
	if _, err := h.run(ctx, "loginctl", "enable-linger", name); err != nil {
		return fmt.Errorf("could not turn on linger for %s (keeping its services running after logout): %w", name, err)
	}
	return nil
}

// freeSubidRange reads a subuid or subgid file: has says whether account has a range, and start is the first id after every
// range in the file (at least subidFloor), where a new range of subidCount ids overlaps none.
func freeSubidRange(content, account string) (start int, has bool, err error) {
	start = subidFloor
	for _, line := range strings.Split(content, "\n") {
		if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, ":")
		if len(fields) != 3 {
			return 0, false, fmt.Errorf("cannot read the line %q", line)
		}
		first, err1 := strconv.Atoi(fields[1])
		count, err2 := strconv.Atoi(fields[2])
		if err1 != nil || err2 != nil {
			return 0, false, fmt.Errorf("cannot read the line %q", line)
		}
		has = has || fields[0] == account
		start = max(start, first+count)
	}
	return start, has, nil
}

// metadata offers the rule that refuses the cloud metadata address for the service account, only on a
// host where that address answers, and never without an explicit yes. A rule that is already installed is applied again.
func (h *host) metadata(ctx context.Context) error {
	installed := h.exists(ctx, "/etc/systemd/system/"+imdsUnit)
	if !installed {
		if _, err := h.run(ctx, "curl", "--version"); err != nil {
			return fmt.Errorf("curl is needed to look for a cloud metadata service, and it does not run: %w", err)
		}
		// A timeout or a refusal makes curl fail: nothing answers there.
		code, err := h.run(ctx, "curl", "-s", "-m", "3", "-X", "PUT", "-H", "X-aws-ec2-metadata-token-ttl-seconds: 60",
			"-o", "/dev/null", "-w", "%{http_code}", "http://"+metadataAddress+"/latest/api/token")
		if answered := err == nil && code != "000"; !answered {
			h.check("Nothing answers at %s, so no metadata rule is needed.\n", metadataAddress)
			return nil
		}
		ok, err := h.r.confirm("metadata-rule", "A cloud metadata service answers at "+metadataAddress+". It hands out this server's cloud credentials, so SuiteWard must not be able to reach it. Add a firewall rule that blocks it for the "+ServiceAccount+" account (--metadata-rule yes|no)?")
		if err != nil {
			return err
		}
		if !ok {
			h.warn("No firewall rule was added, so containers of the account %s can reach %s. Doctor reports this as a failure until the rule is added.\n", ServiceAccount, metadataAddress)
			return nil
		}
	}
	return h.installMetadataRule(ctx, installed)
}

// nftSetup is a rule that refuses addresses for one account: the table it makes, its file and the unit that loads it at every boot.
type nftSetup struct {
	label       string   // what the rule is called in an error: "metadata" or "executor"
	table       string   // the nft table, inet
	ruleFile    string   // where the rule lives
	unit        string   // the oneshot unit that loads it
	rule        string   // the text of the rule file
	addresses   []string // each must be refused in the loaded table
	consequence string   // what must not run on this host until the rule is loaded
	description string   // the unit's description
	refusedLine string   // the line that says the rule is loaded
}

// installMetadataRule writes the rule and its unit and loads them. installed says the unit was there before: enable --now
// does nothing for a unit that is already active, so a rule file that changed needs a restart to be loaded.
func (h *host) installMetadataRule(ctx context.Context, installed bool) error {
	return h.installNft(ctx, nftSetup{
		label: "metadata", table: "suiteward_imds", ruleFile: imdsRuleFile, unit: imdsUnit,
		rule: fmt.Sprintf(`add table inet suiteward_imds
delete table inet suiteward_imds
table inet suiteward_imds {
  chain output {
    type filter hook output priority 0; policy accept;
    meta skuid %s ip daddr %s reject
  }
}
`, ServiceAccount, metadataAddress),
		addresses: []string{metadataAddress}, consequence: "the service must not run on this server until the rule is loaded",
		description: "Reject the instance metadata service for the " + ServiceAccount + " user",
		refusedLine: fmt.Sprintf("Blocked %s for the account %s with a firewall rule (it loads at every boot).\n", metadataAddress, ServiceAccount),
	}, installed)
}

func (h *host) installNft(ctx context.Context, s nftSetup, installed bool) error {
	nft, err := h.run(ctx, "sh", "-c", "command -v nft")
	if err != nil {
		if err := h.dnf(ctx, "nft (the Linux firewall tool) is not installed", "install", "-y", "nftables"); err != nil {
			return err
		}
		if nft, err = h.run(ctx, "sh", "-c", "command -v nft"); err != nil {
			return fmt.Errorf("nft (the Linux firewall tool) is still missing after the installation: %w", err)
		}
	}
	unit := fmt.Sprintf(`[Unit]
Description=%[1]s
Before=systemd-user-sessions.service
After=nftables.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=%[2]s -f %[3]s
ExecStop=-%[2]s delete table inet %[4]s

[Install]
WantedBy=multi-user.target
`, s.description, nft, s.ruleFile, s.table)
	onDisk, readErr := h.run(ctx, "cat", s.ruleFile)
	ruleChanged := readErr != nil || onDisk != strings.TrimSpace(s.rule)
	if err := h.makeDir(ctx, path.Dir(s.ruleFile)); err != nil {
		return err
	}
	for file, content := range map[string]string{s.ruleFile: s.rule, "/etc/systemd/system/" + s.unit: unit} {
		if err := h.writeReadable(ctx, file, content); err != nil {
			return err
		}
	}
	steps := [][]string{{"daemon-reload"}, {"enable", "--now", s.unit}}
	if installed && ruleChanged {
		steps = append(steps, []string{"restart", s.unit})
	}
	for _, args := range steps {
		if _, err := h.run(ctx, "systemctl", args...); err != nil {
			return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
		}
	}
	table, err := h.run(ctx, "nft", "list", "table", "inet", s.table)
	if err != nil {
		return fmt.Errorf("the %s rule (the firewall rule) is not loaded: %w", s.label, err)
	}
	for _, address := range s.addresses {
		if !strings.Contains(table, "reject") || !strings.Contains(table, address) {
			return fmt.Errorf("the firewall table %s has no reject rule for %s, and %s", s.table, address, s.consequence)
		}
	}
	h.check("%s", s.refusedLine)
	return nil
}

// accountHome is the home directory of the service account.
func (h *host) accountHome(ctx context.Context) (string, error) {
	entry, err := h.run(ctx, "getent", "passwd", ServiceAccount)
	if err != nil {
		return "", fmt.Errorf("look up the account %s: %w", ServiceAccount, err)
	}
	fields := strings.Split(entry, ":")
	if len(fields) < 6 || !path.IsAbs(fields[5]) {
		return "", fmt.Errorf("the account entry %q has no home directory", entry)
	}
	return fields[5], nil
}

// configDir makes ~/.config/suiteward owned by the account, where the service-account step writes the GitHub env file, and
// copies a hand-made /etc/suiteward/github.env there.
func (h *host) configDir(ctx context.Context) error {
	home, err := h.accountHome(ctx)
	if err != nil {
		return err
	}
	dotConfig := path.Join(home, ".config")
	dir := path.Join(dotConfig, "suiteward")
	dest := path.Join(dir, "github.env")
	// The account owns these directories, so it could have planted a link where root is about to chown or write.
	for _, p := range []string{dotConfig, dir, dest} {
		if _, err := h.run(ctx, "test", "-L", p); err == nil {
			return fmt.Errorf("%s is a shortcut (symbolic link), and the installer will not write through it as root. Remove the link, then run this again", p)
		}
	}
	// install -d owns only the last component it names, so both directories are named.
	if _, err := h.run(ctx, "install", "-d", "-o", ServiceAccount, "-g", ServiceAccount, "-m", "0700", dotConfig, dir); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if err := h.executorHandover(ctx, dir); err != nil {
		return err
	}
	if !h.exists(ctx, etcEnvFile) {
		return nil
	}
	if h.exists(ctx, dest) {
		if _, err := h.run(ctx, "cmp", "-s", etcEnvFile, dest); err != nil {
			return fmt.Errorf("%[1]s and %[2]s both exist and are different. Edit %[2]s so that it holds the values you want, because the service reads that one, then run this again", etcEnvFile, dest)
		}
		return nil
	}
	if _, err := h.run(ctx, "install", "-o", ServiceAccount, "-g", ServiceAccount, "-m", "0600", etcEnvFile, dest); err != nil {
		return fmt.Errorf("copy %s: %w", etcEnvFile, err)
	}
	// The source stays: a running hand-installed service reads it and could not restart until the service step re-renders
	// its units. The service step says when it may be removed.
	h.check("Copied %s to %s, owned by %s. The old file stays until the service step has moved the units to the new one.\n", etcEnvFile, dest, ServiceAccount)
	return nil
}

// next prints the command for the service-account step. The host step never runs it: that step must not run as root.
func (h *host) next(ctx context.Context) error {
	uid, err := h.run(ctx, "id", "-u", ServiceAccount)
	if err != nil {
		return fmt.Errorf("could not look up the user id of %s: %w", ServiceAccount, err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("could not find this program's own file: %w", err)
	}
	command := exe + " install --tier isolated"
	_, carriesRelease := ReleaseImage()
	if !carriesRelease {
		command += " --image sha256:<signed index digest>"
	}
	h.say("\nThe host is ready. Next, run the installer as the service account. The account has no login shell, so this command opens one with the settings it needs:\n\n")
	h.say("  sudo -u %s -H env XDG_RUNTIME_DIR=/run/user/%s DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/%s/bus %s\n\n", ServiceAccount, uid, uid, command)
	h.say("The executor (the test runner) already runs as %s and waits for the service. It uses the HTTP port named in the service account's settings, else %d. If the service will use another HTTP port, run this step again with `--http-port <port>`. Keep one executor for each container engine: when it starts, it removes every %s container of its engine, so a second one would remove the first one's sandboxes.\n\n", ExecutorAccount, DefaultHTTPPort, sandboxNamePrefix)
	h.say("The account must be able to run %s", exe)
	if strings.HasPrefix(exe, "/root/") || strings.HasPrefix(exe, "/home/") {
		h.say(": that path is inside a home directory, so copy the program to a shared place such as /usr/local/bin first")
	}
	if carriesRelease {
		h.say(".\n")
		return nil
	}
	h.say(". The `--image` flag is needed because this program carries no release fingerprint (it is a development build).\n")
	return nil
}

// yesNo are the answers of a yes or no question on a full screen: No is the default, so that Enter never says yes.
var yesNo = []ui.Choice{{Name: "Yes"}, {Name: "No"}}

// confirm asks a yes or no question: the flag, then the answers file, then a prompt (a choice between Yes and No on a full screen). It
// never assumes a yes, and a run that cannot ask (stdin is not a terminal) and has no answer fails.
func (r *Resolver) confirm(key, question string) (bool, error) {
	v, ok := r.flags[key]
	if !ok {
		v, ok = r.answers[key]
	}
	if ok {
		return parseYesNo(key, v)
	}
	if !r.term.IsTerminal {
		return false, fmt.Errorf("%s\nNothing can ask you here, because the input is not a terminal. Pass `--%s`, or set `%s` in the answers file", question, key, key)
	}
	if r.screen.FullScreen() {
		picked, err := r.screen.Select(question, yesNo, 1)
		if err != nil {
			return false, fmt.Errorf("could not read your answer for %s: %w", key, err)
		}
		return picked == 0, nil
	}
	line, err := r.screen.Prompt(question + " [y/N]: ")
	if err != nil {
		return false, fmt.Errorf("could not read your answer for %s: %w", key, err)
	}
	if line == "" {
		return false, nil
	}
	return parseYesNo(key, line)
}

func parseYesNo(key, v string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "y", "yes", "true", "1":
		return true, nil
	case "n", "no", "false", "0":
		return false, nil
	}
	return false, fmt.Errorf("%s: %q is not yes or no", key, v)
}
