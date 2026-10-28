package installer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
)

// The executor's part of `suiteward install host` (ADR 0028, section 10; spike S4, sections 4.1, 4.5 and 4.6). Every command goes through
// Deps.Exec, as the rest of the host step does, so that a fake machine tests it.

// makeDir makes a directory that other accounts read: root's umask may be 077, which sudo keeps, and a directory made under it is 0700.
func (h *host) makeDir(ctx context.Context, dir string) error {
	if _, err := h.run(ctx, "mkdir", "-p", dir); err != nil {
		return err
	}
	if _, err := h.run(ctx, "chmod", "0755", dir); err != nil {
		return fmt.Errorf("could not make %s readable by other accounts: %w", dir, err)
	}
	return nil
}

// writeReadable writes a file that other accounts read (the manager of the executor reads its unit, the service account's doctor reads the rest) and
// makes it 0644, which a root umask of 077 would have made 0600.
func (h *host) writeReadable(ctx context.Context, file, content string) error {
	if _, err := h.d.Exec(ctx, "tee", []string{file}, strings.NewReader(content)); err != nil {
		return fmt.Errorf("write %s: %w", file, err)
	}
	if _, err := h.run(ctx, "chmod", "0644", file); err != nil {
		return fmt.Errorf("could not make %s readable by other accounts: %w", file, err)
	}
	return nil
}

// delegation writes the drop-in that gives user managers the cgroup controllers that the executor's limits need (a manager gets only memory
// and pids on EL9, and `podman run --cpus` fails without cpu), and has systemd read it.
func (h *host) delegation(ctx context.Context) error {
	if on, err := h.run(ctx, "cat", executorDropIn); err == nil && on == strings.TrimSpace(executorDropInText) {
		return nil
	}
	if err := h.makeDir(ctx, path.Dir(executorDropIn)); err != nil {
		return err
	}
	if err := h.writeReadable(ctx, executorDropIn, executorDropInText); err != nil {
		return err
	}
	if _, err := h.run(ctx, "systemctl", "daemon-reload"); err != nil {
		return fmt.Errorf("systemctl daemon-reload: %w", err)
	}
	h.check("Let user accounts limit the CPU, memory and processes of their containers (cgroup controllers cpu, cpuset, io, memory and pids, in %s).\n", executorDropIn)
	return nil
}

// executorManager starts the user manager of the executor and checks that it has the controllers. A manager that was already up without cpu
// (the drop-in is newer than it) is restarted: it holds no container of the stack. The manager of the service account never is.
func (h *host) executorManager(ctx context.Context) error {
	uid, err := h.run(ctx, "id", "-u", ExecutorAccount)
	if err != nil {
		return fmt.Errorf("could not look up the user id of %s: %w", ExecutorAccount, err)
	}
	unit := "user@" + uid + ".service"
	if _, err := h.run(ctx, "systemctl", "start", unit); err != nil {
		return fmt.Errorf("systemctl start %s: %w", unit, err)
	}
	hasCPU := func() (bool, error) {
		out, err := h.run(ctx, "cat", "/sys/fs/cgroup/user.slice/user-"+uid+".slice/"+unit+"/cgroup.controllers")
		if err != nil {
			return false, fmt.Errorf("could not read the cgroup controllers of %s: %w", unit, err)
		}
		return slices.Contains(strings.Fields(out), "cpu"), nil
	}
	ok, err := hasCPU()
	if err != nil {
		return err
	}
	if !ok {
		if _, err := h.run(ctx, "systemctl", "restart", unit); err != nil {
			return fmt.Errorf("systemctl restart %s: %w", unit, err)
		}
		if ok, err = hasCPU(); err != nil {
			return err
		}
	}
	if !ok {
		return fmt.Errorf("the user manager of %s has no cpu controller, even after a restart. The test runner limits the CPU of each sandbox, which needs cgroup v2 with cpu delegated. Check %s and `cat /sys/fs/cgroup/cgroup.controllers`", ExecutorAccount, executorDropIn)
	}
	return nil
}

