package installer

import (
	"bufio"
	"cmp"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
)

// ErrUsage marks a command line that cannot be parsed.
var ErrUsage = errors.New("usage")

// Usage is printed with a usage error; each command lists its flags with --help.
const Usage = `usage: suiteward install [flags]
       suiteward install --upgrade [--yes] [--backup-dir DIR] [--backup-recipient age1pq1...] [--image sha256:<digest>] [flags]
       suiteward install host [flags]
       suiteward uninstall [--purge] [--yes] [--instance ID] [flags]
       suiteward enroll [--owner LOGIN] [--test-check OWNER/NAME=CHECK ...] [--settings PATH] [--tier isolated] [--plain]
       suiteward backup init [--kit PATH] [--yes] [--replace] [--settings PATH] [--tier isolated] [--plain]
       suiteward backup [--to DIR] [--settings PATH] [--tier isolated] [--plain]
       suiteward restore <backup.tar.age> [--kit PATH] [--yes --instance ID] [--settings PATH] [--tier isolated] [--plain]
       suiteward restore --plaintext-dir DIR [--yes --instance ID] [--settings PATH] [--tier isolated] [--plain]
       suiteward doctor [--json] [--settings PATH] [--token-file PATH] [--plain]
       suiteward version
`

// Env is what the install commands need from the machine; tests replace it.
type Env struct {
	Deps     Deps
	Term     Terminal
	PortFree func(port int) error // nil when the port can be listened on
	Sleep    func(time.Duration)
	// ResolveGitHub is the GitHub flow; nil means resolveGitHub. A test replaces it.
	ResolveGitHub func(ctx context.Context, env githubEnv) (GitHubResult, error)
	// HTTPGet reads an address of the local service (/status); nil means httpGet. A test replaces it.
	HTTPGet func(ctx context.Context, address string) ([]byte, error)
	// Version is shown in the title bar of the full screen ("installer <Version>"); empty reads "dev". The caller that knows the build version
	// (app.Run) sets it.
	Version string
	// Self is the copy of the program that a full screen offers on Windows; nil offers none. OSEnv sets it there.
	Self *SelfInstall
	// Unattended says that the command line answers for the person (unattended): the copy of the program is then never offered or removed, and the
	// one that exists is kept up to date.
	Unattended bool
	// OpenScreen opens the console of one command (ui.OpenOS on the process's console); nil means a plain one on Term.Out. A test replaces it.
	OpenScreen func(ctx context.Context, in io.Reader, version string, plain bool) (*ui.Screen, error)
}

func (e Env) httpGet() func(context.Context, string) ([]byte, error) {
	if e.HTTPGet != nil {
		return e.HTTPGet
	}
	return httpGet
}

// OSEnv is this machine and this process's console, printing to stdout.
func OSEnv(stdout io.Writer) Env {
	term := OSTerminal()
	term.Out = stdout
	env := Env{Deps: OSDeps(), Term: term, PortFree: loopbackFree, Sleep: time.Sleep}
	env.Deps.Command = commandName(runtime.GOOS, os.Executable, exec.LookPath)
	env.Self = osSelfInstall()
	if stdout == io.Writer(os.Stdout) { // the full screen takes over the process's console, so only the real stdout may ask for it
		env.OpenScreen = ui.OpenOS
	}
	return env
}

// openConsole opens the Screen of one command and returns the Env that talks through it: its output is the Screen, and its input the
// buffered reader the Screen reads the answers from, so that the Resolver, the Screen and a read in a goroutine share one buffer. The
// caller defers endConsole, which also runs before an error is printed to stderr.
func (e Env) openConsole(ctx context.Context, plain bool) (*ui.Screen, Env, error) {
	if err := e.Term.validate(); err != nil {
		return nil, e, err
	}
	in, version := bufio.NewReader(e.Term.In), cmp.Or(e.Version, "dev")
	var scr *ui.Screen
	var err error
	if e.OpenScreen != nil {
		scr, err = e.OpenScreen(ctx, in, version, plain)
	} else {
		scr, err = ui.Open(ui.Options{Out: e.Term.Out, In: in, Context: ctx, Version: version, Mode: ui.ModePlain})
	}
	if err != nil {
		return nil, e, fmt.Errorf("open the console: %w", err)
	}
	e.Term.In, e.Term.Out = in, scr
	return scr, e, nil
}

