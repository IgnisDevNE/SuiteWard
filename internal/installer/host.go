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
	r   *Resolver
	d   Deps
	out io.Writer
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
		return fmt.Errorf("install host prepares an AlmaLinux host; this system is %s", d.GOOS)
	}
	h := &host{r: r, d: d, out: out}
	uid, err := h.run(ctx, "id", "-u")
	if err != nil {
		return fmt.Errorf("find out who is running the installer: %w", err)
	}
	if uid != "0" {
		return errors.New("install host changes the system and must run as root: run it with sudo, then run the service-account step as the service account")
	}
	for _, step := range []func(context.Context) error{h.podman, h.account, h.metadata, h.configDir} {
		if err := step(ctx); err != nil {
			return err
		}
	}
	return h.next(ctx)
}

func (h *host) say(format string, args ...any) { _, _ = fmt.Fprintf(h.out, format, args...) }

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
	ok, err := h.r.confirm("yes", why+": run `"+command+"`?")
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("%s; run `%s` yourself, or pass --yes", why, command)
	}
	if _, err := h.run(ctx, "dnf", args...); err != nil {
		return fmt.Errorf("%s: %w", command, err)
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
			return fmt.Errorf("podman still does not run after the installation: %w", err)
		}
	}
	if !podmanAtLeast(out, 5, 5) {
		if err := h.dnf(ctx, "Podman "+podmanVersionText.FindString(out)+" is older than 5.5", "upgrade", "-y", "podman"); err != nil {
			return err
		}
		if out, err = h.run(ctx, "podman", "--version"); err != nil || !podmanAtLeast(out, 5, 5) {
			return fmt.Errorf("SuiteWard needs Podman 5.5 or later, this host has %q", out)
		}
	}
	fixed := func() (bool, error) {
		changelog, err := h.run(ctx, "rpm", "-q", "--changelog", "podman")
		return strings.Contains(changelog, podmanCVE), err
	}
	ok, err := fixed()
	if err != nil {
		return fmt.Errorf("read the changelog of the podman package: %w", err)
	}
	if !ok {
		if err := h.dnf(ctx, "this Podman has no fix for "+podmanCVE, "upgrade", "-y", "podman"); err != nil {
			return err
		}
		if ok, err = fixed(); err != nil || !ok {
			return fmt.Errorf("the podman package has no fix for %s, so SuiteWard must not run on this host: update Podman and run this again", podmanCVE)
		}
	}
	h.say("Podman %s, with the fix for %s.\n", podmanVersionText.FindString(out), podmanCVE)
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

