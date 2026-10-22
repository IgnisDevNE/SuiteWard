package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

const (
	appRole   = "suiteward_app" // the role serve connects as: least privilege, created here once
	ownerRole = "suiteward"     // the bootstrap role, used only by migrate
)

// The user of a database URL, read without parsing the URL: a parse error would repeat the URL, password included.
var urlUser = regexp.MustCompile(`^postgres://([^:/@]+):`)

// ErrForeignContainer means a container of the stack's name exists that this installer did not make.
var ErrForeignContainer = errors.New("a container of the stack's name exists that this installer did not make")

// installColocated is the co-located install: choose and check everything first, then bring the stack up.
func installColocated(ctx context.Context, r *Resolver, env Env, settingsPath string, saved Settings, haveSaved bool) error {
	runtimeName, err := r.Value("runtime", "Container runtime (podman recommended, docker discouraged)", saved.Runtime)
	if err != nil {
		return err
	}
	if runtimeName != RuntimePodman && runtimeName != RuntimeDocker {
		return fmt.Errorf("runtime %q is not %s or %s", runtimeName, RuntimePodman, RuntimeDocker)
	}
	savedConnection := ""
	if saved.Runtime == runtimeName {
		savedConnection = saved.Connection
	}
	connection := r.preset("connection", savedConnection)
	var rt Runtime
	if runtimeName == RuntimeDocker {
		if err := confirmDocker(r); err != nil {
			return err
		}
		if rt, err = NewDocker(ctx, env.Deps, DockerOptions{Context: connection}); err != nil {
			return err
		}
	} else {
		if connection, err = podmanConnection(ctx, env.Deps, connection); err != nil {
			return err
		}
		if rt, err = NewPodman(ctx, env.Deps, connection); err != nil {
			return err
		}
	}
	engine := rt.Engine()
	r.say("Using %s (%s).\n", strings.TrimSpace(engine.Name+" "+engine.Version), map[bool]string{true: "rootless", false: "a rootful daemon"}[engine.Rootless])

	base, previous, err := existingStack(ctx, rt, saved, haveSaved)
	if err != nil {
		return err
	}
	s := base
	s.Tier, s.Runtime, s.Connection = config.TierCoLocated, runtimeName, connection
	if s.HTTPPort, err = r.Int("http-port", "HTTP port on 127.0.0.1", base.HTTPPort); err != nil {
		return err
	}
	if s.MCPPort, err = r.Int("mcp-port", "MCP port on 127.0.0.1", base.MCPPort); err != nil {
		return err
	}
	s.JobTimeout = r.preset("job-timeout", base.JobTimeout)
	github, keyFile, err := githubSettings(r, base.GitHub)
	if err != nil {
		return err
	}
	s.GitHub = github
	if err := s.Validate(); err != nil {
		return err
	}
	image, err := r.Value("image", "Release image (pinned by digest)", previous.Image)
	if err != nil {
		return err
	}
	if !digestImage.MatchString(image) {
		return fmt.Errorf("%w: %s", ErrNotDigestImage, image)
	}

	c := colocated{rt: rt, env: env, out: env.Term.Out, s: s, image: image, tokenFile: r.preset("token-file", "")}
	if c.tokenFile == "" {
		if c.tokenFile, err = defaultTokenFile(env.Deps); err != nil {
			return err
		}
	}
	if c.stack, err = ColocatedStack(s, image); err != nil {
		return err
	}
	// Everything that can fail without changing anything comes first, so a refusal leaves a running stack alone.
	if github.AppID != 0 {
		if have, err := rt.Secrets().Exists(ctx, SecretGitHubAppKey); err != nil {
			return err
		} else if !have {
			if c.githubKey, err = keyFile(); err != nil {
				return err
			}
		}
	}
	if err := c.checkPorts(previous.Settings, previous.Known); err != nil {
		return err
	}
	if c.secrets, err = c.readSecretState(ctx); err != nil {
		return err
	}
	for _, img := range []string{image, c.stack.DB.Image} {
		if err := rt.Pull(ctx, img); err != nil {
			return err
		}
	}

	if err := c.bringUp(ctx); err != nil {
		return err
	}
	if err := SaveSettings(settingsPath, s); err != nil {
		return err
	}
	c.restartAfterReboot(ctx)
	c.nextSteps()
	return nil
}