// wantsPlain says whether --plain, or plain in the answers file, asks for plain lines instead of the full screen; the flag wins.
func wantsPlain(flags, answers map[string]string) (bool, error) {
	v, ok := flags["plain"]
	if !ok {
		v, ok = answers["plain"]
	}
	if !ok {
		return false, nil
	}
	return parseYesNo("plain", v)
}

func loopbackFree(port int) error {
	l, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		return err
	}
	return l.Close()
}

func (e Env) validate() error {
	if err := e.Deps.validate(); err != nil {
		return err
	}
	if e.PortFree == nil || e.Sleep == nil {
		return errors.New("the installer needs PortFree and Sleep")
	}
	if s := e.Self; s != nil && (s.Executable == nil || s.LookPath == nil || s.Rename == nil || s.Path == nil) {
		return errors.New("the installer needs Executable, LookPath, Rename and Path to offer the copy of the program")
	}
	return nil
}

// Main runs one command of the installer: args start with the command name (install, install host, uninstall, enroll, backup, backup init, restore or doctor).
// It returns the process exit code and prints a problem to stderr.
func Main(ctx context.Context, args []string, env Env, stderr io.Writer) int {
	command, rest := "", []string(nil)
	if len(args) > 0 {
		command, rest = args[0], args[1:]
	}
	var err error
	switch {
	case command == "install" && len(rest) > 0 && rest[0] == "host":
		err = installHost(ctx, rest[1:], env)
	case command == "install":
		err = Install(ctx, rest, env)
	case command == "backup" && len(rest) > 0 && rest[0] == "init":
		err = BackupInit(ctx, rest[1:], env)
	case command == "backup":
		err = Backup(ctx, rest, env)
	case command == "restore":
		err = Restore(ctx, rest, env)
	case command == "enroll":
		err = Enroll(ctx, rest, env)
	case command == "uninstall":
		err = Uninstall(ctx, rest, env)
	case command == "doctor":
		return doctorCommand(ctx, rest, env, stderr)
	default:
		err = fmt.Errorf("%w: unknown command %q", ErrUsage, command)
	}
	switch {
	case err == nil || errors.Is(err, flag.ErrHelp):
		return 0
	case errors.Is(err, ErrUsage):
		// A failed write to stderr cannot be reported anywhere else.
		_, _ = fmt.Fprintf(stderr, "suiteward: %v\n%s", err, Usage)
		return 2
	// Ctrl-C: the command has closed its screen, and read nothing since. Any error with a done context is the interrupt's: a prompt's
	// error that a caller put in its own words, or the failure of a child that the signal killed, has lost its Canceled but not its cause.
	// cmd/suiteward cancels the context for SIGTERM as well, so a SIGTERM ends here too, as "interrupted" with 130; that is accepted.
	case ctx.Err() != nil && !errors.As(err, new(failureShown)): // a Ctrl-C at the page of a failure only closes it, and the exit code stays the command's
		_, _ = fmt.Fprintf(stderr, "suiteward: interrupted: %v\n", err) // the error keeps what a failed step left behind and how to clean it up
		return 130
	default:
		_, _ = fmt.Fprintf(stderr, "suiteward: %v\n", err)
		return 1
	}
}

// listFlag is a flag that may be given more than once; its value is the entries one per line.
type listFlag []string

func (l *listFlag) String() string     { return strings.Join(*l, "\n") }
func (l *listFlag) Set(v string) error { *l = append(*l, v); return nil }

