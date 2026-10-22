package installer

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// ErrUsage marks a command line that cannot be parsed.
var ErrUsage = errors.New("usage")

// Usage is printed with a usage error; `suiteward install --help` lists every flag.
const Usage = "usage: suiteward install [flags]   (suiteward install --help lists them)\n"

// Env is what the install command needs from the machine; tests replace it.
type Env struct {
	Deps     Deps
	Term     Terminal
	PortFree func(port int) error // nil when the port can be listened on
	Sleep    func(time.Duration)
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

// Run is `suiteward install`: it returns the process exit code and prints a problem to stderr.
func Run(ctx context.Context, args []string, env Env, stderr io.Writer) int {
	err := Install(ctx, args, env)
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

// installFlags are the string flags; every one is also a key of the answers file.
var installFlags = []struct{ name, usage string }{
	{"answers", "read answers from this YAML file (a flag wins over it)"},
	{"settings", "the installer settings file (default: the per-user configuration directory)"},
	{"token-file", "where the copy of the MCP token for a client is written (default: the per-user directory)"},
	{"tier", "co-located (this PC) or isolated"},
	{"runtime", "podman (recommended) or docker"},
	{"connection", "the Podman connection, or the Docker context (default: the rootless Podman machine, or Docker Desktop's)"},
	{"image", "the release image pinned by digest: ghcr.io/ignisdevne/suiteward@sha256:<64 hex>"},
	{"http-port", "loopback port of the HTTP listener (default 8081)"},
	{"mcp-port", "loopback port of the MCP listener (default 8091)"},
	{"job-timeout", "a Go duration for SUITEWARD_JOB_TIMEOUT (serve only)"},
	{"github-app-id", "the GitHub App ID, or none"},
	{"github-installation-id", "the GitHub installation ID"},
	{"github-owner", "the login that owns the repositories"},
	{"github-repositories", "comma-separated owner/name list, at most 20"},
	{"github-app-key-file", "file with the App's private key (PEM), or - for standard input; read only while the key secret is missing"},
}

func newFlagSet() *flag.FlagSet {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	for _, f := range installFlags {
		fs.String(f.name, "", f.usage)
	}
	fs.Bool("accept-docker", false, "confirm that Docker may be used although Podman is recommended")
	fs.Usage = func() {} // a usage error is reported by Run; --help prints the flags (printHelp)
	return fs
}

func printHelp(fs *flag.FlagSet, out io.Writer) {
	_, _ = io.WriteString(out, Usage)
	fs.SetOutput(out)
	fs.PrintDefaults()
}

// Install runs the install command (args without the command name).
func Install(ctx context.Context, args []string, env Env) error {
	if err := env.validate(); err != nil {
		return err
	}
	fs := newFlagSet()
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			printHelp(fs, env.Term.Out)
			return err
		}
		return fmt.Errorf("%w: %v", ErrUsage, err)
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("%w: unexpected argument %q", ErrUsage, fs.Arg(0))
	}
	flags := FlagsSet(fs)
	answers := map[string]string{}
	if path, ok := flags["answers"]; ok {
		var err error
		if answers, err = LoadAnswers(path); err != nil {
			return err
		}
	}
	r, err := NewResolver(flags, answers, env.Term)
	if err != nil {
		return err
	}
	settingsPath := flags["settings"]
	defaultPath := settingsPath == ""
	if defaultPath {
		if settingsPath, err = DefaultSettingsPath(); err != nil {
			return err
		}
	}
	saved, err := LoadSettings(settingsPath)
	haveSaved := err == nil
	if err != nil && !errors.Is(err, ErrNoSettings) {
		return err
	}
	if !haveSaved {
		saved = DefaultSettings()
	}

	tier, err := r.Value("tier", "Installation tier (co-located or isolated)", string(saved.Tier))
	if err != nil {
		return err
	}
	switch config.Tier(tier) {
	case config.TierIsolated:
		return installIsolated(ctx, r, env)
	case config.TierCoLocated:
		return installColocated(ctx, r, env, installTarget{settingsPath: settingsPath, defaultPath: defaultPath, saved: saved, haveSaved: haveSaved})
	}
	return fmt.Errorf("tier %q is not %s or %s", tier, config.TierCoLocated, config.TierIsolated)
}