// executor sets up the executor on the host: the rule that refuses it the metadata addresses first, then its token, its binary and its unit.
func (h *host) executor(ctx context.Context) error {
	uid, err := h.run(ctx, "id", "-u", ExecutorAccount)
	if err != nil {
		return fmt.Errorf("could not look up the user id of %s: %w", ExecutorAccount, err)
	}
	port, err := h.executorPort(ctx) // before anything of the executor is changed: a port that cannot be read stops here
	if err != nil {
		return err
	}
	if err := h.executorRules(ctx, uid); err != nil {
		return err
	}
	// The image first: it depends on neither the token nor the binary, and a run that stops here has changed nothing. What a run changed before it
	// stopped is not seen again by the next one (the binary then matches, the unit is unchanged), so a running executor would never be restarted.
	if err := h.executorImage(ctx, uid); err != nil {
		return err
	}
	tokenMade, err := h.executorToken(ctx)
	if err != nil {
		return err
	}
	replaced, err := h.executorBinary(ctx)
	if err != nil {
		return err
	}
	return h.executorUnit(ctx, port, replaced || tokenMade)
}

// runAsExecutor runs a command as the executor's account, through its own user manager: it comes with the account's HOME and XDG_RUNTIME_DIR, in the cgroup
// the drop-in delegates, which `runuser` from root's session does not give. --wait is for the end of the command and carries its exit status. The
// transient unit has a name that systemd picks: a fixed one would collide with a unit that a run cut short left, or that was not collected yet.
func (h *host) runAsExecutor(ctx context.Context, argv ...string) (string, error) {
	return h.run(ctx, "systemd-run", append([]string{"--user", "--machine=" + ExecutorAccount + "@.host", "--wait", "--collect"}, argv...)...)
}

// executorImage pulls the Go image of the sandboxes into the rootless storage of the executor's account and checks that it is there under its
// digest. The executor starts every sandbox with --pull=never, so an account without the image ends every job as unavailable; no other account can
// read that storage, which is why this step does it and the service account's doctor cannot. It runs at every host step: a new release names a new digest.
func (h *host) executorImage(ctx context.Context, uid string) error {
	if _, err := h.runAsExecutor(ctx, "podman", "pull", goprofile.Image); err != nil {
		return fmt.Errorf("could not download (pull) the image %s as %s: %w. To see why, run `journalctl _UID=%s -n 50 --no-pager`", goprofile.Image, ExecutorAccount, err, uid)
	}
	if _, err := h.runAsExecutor(ctx, "podman", "image", "exists", goprofile.Image); err != nil { // a reference with a digest exists only when an image has that digest
		return fmt.Errorf("after the download, the image %s is not in the storage of %s under that fingerprint (digest): %w", goprofile.Image, ExecutorAccount, err)
	}
	h.check("The Go image of the sandboxes (where tests run) is stored for %s, under its fingerprint (digest).\n", ExecutorAccount)
	return nil
}

// executorRulesText is the nft file that refuses both metadata addresses, by destination and with no port, for every connection of the uid:
// the executor's own and those of its containers, which rootless networking makes from a process of that account.
func executorRulesText(uid string) string {
	return fmt.Sprintf(`# %[1]s (uid %[2]s) may reach neither instance-metadata address, on any port.
add table inet %[3]s
delete table inet %[3]s
table inet %[3]s {
  chain output {
    type filter hook output priority 0; policy accept;
    meta skuid %[2]s ip daddr %[4]s reject
    meta skuid %[2]s ip6 daddr %[5]s reject
  }
}
`, ExecutorAccount, uid, executorTable, metadataAddress, metadataAddress6)
}

// executorRules installs that rule. It is not asked about: the executor runs what a pull request brings, and the rule costs nothing on a host
// where nothing answers at those addresses.
func (h *host) executorRules(ctx context.Context, uid string) error {
	installed := h.exists(ctx, "/etc/systemd/system/"+executorBlockUnit)
	return h.installNft(ctx, nftSetup{
		label: "test runner", table: executorTable, ruleFile: executorRuleFile, unit: executorBlockUnit, rule: executorRulesText(uid),
		addresses: []string{metadataAddress, metadataAddress6}, consequence: "the test runner must not run on this server until the rule is loaded",
		description: "Reject the instance metadata service for the " + ExecutorAccount + " user",
		refusedLine: fmt.Sprintf("Blocked %s and %s for the account %s, on every port, with a firewall rule (it loads at every boot).\n", metadataAddress, metadataAddress6, ExecutorAccount),
	}, installed)
}