// installFlags are the string flags of install; every one is also a key of the answers file.
var installFlags = []struct{ name, usage string }{
	{"answers", "read the answers from this YAML file (a flag you give wins over it)"},
	{"settings", "the file where the installer keeps its settings (default: your user configuration folder)"},
	{"token-file", "where to write the copy of the access token for your coding agent's client, with the test runner's files next to it (default: your user folder, for the default settings file only: another --settings file needs --token-file)"},
	{"tier", "installation type: co-located (on this computer, next to your coding agent) or isolated (on a separate server)"},
	{"runtime", "container program: podman (recommended) or docker"},
	{"connection", "the Podman connection or Docker context to use (default: the rootless Podman machine, or Docker Desktop's)"},
	{"image", "the SuiteWard release to install, ghcr.io/ignisdevne/suiteward@sha256:<64 hex> (default: the one already installed, else the release this program carries; a development build needs this flag)"},
	{"sha256sums", "co-located only, optional: the release's SHA256SUMS file, already verified. The file must list this program, and the install uses it to check the test runner against the release. Without the flag the install works, and doctor shows a warning"},
	{"http-port", "port of SuiteWard's web address, on this computer only (default 27081)"},
	{"mcp-port", "port your coding agent connects to, on this computer only (default 27091)"},
	{"job-timeout", "how long a SuiteWard job may run, as a duration such as 5m (sets SUITEWARD_JOB_TIMEOUT)"},
	{"github", "yes: create the SuiteWard GitHub App now; no: skip GitHub; new-app: create a new App although the settings already name one (the old App stays on GitHub)"},
	{"github-account", `the account that will own the new App: "personal" or an organization's login`},
	{"manifest-port", "port where this program waits for GitHub while the App is created, on this computer only (default: any free port)"},
	{"ruleset-imported", "yes once you have imported the ruleset file of each repository on GitHub"},
	{"github-app-id", "the ID of a GitHub App you already made, or none to skip GitHub"},
	{"github-installation-id", "the installation ID of that App"},
	{"github-owner", "the GitHub username of the owner, the only account that can approve changes"},
	{"github-repositories", "the repositories to protect, as a comma-separated owner/name list (at most 20)"},
	{"github-app-key-file", "file with the App's private key (.pem), or - to read it from standard input. Used only while the key is not in the secret store (also for an App the settings name, or one an earlier run recorded, whose key is missing)"},
}

func newFlagSet(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	fs.Usage = func() {} // a usage error is reported by Main; --help prints the flags (printHelp)
	return fs
}

func newInstallFlagSet() *flag.FlagSet {
	fs := newFlagSet("install")
	for _, f := range installFlags {
		fs.String(f.name, "", f.usage)
	}
	fs.Var(&listFlag{}, "test-check", "the name of the check your CI publishes, as OWNER/NAME=CHECK; repeat the flag for more repositories, or give the name alone when there is one repository")
	fs.Bool("accept-docker", false, "accept the risks of Docker and use it although Podman is recommended")
	fs.Bool("upgrade", false, "upgrade the installed SuiteWard to the release this program carries (or --image), after making a backup")
	fs.Bool("yes", false, "with --upgrade: do not ask before the backup and the upgrade (a stopped service is not started: that is asked separately)")
	fs.String("backup-dir", "", "with --upgrade: the folder for the backup file (default: a backups folder next to the access token copy, or in ~/.config/suiteward of the service account on a separate server)")
	fs.String("backup-recipient", "", "with --upgrade, when no terminal can be asked: the public key (age1pq1...) the backup is encrypted to; it is saved in the settings (answers-file key backup_recipient)")
	fs.Bool("plain", false, plainUsage)
	return fs
}

// plainUsage is the help of --plain, which every command that draws a screen has.
const plainUsage = "print plain lines instead of the full-screen window, even on a terminal (this is what a pipe, a script and NO_COLOR get)"

func printHelp(fs *flag.FlagSet, out io.Writer) {
	_, _ = io.WriteString(out, Usage)
	fs.SetOutput(out)
	fs.PrintDefaults()
}