// account creates the service account if it is missing, gives it subordinate id ranges no other account uses, and enables
// linger so its services start at boot and run without a login.
func (h *host) account(ctx context.Context) error {
	if _, err := h.run(ctx, "id", "-u", ServiceAccount); err != nil {
		if _, err := h.run(ctx, "useradd", "--create-home", "--shell", "/usr/sbin/nologin", ServiceAccount); err != nil {
			return fmt.Errorf("create the account %s: %w", ServiceAccount, err)
		}
		h.say("Created the account %s with no login shell.\n", ServiceAccount)
	}
	var add []string
	for _, f := range []struct{ file, flag string }{{"/etc/subuid", "--add-subuids"}, {"/etc/subgid", "--add-subgids"}} {
		raw, err := h.run(ctx, "cat", f.file)
		if err != nil {
			return fmt.Errorf("read %s: %w", f.file, err)
		}
		start, has, err := freeSubidRange(raw, ServiceAccount)
		if err != nil {
			return fmt.Errorf("%s: %w", f.file, err)
		}
		if !has {
			add = append(add, f.flag, fmt.Sprintf("%d-%d", start, start+subidCount-1))
		}
	}
	if len(add) > 0 {
		if _, err := h.run(ctx, "usermod", append(add, ServiceAccount)...); err != nil {
			return fmt.Errorf("add the subordinate id ranges: %w", err)
		}
		h.say("Added the subordinate id ranges %s.\n", strings.Join(add, " "))
	}
	if _, err := h.run(ctx, "loginctl", "enable-linger", ServiceAccount); err != nil {
		return fmt.Errorf("enable linger for %s: %w", ServiceAccount, err)
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
			return fmt.Errorf("curl is needed to look for a cloud metadata service: %w", err)
		}
		// A timeout or a refusal makes curl fail: nothing answers there.
		code, err := h.run(ctx, "curl", "-s", "-m", "3", "-X", "PUT", "-H", "X-aws-ec2-metadata-token-ttl-seconds: 60",
			"-o", "/dev/null", "-w", "%{http_code}", "http://"+metadataAddress+"/latest/api/token")
		if answered := err == nil && code != "000"; !answered {
			h.say("Nothing answers at %s, so no metadata rule is needed.\n", metadataAddress)
			return nil
		}
		ok, err := h.r.confirm("metadata-rule", "A metadata service answers at "+metadataAddress+" and hands out the instance's credentials. Add the nft rule that refuses it for the "+ServiceAccount+" account (--metadata-rule yes|no)")
		if err != nil {
			return err
		}
		if !ok {
			h.say("No metadata rule installed: containers of %s can reach %s. Doctor reports this.\n", ServiceAccount, metadataAddress)
			return nil
		}
	}
	return h.installMetadataRule(ctx, installed)
}

// installMetadataRule writes the rule and its unit and loads them. installed says the unit was there before: enable --now
// does nothing for a unit that is already active, so a rule file that changed needs a restart to be loaded.
func (h *host) installMetadataRule(ctx context.Context, installed bool) error {
	nft, err := h.run(ctx, "sh", "-c", "command -v nft")
	if err != nil {
		if err := h.dnf(ctx, "nft is not installed", "install", "-y", "nftables"); err != nil {
			return err
		}
		if nft, err = h.run(ctx, "sh", "-c", "command -v nft"); err != nil {
			return fmt.Errorf("nft is still missing after the installation: %w", err)
		}
	}
	rule := fmt.Sprintf(`add table inet suiteward_imds
delete table inet suiteward_imds
table inet suiteward_imds {
  chain output {
    type filter hook output priority 0; policy accept;
    meta skuid %s ip daddr %s reject
  }
}
`, ServiceAccount, metadataAddress)
	unit := fmt.Sprintf(`[Unit]
Description=Reject the instance metadata service for the %[1]s user
Before=systemd-user-sessions.service
After=nftables.service

[Service]
Type=oneshot
RemainAfterExit=yes
ExecStart=%[2]s -f %[3]s
ExecStop=-%[2]s delete table inet suiteward_imds

[Install]
WantedBy=multi-user.target
`, ServiceAccount, nft, imdsRuleFile)
	onDisk, readErr := h.run(ctx, "cat", imdsRuleFile)
	ruleChanged := readErr != nil || onDisk != strings.TrimSpace(rule)
	if _, err := h.run(ctx, "mkdir", "-p", path.Dir(imdsRuleFile)); err != nil {
		return err
	}
	for file, content := range map[string]string{imdsRuleFile: rule, "/etc/systemd/system/" + imdsUnit: unit} {
		if _, err := h.d.Exec(ctx, "tee", []string{file}, strings.NewReader(content)); err != nil {
			return fmt.Errorf("write %s: %w", file, err)
		}
	}
	steps := [][]string{{"daemon-reload"}, {"enable", "--now", imdsUnit}}
	if installed && ruleChanged {
		steps = append(steps, []string{"restart", imdsUnit})
	}
	for _, args := range steps {
		if _, err := h.run(ctx, "systemctl", args...); err != nil {
			return fmt.Errorf("systemctl %s: %w", strings.Join(args, " "), err)
		}
	}
	table, err := h.run(ctx, "nft", "list", "table", "inet", "suiteward_imds")
	if err != nil {
		return fmt.Errorf("the metadata rule is not loaded: %w", err)
	}
	if !strings.Contains(table, "reject") || !strings.Contains(table, metadataAddress) {
		return fmt.Errorf("the table suiteward_imds has no reject rule for %s: the service must not run on this host until it does", metadataAddress)
	}
	h.say("Refusing %s for the account %s (the rule loads at every boot).\n", metadataAddress, ServiceAccount)
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
			return fmt.Errorf("%s is a symbolic link: root will not chown or copy through it; remove the link and run this again", p)
		}
	}
	// install -d owns only the last component it names, so both directories are named.
	if _, err := h.run(ctx, "install", "-d", "-o", ServiceAccount, "-g", ServiceAccount, "-m", "0700", dotConfig, dir); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	if !h.exists(ctx, etcEnvFile) {
		return nil
	}
	if h.exists(ctx, dest) {
		if _, err := h.run(ctx, "cmp", "-s", etcEnvFile, dest); err != nil {
			return fmt.Errorf("%s and %s both exist and differ: make the new one hold the values you want (it is the one the service will read), and run this again", etcEnvFile, dest)
		}
		return nil
	}
	if _, err := h.run(ctx, "install", "-o", ServiceAccount, "-g", ServiceAccount, "-m", "0600", etcEnvFile, dest); err != nil {
		return fmt.Errorf("copy %s: %w", etcEnvFile, err)
	}
	// The source stays: a running hand-installed service reads it and could not restart until the service step re-renders
	// its units. The service step says when it may be removed.
	h.say("Copied %s to %s, owned by %s. The old file stays until the service step has moved the units to the new one.\n", etcEnvFile, dest, ServiceAccount)
	return nil
}