// executorToken makes the executor's token once: a file that only the executor's account reads. It is written through stdin under a
// restrictive umask and then given its owner, so it is never wider than it ends up. An existing token is never made again; its owner and
// mode are put right. made says that there is a new token: a running executor reads its token only when it starts, so it is restarted.
func (h *host) executorToken(ctx context.Context) (made bool, err error) {
	if !h.exists(ctx, executorAuthEtc) {
		token, err := NewExecutorToken()
		if err != nil {
			return false, err
		}
		if _, err := h.d.Exec(ctx, "sh", []string{"-c", `umask 077; cat > "$1"`, "sh", executorAuthEtc}, strings.NewReader(token)); err != nil {
			return false, fmt.Errorf("write %s: %w", executorAuthEtc, err)
		}
		h.check("Made the test runner's token in %s. Only the account %s can read it.\n", executorAuthEtc, ExecutorAccount)
		made = true
	}
	if _, err := h.run(ctx, "chown", ExecutorAccount, executorAuthEtc); err != nil {
		return false, fmt.Errorf("could not give %s to %s: %w", executorAuthEtc, ExecutorAccount, err)
	}
	if _, err := h.run(ctx, "chmod", "0400", executorAuthEtc); err != nil {
		return false, fmt.Errorf("could not restrict %s: %w", executorAuthEtc, err)
	}
	return made, nil
}

// releaseAssetOf is the pattern of the name of a binary of goos in the release's SHA256SUMS: suiteward_<tag>_<os>_<arch>, with .exe on Windows.
func releaseAssetOf(goos string) *regexp.Regexp {
	suffix := ""
	if goos == "windows" {
		suffix = `\.exe`
	}
	return regexp.MustCompile(`^suiteward_v[0-9][^_\s]*_` + regexp.QuoteMeta(goos) + `_(amd64|arm64)` + suffix + `$`)
}

// pinFromSums is the line of the release's SHA256SUMS that lists sum as a binary of goos, which is the pin of the executor's binary. The program
// must be listed exactly once: one that is not is not the release's.
func pinFromSums(sums, sum, sumsFile, goos string) (string, error) {
	asset := releaseAssetOf(goos)
	var found []string
	for _, line := range strings.Split(sums, "\n") {
		hash, name, ok := strings.Cut(strings.TrimSpace(line), " ")
		name = strings.TrimPrefix(strings.TrimSpace(name), "*")
		if ok && hash == sum && asset.MatchString(name) {
			found = append(found, hash+"  "+name)
		}
	}
	if len(found) != 1 {
		return "", fmt.Errorf("this program's fingerprint (SHA-256 %s) is not listed exactly once as a %s program in %s, so it is not an official release and is not installed as the test runner. Verify the download as the install guide says, and give the SHA256SUMS file of that release. Nothing was changed", sum, goos, sumsFile)
	}
	return found[0], nil
}