// parse parses args and reports a bad command line as a usage error, and --help as flag.ErrHelp after printing the flags.
func parse(fs *flag.FlagSet, args []string, out io.Writer) (map[string]string, error) {
	if err := parseFlags(fs, args, out); err != nil {
		return nil, err
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("%w: unexpected argument %q", ErrUsage, fs.Arg(0))
	}
	flags := FlagsSet(fs)
	if err := emptySettings(flags); err != nil { // here, before the screen of any command opens: a usage error comes before it
		return nil, err
	}
	return flags, nil
}

// unattended says that the command line answers for the person: --yes, or an answers file. Such a run never offers the copy of the program or its
// removal, which the person has to agree to.
func unattended(flags map[string]string) bool {
	_, answers := flags["answers"]
	return answers || flags["yes"] == "true"
}

func parseFlags(fs *flag.FlagSet, args []string, out io.Writer) error {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printHelp(fs, out)
			return err
		}
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}
	return nil
}

// parseWithFile is parse for a command with at most one argument that is not a flag, a file, which may come before the flags: the flag
// package stops at the first argument that is not a flag, so the rest is parsed again.
func parseWithFile(fs *flag.FlagSet, args []string, out io.Writer) (file string, flags map[string]string, err error) {
	if err := parseFlags(fs, args, out); err != nil {
		return "", nil, err
	}
	if fs.NArg() > 0 {
		file = fs.Arg(0)
		if err := parseFlags(fs, fs.Args()[1:], out); err != nil {
			return "", nil, err
		}
	}
	if fs.NArg() > 0 {
		return "", nil, fmt.Errorf("%w: unexpected argument %q", ErrUsage, fs.Arg(0))
	}
	flags = FlagsSet(fs)
	if err := emptySettings(flags); err != nil {
		return "", nil, err
	}
	return file, flags, nil
}

// emptySettings refuses --settings with no path: an unset shell variable gives that, and it must not silently mean the default settings file,
// which may belong to another instance.
func emptySettings(flags map[string]string) error {
	if path, given := flags["settings"]; given && path == "" {
		return fmt.Errorf("%w: --settings is empty (an unset variable?): give the path of the settings file, or leave the flag out for the default one", ErrUsage)
	}
	return nil
}

// settingsPathFrom is the settings file of a command and whether it is the shared per-user default.
func settingsPathFrom(r *Resolver) (path string, isDefault bool, err error) {
	if path = r.settingsFlag(); path != "" {
		return path, false, nil
	}
	path, err = DefaultSettingsPath()
	return path, true, err
}

// loadSaved reads the settings of an earlier install. None yet is not an error for the default file, but a file the command line
// names (named) must exist: a mistyped path would otherwise run the command on defaults, which are not an instance.
func loadSaved(d Deps, path string, named bool) (Settings, bool, error) {
	saved, err := LoadSettings(path)
	switch {
	case err == nil:
		return saved, true, nil
	case errors.Is(err, ErrNoSettings) && named:
		return Settings{}, false, fmt.Errorf("the settings file %s does not exist: %w (nothing was changed; check the path: only `%s` makes a settings file)", path, ErrNoSettings, d.suggest("install"))
	case errors.Is(err, ErrNoSettings):
		return DefaultSettings(), false, nil
	}
	return Settings{}, false, err
}

