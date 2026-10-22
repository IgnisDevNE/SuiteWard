package installer

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// ErrUsage marks a command line that cannot be parsed.
var ErrUsage = errors.New("usage")

// Usage is printed with a usage error; each command lists its flags with --help.
const Usage = `usage: suiteward install [flags]
       suiteward install --upgrade [--yes] [--backup-dir DIR] [--image sha256:<digest>] [flags]
       suiteward install host [flags]
       suiteward uninstall [--purge] [--yes] [flags]
       suiteward enroll [--owner LOGIN] [--test-check OWNER/NAME=CHECK ...] [--settings PATH] [--tier isolated]
       suiteward doctor [--json] [--settings PATH]
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
	return Env{Deps: OSDeps(), Term: term, PortFree: loopbackFree, Sleep: time.Sleep}
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
	return nil
}

// Main runs one command of the installer: args start with the command name (install, install host, uninstall, enroll or doctor).
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
	{"answers", "read answers from this YAML file (a flag wins over it)"},
	{"settings", "the installer settings file (default: the per-user configuration directory)"},
	{"token-file", "where the copy of the MCP token for a client is written (default: the per-user directory)"},
	{"tier", "co-located (this PC) or isolated"},
	{"runtime", "podman (recommended) or docker"},
	{"connection", "the Podman connection, or the Docker context (default: the rootless Podman machine, or Docker Desktop's)"},
	{"image", "the release image pinned by digest, ghcr.io/ignisdevne/suiteward@sha256:<64 hex> (default: the installed image, else the release this binary carries; a development build needs it)"},
	{"http-port", "loopback port of the HTTP listener (default 8081)"},
	{"mcp-port", "loopback port of the MCP listener (default 8091)"},
	{"job-timeout", "a Go duration for SUITEWARD_JOB_TIMEOUT (serve only)"},
	{"github", "yes: create the GitHub App now (manifest flow), no: skip GitHub, new-app: create a new App although the settings name one (the old App stays on GitHub)"},
	{"github-account", `the account that owns the new App: "personal" or an organization login`},
	{"manifest-port", "the port of the manifest listener on 127.0.0.1 (default: any free port)"},
	{"ruleset-imported", "yes once the ruleset of each repository is imported"},
	{"github-app-id", "the ID of an App that exists, or none to skip GitHub"},
	{"github-installation-id", "the installation of that App"},
	{"github-owner", "the GitHub login of the instance owner"},
	{"github-repositories", "comma-separated owner/name list, at most 20"},
	{"github-app-key-file", "file with the App's private key (PEM), or - for standard input; read only while the key secret is missing (it also gives the key of an App the settings name, or a recorded App, whose key secret is gone)"},
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
	fs.Var(&listFlag{}, "test-check", "the test check of a repository, OWNER/NAME=CHECK; repeat it, or give one plain name for a single repository")
	fs.Bool("accept-docker", false, "confirm that Docker may be used although Podman is recommended")
	fs.Bool("upgrade", false, "upgrade the installed stack to the release this binary carries (or --image), after a backup")
	fs.Bool("yes", false, "with --upgrade: do not ask before backing up and upgrading (it does not start a service that is down: that is asked on its own)")
	fs.String("backup-dir", "", "with --upgrade: the directory the backup directory is made in (default: a backups directory beside the MCP token copy, or in ~/.config/suiteward of the service account on the isolated tier)")
	return fs
}

func printHelp(fs *flag.FlagSet, out io.Writer) {
	_, _ = io.WriteString(out, Usage)
	fs.SetOutput(out)
	fs.PrintDefaults()
}

// parse parses args and reports a bad command line as a usage error, and --help as flag.ErrHelp after printing the flags.
func parse(fs *flag.FlagSet, args []string, out io.Writer) (map[string]string, error) {
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printHelp(fs, out)
			return nil, err
		}
		return nil, fmt.Errorf("%w: %v", ErrUsage, err)
	}
	if fs.NArg() > 0 {
		return nil, fmt.Errorf("%w: unexpected argument %q", ErrUsage, fs.Arg(0))
	}
	return FlagsSet(fs), nil
}

// settingsPathFrom is the settings file of a command and whether it is the shared per-user default.
func settingsPathFrom(flags map[string]string) (path string, isDefault bool, err error) {
	if path = flags["settings"]; path != "" {
		return path, false, nil
	}
	path, err = DefaultSettingsPath()
	return path, true, err
}

// loadSaved reads the settings of an earlier install; none yet is not an error.
func loadSaved(path string) (Settings, bool, error) {
	saved, err := LoadSettings(path)
	switch {
	case err == nil:
		return saved, true, nil
	case errors.Is(err, ErrNoSettings):
		return DefaultSettings(), false, nil
	}
	return Settings{}, false, err
}

// Install runs the install command (args without the command name).
func Install(ctx context.Context, args []string, env Env) error {
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
	aliasGitHubKeys(flags)
	aliasGitHubKeys(answers)
	r, err := NewResolver(flags, answers, env.Term)
	if err != nil {
		return err
	}
	settingsPath, defaultPath, err := settingsPathFrom(flags)
	if err != nil {
		return err
	}
	saved, haveSaved, err := loadSaved(settingsPath)
	if err != nil {
		return err
	}

	upgrade := flags["upgrade"] == "true"
	var tier string
	if upgrade { // an upgrade works on what is installed: it never asks which tier
		if err := tierNeeded(r, haveSaved, env.Deps.GOOS); err != nil {
			return err
		}
		tier = r.preset("tier", string(saved.Tier))
	} else if tier, err = r.Value("tier", "Installation tier (co-located or isolated)", string(saved.Tier)); err != nil {
		return err
	}
	switch config.Tier(tier) {
	case config.TierIsolated:
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
	return fmt.Errorf("tier %q is not %s or %s", tier, config.TierCoLocated, config.TierIsolated)
}

// installHost is `suiteward install host`, the administrator half of the isolated install.
func installHost(ctx context.Context, args []string, env Env) error {
	if err := env.validate(); err != nil {
		return err
	}
	fs := newFlagSet("install host")
	fs.String("answers", "", "read answers from this YAML file (a flag wins over it)")
	fs.Bool("yes", false, "answer yes to installing Podman (dnf)")
	fs.String("metadata-rule", "", "yes or no: refuse the cloud metadata address for the service account")
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
	r, err := NewResolver(flags, answers, env.Term)
	if err != nil {
		return err
	}
	return PrepareHost(ctx, r, env.Deps, env.Term.Out)
}

// doctorCommand is `suiteward doctor [--json] [--settings PATH]`. It reads the settings before any configuration is loaded: the
// machine that checks the instance has no SUITEWARD_* environment.
func doctorCommand(ctx context.Context, args []string, env Env, stderr io.Writer) int {
	fs := newFlagSet("doctor")
	fs.String("settings", "", "the installer settings file (default: the per-user configuration directory)")
	fs.Bool("json", false, "print the results as JSON")
	flags, err := parse(fs, args, env.Term.Out)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case err != nil:
		// A failed write to stderr cannot be reported anywhere else.
		_, _ = fmt.Fprintf(stderr, "suiteward: %v\n%s", err, Usage)
		return 2
	}
	return Doctor(ctx, env.Deps, flags["settings"], env.Term.Out, flags["json"] == "true")
}

// preset is an answer that is never asked for: the flag or the answers file, else def.
func (r *Resolver) preset(key, def string) string {
	if v, ok := r.flags[key]; ok {
		return v
	}
	if v, ok := r.answers[key]; ok {
		return v
	}
	return def
}

const dockerWarning = `Docker is allowed on this PC, but Podman is strongly recommended.
  - Docker's daemon runs as root (a rootful daemon): the containers are not isolated from whoever controls that daemon the way rootless Podman's are.
  - Docker has no secret store outside Swarm, so the secrets (the database owner URL, the App key, the MCP token) live in named volumes
    that anyone who controls the Docker daemon can read.
`

// confirmDocker prints the warning and requires an explicit yes.
func confirmDocker(r *Resolver) error {
	r.say("%s", dockerWarning)
	answer, err := r.Value("accept-docker", "Type yes to use Docker anyway", "no")
	if err != nil {
		return err
	}
	if yes, err := parseYesNo("accept-docker", answer); err != nil || !yes {
		return errors.New("the use of Docker was not confirmed: pass --accept-docker, or install with --runtime podman")
	}
	return nil
}

// tierNeeded refuses a command that works on what is installed when it cannot tell the tier: a host installed by hand has no
// settings file, and the default tier would be a guess (on Linux that is the isolated host).
func tierNeeded(r *Resolver, haveSaved bool, goos string) error {
	if _, given := r.given("tier"); !given && !haveSaved && goos == "linux" {
		return errors.New("this host has no settings file, so the tier cannot be told: pass --tier isolated (as the service account) or --tier co-located")
	}
	return nil
}

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