// stackInstalled is what an existing installation tells the next run.
type stackInstalled struct {
	Settings Settings
	Known    bool // an installation exists and its ports are in Settings
	Image    string
}

// existingStack finds out what is installed. Podman stacks are adopted from their containers: any difference stops the
// install, and the carried fields become the defaults. A Docker stack keeps its secrets in volumes and is known only by
// the settings file of the install that made it.
func existingStack(ctx context.Context, rt Runtime, saved Settings, haveSaved bool) (Settings, stackInstalled, error) {
	serve, err := rt.Exists(ctx, KindContainer, ContainerServe)
	if err != nil {
		return Settings{}, stackInstalled{}, err
	}
	base := saved
	known := stackInstalled{Settings: saved, Known: serve && haveSaved}
	if !serve {
		return base, stackInstalled{}, nil
	}
	if rt.Name() == RuntimeDocker {
		if !haveSaved {
			return Settings{}, stackInstalled{}, fmt.Errorf("%w: %s; remove it or install with the settings file of the install that made it", ErrForeignContainer, ContainerServe)
		}
		return base, known, nil
	}
	raw := map[string][]byte{}
	for _, name := range []string{ContainerDB, ContainerMigrate, ContainerServe} {
		ok, err := rt.Exists(ctx, KindContainer, name)
		if err != nil {
			return Settings{}, stackInstalled{}, err
		}
		if !ok {
			continue
		}
		if raw[name], err = rt.Inspect(ctx, name); err != nil {
			return Settings{}, stackInstalled{}, err
		}
	}
	adopted, err := AdoptContainers(raw)
	if err != nil {
		return Settings{}, stackInstalled{}, err
	}
	base = adopted.Settings
	base.GitHub.TestChecks = saved.GitHub.TestChecks
	return base, stackInstalled{Settings: adopted.Settings, Known: true, Image: adopted.Image}, nil
}

// podmanConnection is the connection to use: the one asked for, CONTAINER_CONNECTION, or off Linux the single rootless
// machine connection. It is chosen here, not inside NewPodman, so that the install can record it.
func podmanConnection(ctx context.Context, d Deps, asked string) (string, error) {
	if asked == "" {
		asked = d.Getenv("CONTAINER_CONNECTION")
	}
	if asked != "" || d.GOOS == "linux" {
		return asked, nil
	}
	if err := d.validate(); err != nil {
		return "", err
	}
	return (&Podman{cli: cli{bin: "podman", d: d}}).rootlessMachineConnection(ctx)
}

// defaultTokenFile is where the copy of the MCP token for a client goes: outside the repository and outside any
// roaming profile, as local.ps1 kept it.
func defaultTokenFile(d Deps) (string, error) {
	if d.GOOS == "windows" {
		local := d.Getenv("LOCALAPPDATA")
		if local == "" {
			return "", errors.New("LOCALAPPDATA is not set: the MCP token copy for a client lives there")
		}
		return filepath.Join(local, "SuiteWard", "mcp-token"), nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate the user configuration directory: %w", err)
	}
	return filepath.Join(dir, "suiteward", "mcp-token"), nil
}

type colocated struct {
	rt        Runtime
	env       Env
	out       io.Writer
	s         Settings
	image     string
	stack     Stack
	tokenFile string
	githubKey []byte // the App key to store, nil when its secret exists or GitHub is off
	secrets   secretState
}

// secretState is what the stack already has of its database secrets, read before anything is changed.
type secretState struct {
	haveDB, haveOwner bool
	needAppURL        bool // the application URL secret is missing, or still the owner URL of a v0.1.x stack
}

func (c colocated) say(format string, args ...any) { _, _ = fmt.Fprintf(c.out, format, args...) }

// checkPorts refuses a loopback port that something else listens on. The stack's own service holds its ports while it runs
// (it is replaced below), so a port it already publishes is not a conflict.
func (c colocated) checkPorts(previous Settings, known bool) error {
	for _, p := range []struct {
		flag      string
		port, old int
	}{{"--http-port", c.s.HTTPPort, previous.HTTPPort}, {"--mcp-port", c.s.MCPPort, previous.MCPPort}} {
		if known && p.port == p.old {
			continue
		}
		if err := c.env.PortFree(p.port); err != nil {
			return fmt.Errorf("port %d on 127.0.0.1 is in use (%v): choose another with %s", p.port, err, p.flag)
		}
	}
	return nil
}