// Install runs the install command (args without the command name).
func Install(ctx context.Context, args []string, env Env) (err error) {
	if err := env.validate(); err != nil {
		return err
	}
	fs := newInstallFlagSet()
	flags, err := parse(fs, args, env.Term.Out)
	if err != nil {
		return err
	}
	answers := map[string]string{}
	if path, ok := flags["answers"]; ok {
		if answers, err = LoadAnswers(path); err != nil {
			return err
		}
	}
	env.Unattended = unattended(flags)
	plain, err := wantsPlain(flags, answers)
	if err != nil {
		return err
	}
	scr, env, err := env.openConsole(ctx, plain)
	if err != nil {
		return err
	}
	defer endConsole(scr, &err)
	aliasGitHubKeys(flags)
	aliasGitHubKeys(answers)
	aliasBackupKey(flags)
	aliasBackupKey(answers)
	r, err := NewResolver(flags, answers, env.Term)
	if err != nil {
		return err
	}
	upgrade := flags["upgrade"] == "true"
	if upgrade { // an install of a new installation never asks: it names its settings file and token file, or takes the default ones
		if err := r.chooseInstallation(env.Deps); err != nil {
			return err
		}
	}
	env.Deps.instance = r.instance()
	settingsPath, defaultPath, err := settingsPathFrom(r)
	if err != nil {
		return err
	}
	saved, haveSaved, err := loadSaved(env.Deps, settingsPath, upgrade && !defaultPath) // an install makes the file: only an upgrade needs it to be there
	if err != nil {
		return err
	}
	if _, given := r.given("backup-recipient"); given && !upgrade {
		return fmt.Errorf("--backup-recipient (backup_recipient) is for --upgrade: to set the recovery key of this instance, run `%s`", env.Deps.suggest("backup init"))
	}
	var tier string
	if upgrade { // an upgrade works on what is installed: it never asks which tier
		if err := tierNeeded(r, haveSaved, env.Deps.GOOS, coLocatedTierFlag); err != nil {
			return err
		}
		tier = r.preset("tier", string(saved.Tier))
	} else if tier, err = chooseTier(r, saved.Tier); err != nil {
		return err
	}
	switch config.Tier(tier) {
	case config.TierIsolated:
		if _, given := r.given("sha256sums"); given {
			return fmt.Errorf("%w: --sha256sums does not apply to the isolated install, and nothing was changed: the administrator's host step sets up the test runner and takes this flag instead (`%s`)", ErrUsage, env.Deps.suggestHostStep(hostStepSums))
		}
		isolated := saved
		isolated.Tier, isolated.Runtime = config.TierIsolated, RuntimePodman
		if upgrade {
			return upgradeIsolated(ctx, isolated, r, env)
		}
		return installIsolated(ctx, isolated, r, env)
	case config.TierCoLocated:
		target := installTarget{settingsPath: settingsPath, defaultPath: defaultPath, saved: saved, haveSaved: haveSaved}
		if upgrade {
			return upgradeColocated(ctx, r, env, target)
		}
		return installColocated(ctx, r, env, target)
	}
	return fmt.Errorf("the installation type %q is not %s or %s", tier, config.TierCoLocated, config.TierIsolated)
}

// installHost is `suiteward install host`, the administrator half of the isolated install.
func installHost(ctx context.Context, args []string, env Env) (err error) {
	if err := env.validate(); err != nil {
		return err
	}
	fs := newFlagSet("install host")
	fs.String("answers", "", "read answers from this YAML file (a flag wins over it)")
	fs.Bool("yes", false, "answer yes to installing Podman (with dnf)")
	fs.String("metadata-rule", "", "yes or no: block the service account from the cloud provider's metadata address")
	fs.String("http-port", "", "port of the service's web address on this server, which the test runner connects to (default: the port of the existing installation, else 27081)")
	fs.String("sha256sums", "", "(required) the release's SHA256SUMS file, already verified. It must list this program, which is installed as the test runner")
	fs.Bool("plain", false, plainUsage)
	flags, err := parse(fs, args, env.Term.Out)
	if err != nil {
		return err
	}
	answers := map[string]string{}
	if path, ok := flags["answers"]; ok {
		if answers, err = LoadAnswers(path); err != nil {
			return err
		}
	}
	plain, err := wantsPlain(flags, answers)
	if err != nil {
		return err
	}
	scr, env, err := env.openConsole(ctx, plain)
	if err != nil {
		return err
	}
	defer endConsole(scr, &err)
	r, err := NewResolver(flags, answers, env.Term)
	if err != nil {
		return err
	}
	return PrepareHost(ctx, r, env.Deps, env.Term.Out)
}