// hashFile is the SHA-256 of a file, as lowercase hex.
func hashFile(file string) (string, error) {
	f, err := os.Open(filepath.Clean(file))
	if err != nil {
		return "", fmt.Errorf("read %s: %w", file, err)
	}
	sum := sha256.New()
	_, copyErr := io.Copy(sum, f)
	if err := errors.Join(copyErr, f.Close()); err != nil {
		return "", fmt.Errorf("read %s: %w", file, err)
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// installedHash is the SHA-256 that `sha256sum` prints for an installed file; empty when it is not there.
func (h *host) installedHash(ctx context.Context, file string) string {
	out, err := h.run(ctx, "sha256sum", file)
	if err != nil {
		return ""
	}
	fields := strings.Fields(out)
	if len(fields) == 0 {
		return ""
	}
	return fields[0]
}

// releaseProgram is this program as the release's SHA256SUMS lists it: where it is, its SHA-256, and the line of the sums that is the executor's pin.
type releaseProgram struct{ exe, sum, pin string }

// verifyRelease requires the release's SHA256SUMS (--sha256sums) and checks, before the step changes anything, that it lists this program exactly once
// as a Linux binary: the executor's pin is the signed release's line, not only the hash of the program that happened to be copied. A missing flag is
// a mistake of the command line (ErrUsage).
func (h *host) verifyRelease(ctx context.Context) (releaseProgram, error) {
	sumsFile, ok := h.r.given("sha256sums")
	if ok && strings.TrimSpace(sumsFile) == "" {
		return releaseProgram{}, fmt.Errorf("%w: install host needs the SHA256SUMS file of the release this program was downloaded from, but `--sha256sums` is empty (an unset variable?)", ErrUsage)
	}
	if !ok {
		return releaseProgram{}, fmt.Errorf("%w: install host needs `--sha256sums FILE`, the SHA256SUMS file of the release this program was downloaded from (it sits beside the programs on that GitHub Release; verify it as the install guide says). The test runner runs code from pull requests, so its program must be an official release, which that file lists", ErrUsage)
	}
	if h.d.Executable == nil {
		return releaseProgram{}, errors.New("the installer cannot find its own program file, so it cannot copy it as the test runner")
	}
	exe, err := h.d.Executable()
	if err != nil {
		return releaseProgram{}, fmt.Errorf("could not find this program's own file: %w", err)
	}
	sum, err := hashFile(exe)
	if err != nil {
		return releaseProgram{}, err
	}
	sums, err := h.run(ctx, "cat", sumsFile)
	if err != nil {
		return releaseProgram{}, fmt.Errorf("read %s: %w", sumsFile, err)
	}
	pin, err := pinFromSums(sums, sum, sumsFile, "linux")
	if err != nil {
		return releaseProgram{}, err
	}
	return releaseProgram{exe: exe, sum: sum, pin: pin}, nil
}

// executorBinary installs the copy of this program that the executor runs: root's, in a directory only root writes, so the executor cannot
// change it. It records the pin beside it, which is the release's line of SHA256SUMS that verifyRelease found. replaced says that a binary that was
// there is not this program's.
func (h *host) executorBinary(ctx context.Context) (replaced bool, err error) {
	exe, sum, pin := h.release.exe, h.release.sum, h.release.pin
	if _, err := h.run(ctx, "install", "-d", "-m", "0755", "-o", "root", "-g", "root", executorLibDir); err != nil {
		return false, fmt.Errorf("create %s: %w", executorLibDir, err)
	}
	if on := h.installedHash(ctx, executorBinary); on != sum {
		replaced = on != ""
		if _, err := h.run(ctx, "install", "-m", "0755", "-o", "root", "-g", "root", exe, executorBinary); err != nil {
			return false, fmt.Errorf("could not install the test runner program: %w", err)
		}
		if got := h.installedHash(ctx, executorBinary); got != sum {
			return false, fmt.Errorf("the test runner program %s has the fingerprint (SHA-256) %q after the copy, not %s like this program, so it is not run", executorBinary, got, sum)
		}
	}
	if err := h.writeReadable(ctx, executorPin, pin+"\n"); err != nil {
		return false, err
	}
	h.check("The test runner runs %s, a copy of this program that only root can change (fingerprint, SHA-256: %s).\n", executorBinary, sum)
	return replaced, nil
}

// executorPort is the port of the service's HTTP listener that the executor is pointed at: --http-port, else the one the service account's
// settings name, else the one its installed unit publishes, else the default. The service is installed after this step, as another account.
func (h *host) executorPort(ctx context.Context) (int, error) {
	if v, ok := h.r.given("http-port"); ok {
		port, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || port < 1 || port > 65535 {
			return 0, fmt.Errorf("`--http-port %q` is not a port number (1 to 65535)", v)
		}
		return port, nil
	}
	home, err := h.accountHome(ctx)
	if err != nil {
		return 0, err
	}
	settings := path.Join(home, ".config", "suiteward", "settings.yaml")
	if raw, err := h.run(ctx, "cat", settings); err == nil {
		var doc struct {
			HTTPPort int `yaml:"http_port"`
		}
		if err := yaml.Unmarshal([]byte(raw), &doc); err != nil {
			return 0, fmt.Errorf("read %s: %w", settings, err)
		}
		if doc.HTTPPort != 0 {
			return doc.HTTPPort, nil
		}
	}
	quadlet := path.Join(home, ".config", "containers", "systemd", serveUnit)
	if raw, err := h.run(ctx, "cat", quadlet); err == nil {
		for _, line := range configLinesOf([]byte(raw)) {
			if m := installedHTTP.FindStringSubmatch(line); m != nil {
				return strconv.Atoi(m[1])
			}
		}
	}
	return DefaultHTTPPort, nil
}

// userctl runs systemctl against the user manager of the executor, as root: --machine=<user>@.host is how another account's manager is
// reached. Never --global, which would enable the unit for every account.
func (h *host) userctl(ctx context.Context, args ...string) (string, error) {
	return h.run(ctx, "systemctl", append([]string{"--user", "--machine=" + ExecutorAccount + "@.host"}, args...)...)
}

// executorUnit writes the unit to the directory that every user manager reads (it is root's, so the executor cannot change it; ConditionUser
// keeps the other accounts from running it), enables it for the executor's manager only and checks that it is active. A running executor
// that has other files than the ones just installed (filesChanged: its binary or its token) is restarted: `enable --now` does not restart a unit that is running.
func (h *host) executorUnit(ctx context.Context, port int, filesChanged bool) error {
	unit, err := RenderExecutorUnit(port)
	if err != nil {
		return err
	}
	on, readErr := h.run(ctx, "cat", executorUnitPath)
	unitChanged := readErr != nil || on != strings.TrimSpace(string(unit))
	if unitChanged {
		if err := h.makeDir(ctx, path.Dir(executorUnitPath)); err != nil {
			return err
		}
		if err := h.writeReadable(ctx, executorUnitPath, string(unit)); err != nil {
			return err
		}
	}
	state, _ := h.userctl(ctx, "show", "--property=ActiveState", "--value", executorUnitName) // an error here is a unit that is not there yet
	steps := [][]string{{"daemon-reload"}, {"enable", "--now", executorUnitName}}
	if state == "active" && (unitChanged || filesChanged) {
		steps = append(steps, []string{"restart", executorUnitName})
	}
	for _, args := range steps {
		if _, err := h.userctl(ctx, args...); err != nil {
			return fmt.Errorf("systemctl --user --machine=%s@.host %s: %w", ExecutorAccount, strings.Join(args, " "), err)
		}
	}
	state, err = h.userctl(ctx, "show", "--property=ActiveState", "--value", executorUnitName)
	if err != nil || state != "active" {
		uid, _ := h.run(ctx, "id", "-u", ExecutorAccount)
		return fmt.Errorf("%s is %q after the start, but it should be \"active\". To see why, run `journalctl _SYSTEMD_USER_UNIT=%s _UID=%s -n 50 --no-pager`", executorUnitName, state, executorUnitName, uid)
	}
	h.check("The test runner runs as the unit %s of the account %s, pointed at http://127.0.0.1:%d.\n", executorUnitName, ExecutorAccount, port)
	return nil
}

// executorHandover gives the service account a copy of the executor's token that it owns, as configDir gives it the GitHub env file: with
// `install`, which makes the copy the account's, and never by a write of root into a directory the account owns. The service step reads it
// into the secret of the service. A copy that is a link is refused, and one that is the same is left alone.
func (h *host) executorHandover(ctx context.Context, dir string) error {
	dest := path.Join(dir, executorTokenHandover)
	if _, err := h.run(ctx, "test", "-L", dest); err == nil {
		return fmt.Errorf("%s is a shortcut (symbolic link), and the installer will not copy through it as root. Remove the link, then run this again", dest)
	}
	if h.exists(ctx, dest) {
		if _, err := h.run(ctx, "cmp", "-s", executorAuthEtc, dest); err == nil {
			return nil
		}
	}
	if _, err := h.run(ctx, "install", "-o", ServiceAccount, "-g", ServiceAccount, "-m", "0600", executorAuthEtc, dest); err != nil {
		return fmt.Errorf("copy %s: %w", executorAuthEtc, err)
	}
	h.check("Gave %s a copy of the test runner's token (%s), which the next step stores as the service's secret.\n", ServiceAccount, dest)
	return nil
}