// readSecretState reads which database secrets exist and refuses a state the install cannot repair.
func (c colocated) readSecretState(ctx context.Context) (secretState, error) {
	secrets := c.rt.Secrets()
	haveDB, err := secrets.Exists(ctx, SecretDBAuth)
	if err != nil {
		return secretState{}, err
	}
	haveOwner, err := secrets.Exists(ctx, SecretDatabaseOwnerURL)
	if err != nil {
		return secretState{}, err
	}
	haveURL, err := secrets.Exists(ctx, SecretDatabaseURL)
	if err != nil {
		return secretState{}, err
	}
	if !haveDB && (haveOwner || haveURL) {
		return secretState{}, fmt.Errorf("database secrets exist without %s; remove the stack and its secrets to start over", SecretDBAuth)
	}
	// A v0.1.x stack has the owner URL where the application URL is now: its user is the owner.
	state := secretState{haveDB: haveDB, haveOwner: haveOwner, needAppURL: !haveURL}
	if haveURL {
		current, err := secrets.Read(ctx, SecretDatabaseURL)
		if err != nil {
			return secretState{}, err
		}
		m := urlUser.FindSubmatch(current)
		if m == nil {
			return secretState{}, fmt.Errorf("%s is not a PostgreSQL URL; remove the stack and its secrets to start over", SecretDatabaseURL)
		}
		if string(m[1]) == ownerRole {
			c.say("Upgrading an existing stack: %s held the owner URL. Creating the role %s and replacing the secret with its URL; the owner URL is now in %s and only the migrate container gets it.\n",
				SecretDatabaseURL, appRole, SecretDatabaseOwnerURL)
			state.needAppURL = true
		}
	}
	return state, nil
}

// bringUp creates what is missing and starts the stack in the order of the quadlets: database, role, migrate, service.
func (c colocated) bringUp(ctx context.Context) error {
	secrets := c.rt.Secrets()

	if err := c.ensure(ctx, KindNetwork, NetworkName); err != nil {
		return err
	}
	for _, volume := range []string{VolumeData, VolumeDBData} {
		if err := c.ensure(ctx, KindVolume, volume); err != nil {
			return err
		}
	}

	// The owner password is generated once; an existing one is kept, because replacing it would lock the owner out.
	if !c.secrets.haveOwner {
		var password string
		if c.secrets.haveDB {
			existing, err := secrets.Read(ctx, SecretDBAuth)
			if err != nil {
				return err
			}
			password = string(existing)
		} else {
			var err error
			if password, err = NewPassword(); err != nil {
				return err
			}
			if err := secrets.Write(ctx, SecretDBAuth, []byte(password), false); err != nil {
				return err
			}
		}
		if err := secrets.Write(ctx, SecretDatabaseOwnerURL, []byte(DatabaseURL(ownerRole, password)), false); err != nil {
			return err
		}
	}
	if err := c.ensureMCPToken(ctx); err != nil {
		return err
	}
	if c.githubKey != nil {
		if err := secrets.Write(ctx, SecretGitHubAppKey, c.githubKey, false); err != nil {
			return err
		}
	}

	// The old service, and a migrate container an interrupted run left, go first: the database restarts below and the
	// application secret may be replaced.
	for _, name := range []string{ContainerServe, ContainerMigrate} {
		if err := c.rt.Remove(ctx, name); err != nil {
			return err
		}
	}
	if err := c.rt.Run(ctx, c.stack.DB); err != nil {
		return err
	}
	if err := c.waitHealthy(ctx); err != nil {
		return err
	}
	if err := c.ensureRole(ctx, c.secrets.needAppURL); err != nil {
		return err
	}
	if err := c.rt.Run(ctx, c.stack.Migrate); err != nil {
		return fmt.Errorf("suiteward migrate failed, so the service was not started: %w", err)
	}
	if err := c.rt.Run(ctx, c.stack.Serve); err != nil {
		return err
	}
	c.say("Started %s and %s (%s), GitHub %s.\n", ContainerDB, ContainerServe, c.image, map[bool]string{true: "configured", false: "not configured"}[c.s.GitHub.AppID != 0])
	return nil
}