// next prints the command for the service-account step. The host step never runs it: that step must not run as root.
func (h *host) next(ctx context.Context) error {
	uid, err := h.run(ctx, "id", "-u", ServiceAccount)
	if err != nil {
		return fmt.Errorf("look up the id of %s: %w", ServiceAccount, err)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("find this program: %w", err)
	}
	command := exe + " install --tier isolated"
	_, carriesRelease := ReleaseImage()
	if !carriesRelease {
		command += " --image sha256:<signed index digest>"
	}
	h.say("\nThe host is ready. Now run the installer as the service account, in a shell with its user bus (the account has no login shell):\n\n")
	h.say("  sudo -u %s -H env XDG_RUNTIME_DIR=/run/user/%s DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/%s/bus %s\n\n", ServiceAccount, uid, uid, command)
	h.say("The account must be able to run %s", exe)
	if strings.HasPrefix(exe, "/root/") || strings.HasPrefix(exe, "/home/") {
		h.say(": that path is inside a home directory, so copy the binary to a shared place such as /usr/local/bin first")
	}
	if carriesRelease {
		h.say(".\n")
		return nil
	}
	h.say(". The --image flag is needed because this binary carries no release digest (it is a development build).\n")
	return nil
}

// confirm asks a yes or no question: the flag, then the answers file, then a prompt. It never assumes a yes, and a run
// that cannot ask (stdin is not a terminal) and has no answer fails.
func (r *Resolver) confirm(key, question string) (bool, error) {
	v, ok := r.flags[key]
	if !ok {
		v, ok = r.answers[key]
	}
	if ok {
		return parseYesNo(key, v)
	}
	if !r.term.IsTerminal {
		return false, fmt.Errorf("%s (pass --%s, or set %q in the answers file: stdin is not a terminal, so it cannot be asked)", question, key, key)
	}
	r.say("%s [y/N]: ", question)
	line, err := r.in.ReadString('\n')
	if err != nil && (!errors.Is(err, io.EOF) || line == "") {
		return false, fmt.Errorf("read the answer for %s: %w", key, err)
	}
	if strings.TrimSpace(line) == "" {
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