// doctorCommand is `suiteward doctor [--json] [--settings PATH] [--token-file PATH]`. It reads the settings before any configuration is loaded: the
// machine that checks the instance has no SUITEWARD_* environment.
func doctorCommand(ctx context.Context, args []string, env Env, stderr io.Writer) int {
	fs := newFlagSet("doctor")
	fs.String("settings", "", "the file where the installer keeps its settings (default: your user configuration folder)")
	fs.String("token-file", "", "where the instance keeps the copy of its access token, with the test runner's files next to it (default: your user folder, for the default settings file only: another --settings file needs --token-file)")
	fs.Bool("json", false, "print the results as JSON, for a program to read")
	fs.Bool("plain", false, plainUsage)
	flags, err := parse(fs, args, env.Term.Out)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case err != nil:
		// A failed write to stderr cannot be reported anywhere else.
		_, _ = fmt.Fprintf(stderr, "suiteward: %v\n%s", err, Usage)
		return 2
	}
	env.Deps.instance = instanceArgs{settings: flags["settings"], tokenFile: flags["token-file"]}
	if flags["json"] == "true" { // JSON is for a program: it is never drawn on a screen that vanishes
		return Doctor(ctx, env.Deps, flags["settings"], flags["token-file"], env.Term.Out, true)
	}
	plain, err := wantsPlain(flags, nil)
	var scr *ui.Screen
	if err == nil {
		scr, env, err = env.openConsole(ctx, plain)
	}
	if err != nil {
		_, _ = fmt.Fprintf(stderr, "suiteward: %v\n", err)
		return 1
	}
	var closed error
	defer endConsole(scr, &closed) // a panic must not leave the terminal on the alternate screen; the exit code below reads the error of Close
	r, err := NewResolver(flags, nil, env.Term)
	if err == nil {
		err = r.chooseInstallation(env.Deps)
	}
	if err != nil {
		_ = scr.Close() // before the error is printed, or the screen would hide it; the deferred call reports a close that failed
		_, _ = fmt.Fprintf(stderr, "suiteward: %v\n", err)
		return 1
	}
	env.Deps.instance = r.instance()
	code := Doctor(ctx, env.Deps, r.settingsFlag(), r.preset("token-file", ""), env.Term.Out, false)
	interrupted := ctx.Err() != nil // Ctrl-C ended the checks; one at the final wait comes after them and keeps their code
	waitErr := holdPage(scr)        // the results stay on the screen until Enter
	closeErr := scr.Close()
	if interrupted {
		_, _ = fmt.Fprintln(stderr, "suiteward: interrupted") // the screen is closed: the line would vanish with it otherwise
		return 130
	}
	if waitErr != nil {
		_, _ = fmt.Fprintf(stderr, "suiteward: %v\n", waitErr) // a failed write to stderr cannot be reported anywhere else
	}
	if closeErr != nil { // Doctor's own 1 for a report it could not write comes with this error too: the screen keeps the first failed write
		_, _ = fmt.Fprintf(stderr, "suiteward: the terminal failed while doctor ran: %v\n", closeErr) // a failed write to stderr cannot be reported anywhere else
	}
	if code == 0 && (waitErr != nil || closeErr != nil) {
		return 1 // the report could not be held or written to the terminal, and the lines above say which
	}
	return code
}

// preset is an answer that is never asked for: the flag or the answers file, else the installation that was chosen, else def.
func (r *Resolver) preset(key, def string) string {
	if v, ok := r.flags[key]; ok {
		return v
	}
	if v, ok := r.answers[key]; ok {
		return v
	}
	if v, ok := r.chosen[key]; ok {
		return v
	}
	return def
}

const dockerWarning = `Docker works on this computer, but Podman is strongly recommended.
  - Docker's background service runs as root (an administrator), so whoever controls it can reach everything inside the containers. Rootless Podman does not have this problem.
  - Docker keeps SuiteWard's secrets (the database password, the GitHub App key, the access token) in volumes that anyone who controls that service can read.
`

// dockerWarningShort is the same two risks for a full screen, in the few rows that the smallest window leaves above the question: a typed
// yes must be an informed one there too.
const dockerWarningShort = `Docker works on this computer, but Podman is strongly recommended.
  - Docker's service runs as root: whoever controls it can reach the containers.
  - Its secrets are in volumes that anyone who controls the service can read.
`