func (c colocated) ensure(ctx context.Context, kind Kind, name string) error {
	have, err := c.rt.Exists(ctx, kind, name)
	if err != nil || have {
		return err
	}
	return c.rt.Create(ctx, kind, name)
}

// ensureMCPToken makes the MCP token once. Its copy for the client is written first and restricted to this user, so a
// failure after it leaves a file the next run overwrites.
func (c colocated) ensureMCPToken(ctx context.Context) error {
	have, err := c.rt.Secrets().Exists(ctx, SecretMCPAuth)
	if err != nil {
		return err
	}
	if have {
		if _, err := os.Stat(c.tokenFile); err != nil {
			c.say("Warning: the secret %s exists but %s does not. Remove the stack and its secrets to generate a new token.\n", SecretMCPAuth, c.tokenFile)
		}
		return nil
	}
	token, err := NewMCPToken()
	if err != nil {
		return err
	}
	if err := writeTokenFile(ctx, c.env.Deps, c.tokenFile, token); err != nil {
		return err
	}
	return c.rt.Secrets().Write(ctx, SecretMCPAuth, []byte(token), false)
}

// writeTokenFile writes the token to a file only this user can read: an ACL on Windows, mode 0600 elsewhere.
func writeTokenFile(ctx context.Context, d Deps, path, token string) error {
	path = filepath.Clean(path)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create the directory of %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if d.GOOS == "windows" {
		user := d.Getenv("USERNAME")
		if user == "" {
			return errors.New("USERNAME is not set, so the token copy cannot be restricted to this user")
		}
		if _, err := d.Exec(ctx, "icacls", []string{path, "/inheritance:r", "/grant:r", user + ":F"}, nil); err != nil {
			return fmt.Errorf("restrict %s to this user: %w", path, err)
		}
	} else if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("restrict %s to this user: %w", path, err)
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// waitHealthy polls the database for 90 s, one check a second.
func (c colocated) waitHealthy(ctx context.Context) error {
	logs := c.rt.Name() + " logs " + ContainerDB
	for range 90 {
		if err := ctx.Err(); err != nil {
			return err
		}
		state, err := c.rt.State(ctx, ContainerDB)
		if err != nil {
			return err
		}
		switch {
		case state.Status == "running" && state.Health == "healthy":
			return nil
		case slices.Contains([]string{"exited", "stopped", "dead"}, state.Status):
			return fmt.Errorf("%s exited before it became healthy (%s)", ContainerDB, logs)
		}
		c.env.Sleep(time.Second)
	}
	return fmt.Errorf("%s did not become healthy within 90 s (%s)", ContainerDB, logs)
}

// psql runs SQL as the database owner over the container's local socket. The SQL goes in on stdin: an argument would show
// in the process list. A sensitive statement carries a password and a failing statement echoes itself, so its error says
// only that it failed.
func (c colocated) psql(ctx context.Context, sql string, sensitive bool) (string, error) {
	argv := []string{"psql", "-X", "-q", "-t", "-A", "-v", "ON_ERROR_STOP=1", "-U", ownerRole, "-d", "suiteward", "-f", "-"}
	out, err := c.rt.Exec(ctx, ContainerDB, argv, strings.NewReader(sql))
	if err != nil {
		if sensitive {
			return "", fmt.Errorf("psql in %s failed (the statement is not shown: it carries a password)", ContainerDB)
		}
		return "", fmt.Errorf("psql in %s failed: %w", ContainerDB, err)
	}
	return strings.TrimSpace(string(out)), nil
}

// ensureRole creates the application role, or gives it a new password when its secret is missing or still the owner's,
// so a purged database or an interrupted conversion is repaired. Statements are not logged, not even a failing one.
func (c colocated) ensureRole(ctx context.Context, needAppURL bool) error {
	exists, err := c.psql(ctx, "SELECT 1 FROM pg_roles WHERE rolname = '"+appRole+"'", false)
	if err != nil {
		return err
	}
	if !needAppURL && exists == "1" {
		return nil
	}
	password, err := NewPassword()
	if err != nil {
		return err
	}
	statement := "CREATE ROLE " + appRole + " LOGIN PASSWORD '" + password + "'"
	if exists == "1" {
		statement = "ALTER ROLE " + appRole + " PASSWORD '" + password + "'"
	}
	if _, err := c.psql(ctx, "SET log_statement = 'none'; SET log_min_error_statement = 'panic'; SET log_min_duration_statement = -1; "+statement+";", true); err != nil {
		return err
	}
	return c.rt.Secrets().Write(ctx, SecretDatabaseURL, []byte(DatabaseURL(appRole, password)), true)
}

// restartAfterReboot enables podman-restart.service of the rootless user, which is what starts a --restart always
// container after the machine restarts. A failure is reported, not fatal.
func (c colocated) restartAfterReboot(ctx context.Context) {
	podman, ok := c.rt.(*Podman)
	if !ok {
		return
	}
	const unit = "podman-restart.service"
	d := c.env.Deps
	var err error
	switch machine := c.s.Connection; {
	case machine == "" && d.GOOS == "linux":
		_, err = d.Exec(ctx, "systemctl", []string{"--user", "enable", unit}, nil)
	default:
		var user string
		if user, err = machineUser(ctx, podman, machine); err == nil {
			_, err = d.Exec(ctx, "podman", []string{"machine", "ssh", "--username", user, machine, "systemctl", "--user", "enable", unit}, nil)
		}
	}
	if err != nil {
		c.say("Note: %s could not be enabled (%v). Without it, containers do not restart after the machine restarts: enable it as the rootless user (systemctl --user enable %s).\n", unit, err, unit)
	}
	if d.GOOS != "linux" {
		c.say("Note: the Podman machine is not started at logon. After this PC restarts, run `podman machine start`; the stack comes back with it.\n")
	}
}

// machineUser is the user of the machine connection's ssh URI: the machine's rootless user.
func machineUser(ctx context.Context, p *Podman, connection string) (string, error) {
	out, err := p.run(ctx, nil, "system", "connection", "list", "--format", "json")
	if err != nil {
		return "", fmt.Errorf("list the Podman connections: %w", err)
	}
	var listed []struct{ Name, URI string }
	if err := json.Unmarshal(out, &listed); err != nil {
		return "", fmt.Errorf("read the Podman connections: %w", err)
	}
	for _, l := range listed {
		if l.Name != connection {
			continue
		}
		u, err := url.Parse(l.URI)
		if err != nil || u.User.Username() == "" {
			return "", fmt.Errorf("the connection %s has no ssh user", connection)
		}
		return u.User.Username(), nil
	}
	return "", fmt.Errorf("%q is not a Podman machine connection", connection)
}

// nextSteps prints what to do now: setup is run inside the service container with the real terminal.
func (c colocated) nextSteps() {
	c.say("The MCP token for a client is in %s (it is not printed).\n", c.tokenFile)
	g := c.s.GitHub
	if g.AppID == 0 {
		c.say("GitHub is not configured. Run the install again with the GitHub values before setup.\n")
		return
	}
	cmd := engineCommand(c.rt, c.env.Deps.GOOS)
	var checks []string
	for _, repo := range g.Repositories {
		checks = append(checks, "--test-check '"+repo+"=<check name>'")
	}
	c.say("Next: turn off any session recording of this terminal (setup prints the TOTP secret), then enroll the owner:\n  %s exec -it %s /suiteward setup --owner %s %s\n",
		cmd, ContainerServe, g.Owner, strings.Join(checks, " "))
}

// engineCommand is how to run the engine's CLI by hand against this stack's engine.
func engineCommand(rt Runtime, goos string) string {
	var c cli
	switch r := rt.(type) {
	case *Podman:
		c = r.cli
	case *Docker:
		c = r.cli
	}
	bin := c.bin
	if strings.Contains(bin, " ") {
		bin = `"` + bin + `"`
		if goos == "windows" {
			bin = "& " + bin
		}
	}
	return strings.Join(append([]string{bin}, c.base...), " ")
}