// installIsolated is replaced by the isolated install (task I5).
func installIsolated(context.Context, *Resolver, Env) error {
	return errors.New("the isolated install is not available yet: only --tier co-located is")
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
	if !slices.Contains([]string{"y", "yes", "true"}, strings.ToLower(strings.TrimSpace(answer))) {
		return errors.New("the use of Docker was not confirmed: pass --accept-docker, or install with --runtime podman")
	}
	return nil
}

var pemKey = regexp.MustCompile(`-----BEGIN (RSA )?PRIVATE KEY-----`)

// githubSettings is the one source of the GitHub values and of the App key: flags, the answers file and prompts. The
// manifest flow replaces it (task I6). The key function runs only when the key secret does not exist yet.
func githubSettings(r *Resolver, cur GitHub) (GitHub, func() ([]byte, error), error) {
	def := "none"
	if cur.AppID != 0 {
		def = strconv.FormatInt(cur.AppID, 10)
	}
	appID, err := r.Value("github-app-id", "GitHub App ID (none to skip GitHub)", def)
	if err != nil {
		return GitHub{}, nil, err
	}
	if appID = strings.TrimSpace(appID); appID == "" || strings.EqualFold(appID, "none") {
		return GitHub{}, nil, nil
	}
	var g GitHub
	if g.AppID, err = parseID("github-app-id", appID); err != nil {
		return GitHub{}, nil, err
	}
	installation := ""
	if cur.InstallationID != 0 {
		installation = strconv.FormatInt(cur.InstallationID, 10)
	}
	v, err := r.Value("github-installation-id", "GitHub installation ID", installation)
	if err != nil {
		return GitHub{}, nil, err
	}
	if g.InstallationID, err = parseID("github-installation-id", v); err != nil {
		return GitHub{}, nil, err
	}
	repos, err := r.Value("github-repositories", "GitHub repositories (owner/name, comma-separated)", strings.Join(cur.Repositories, ","))
	if err != nil {
		return GitHub{}, nil, err
	}
	for _, repo := range strings.Split(repos, ",") {
		if repo = strings.TrimSpace(repo); repo != "" {
			g.Repositories = append(g.Repositories, repo)
		}
	}
	ownerDef := cur.Owner
	if ownerDef == "" && len(g.Repositories) > 0 {
		ownerDef, _, _ = strings.Cut(g.Repositories[0], "/")
	}
	if g.Owner, err = r.Value("github-owner", "GitHub owner login", ownerDef); err != nil {
		return GitHub{}, nil, err
	}
	for repo, check := range cur.TestChecks { // test checks are chosen later; a re-run keeps those of repositories that stay
		if slices.Contains(g.Repositories, repo) {
			if g.TestChecks == nil {
				g.TestChecks = map[string]string{}
			}
			g.TestChecks[repo] = check
		}
	}
	return g, func() ([]byte, error) { return readAppKey(r) }, nil
}

func parseID(key, v string) (int64, error) {
	n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
	if err != nil || n < 1 {
		return 0, fmt.Errorf("%s %q is not a positive number", key, v)
	}
	return n, nil
}

// readAppKey reads the App's private key from the file named by github-app-key-file, or from stdin for -.
func readAppKey(r *Resolver) ([]byte, error) {
	path, err := r.Value("github-app-key-file", "File with the App's private key (PEM, - for standard input)", "")
	if err != nil {
		return nil, err
	}
	var pem []byte
	if path == "-" {
		pem, err = io.ReadAll(r.in)
	} else {
		pem, err = os.ReadFile(filepath.Clean(path))
	}
	if err != nil {
		return nil, fmt.Errorf("read the App key from %s: %w", path, err)
	}
	if !pemKey.Match(pem) {
		return nil, fmt.Errorf("%s is not a PEM private key (PKCS#1 or PKCS#8)", path)
	}
	return pem, nil
}