// confirmDocker prints the warning and requires an explicit yes. The warning is one Warn: the full screen wraps its lines and keeps it.
func confirmDocker(r *Resolver) error {
	r.explain([]string{"accept-docker"}, "Docker is not recommended",
		ui.Para("Type yes to accept the risks below. Anything else stops the install before it changes anything."))
	warning := dockerWarning
	if r.screen.FullScreen() {
		warning = dockerWarningShort
	}
	r.screen.Warn("%s", warning)
	answer, err := r.Value("accept-docker", "Type yes to use Docker anyway", "no")
	if err != nil {
		return err
	}
	if yes, err := parseYesNo("accept-docker", answer); err != nil || !yes {
		return errors.New("you did not confirm Docker, so nothing was changed: to use Docker, pass --accept-docker; to use Podman, pass --runtime podman")
	}
	return nil
}

// tierNeeded refuses a command that works on what is installed when it cannot tell the tier: a host installed by hand has no
// settings file, and the default tier would be a guess (on Linux that is the isolated host). coLocated is what the message offers for a
// co-located stack: a command that cannot work on one without its settings file says how to get the file instead.
func tierNeeded(r *Resolver, haveSaved bool, goos, coLocated string) error {
	if _, given := r.given("tier"); !given && !haveSaved && goos == "linux" {
		return errors.New("this computer has no SuiteWard settings file, so the installation type cannot be told: pass --tier isolated (as the service account) or " + coLocated)
	}
	return nil
}

// coLocatedTierFlag is what tierNeeded offers for a co-located stack to the commands that can work on one without a settings file.
const coLocatedTierFlag = "--tier co-located"

var plainArg = regexp.MustCompile(`^[A-Za-z0-9_+=.,:/\\@%-]+$`)

// quoteArg is a command-line argument as a person can paste it into a shell: as it is when it is plain, else in single quotes.
// shortcut: a path with a single quote goes in double quotes, inside which bash and PowerShell both still expand $ and backticks, so
// such a path is not safe to paste; and a path with backslashes is left bare, which is right in PowerShell and cmd, where a path is
// not escaped, and wrong in bash. An installer path has none of these: the upgrade is to escape per shell once one does.
func quoteArg(arg string) string {
	switch {
	case plainArg.MatchString(arg):
		return arg
	case strings.Contains(arg, "'"):
		return `"` + arg + `"`
	}
	return "'" + arg + "'"
}

// tierChoices are the answers of the tier question.
var tierChoices = []ui.Choice{
	{Name: string(config.TierCoLocated), Desc: "on this computer, next to your coding agent"},
	{Name: string(config.TierIsolated), Desc: "on a separate server or user account"},
}

// The steps of the install of each tier. The tier question shows those of the tier it offers first, so that the step column is there from
// the first question; the flow of the tier that was chosen sets its own again.
var (
	colocatedInstallSteps = []string{"Environment", "Checks", "GitHub", "Start", "Owner", "Done"}
	isolatedInstallSteps  = []string{"Checks", "Images", "GitHub", "Units", "Start", "Owner", "Done"}
)

// chooseTier asks which tier to install. On a full screen it first draws the steps and a page that says what each tier is and which to take.
func chooseTier(r *Resolver, def config.Tier) (string, error) {
	if r.screen.FullScreen() && r.asks("tier") {
		steps := colocatedInstallSteps
		if def == config.TierIsolated {
			steps = isolatedInstallSteps
		}
		r.screen.Steps(steps...)
		r.screen.Page("Where SuiteWard runs",
			ui.Fields(
				ui.KV{Key: string(config.TierCoLocated), Value: reducedAssuranceNotice + " Your coding agent can reach SuiteWard's key, its database and its secrets."},
				ui.KV{Key: string(config.TierIsolated), Value: "On a separate Linux server or account that your coding agent cannot reach: the strongest protection. It needs Enterprise Linux 9, such as AlmaLinux 9."}),
			ui.Para("To start, choose co-located."))
	}
	return r.Select("tier", "Installation type", tierChoices, string(def))
}
