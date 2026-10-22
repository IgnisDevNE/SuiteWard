package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
)

// The user of a database URL, read without parsing the URL: a parse error would repeat the URL, password included.
var urlUser = regexp.MustCompile(`^postgres://([^:/@]+):`)

// ErrForeignContainer means a container of the stack's name exists that this installer did not make.
var ErrForeignContainer = errors.New("a container of the stack's name exists that this installer did not make")

// installTarget is where the install keeps its settings and what is already there.
type installTarget struct {
	settingsPath string
	defaultPath  bool // the settings file is the per-user default, shared by every instance on this PC
	saved        Settings
	haveSaved    bool
}

// guardDefaultSettings refuses to take over the default settings file of another instance on this PC: one that belongs to
// the other runtime, or to another Podman connection. A second instance keeps files of its own.
func guardDefaultSettings(t installTarget, runtimeName, connection string) error {
	if !t.defaultPath || !t.haveSaved {
		return nil
	}
	other := ""
	switch {
	case t.saved.Runtime != runtimeName:
		other = "the " + t.saved.Runtime + " instance"
	case t.saved.Connection != connection:
		other = fmt.Sprintf("the instance on the connection %q", t.saved.Connection)
	default:
		return nil
	}
	return fmt.Errorf("the default settings file %s belongs to %s of this PC, and this command would take it over: "+
		"pass --settings and --token-file with files of their own for a second instance, or remove the other instance first",
		t.settingsPath, other)
}

// installColocated is the co-located install: choose and check everything first, then bring the stack up.
func installColocated(ctx context.Context, r *Resolver, env Env, t installTarget) error {
	rt, connection, err := openRuntime(ctx, r, env, t.saved, true)
	if err != nil {
		return err
	}
	if err := guardDefaultSettings(t, rt.Name(), connection); err != nil {
		return err
	}
	base, previous, err := existingStack(ctx, rt, t.saved, t.haveSaved)
	if err != nil {
		return err
	}
	// A different release than the one that runs is an upgrade: it backs up first, so it has its own command.
	if asked, ok := r.given("image"); ok && previous.Known && previous.Image != "" {
		want, err := parseImage(asked)
		if err != nil {
			return err
		}
		if !sameImage(want, previous.Image) {
			return fmt.Errorf("--image names %s but %s is installed: changing the release is an upgrade, which backs up first; use `suiteward install --upgrade`", want, previous.Image)
		}
	}
	s, err := chooseSettings(r, base, rt.Name(), connection)
	if err != nil {
		return err
	}
	// The installed image is kept by a re-run; a fresh install takes the release this binary carries (a development build
	// carries none, and then --image is required).
	defaultImage := previous.Image
	if embedded, ok := ReleaseImage(); defaultImage == "" && ok {
		defaultImage = embedded
	}
	asked, err := r.Value("image", "Release image (pinned by digest)", defaultImage)
	if err != nil {
		return err
	}
	image, err := parseImage(asked)
	if err != nil {
		return err
	}
	s.Image = image
	c := colocatedInstall{githubInstall: githubInstall{rt: rt, env: env, out: env.Term.Out, s: s}, image: image, tokenFile: r.preset("token-file", ""), previous: previous}
	if c.tokenFile == "" {
		if c.tokenFile, err = defaultTokenFile(env.Deps); err != nil {
			return err
		}
	}
	if err := c.preflight(ctx); err != nil {
		return err
	}
	if err := c.githubFlow(ctx, r, filepath.Dir(t.settingsPath)); err != nil {
		return err
	}
	if c.stack, err = ColocatedStack(c.s, image); err != nil {
		return err
	}
	if err := c.bringUp(ctx, func() error { return SaveSettings(t.settingsPath, c.s) }); err != nil {
		return err
	}
	c.restartAfterReboot(ctx)
	return c.finish(ctx, t)
}

// openRuntime chooses the runtime and the connection and identifies the engine behind it. Docker needs a confirmation
// when the command installs.
func openRuntime(ctx context.Context, r *Resolver, env Env, saved Settings, confirm bool) (Runtime, string, error) {
	name := r.preset("runtime", saved.Runtime) // only an install asks which runtime to use
	var err error
	if confirm {
		if name, err = r.Value("runtime", "Container runtime (podman recommended, docker discouraged)", saved.Runtime); err != nil {
			return nil, "", err
		}
	}
	if name != RuntimePodman && name != RuntimeDocker {
		return nil, "", fmt.Errorf("runtime %q is not %s or %s", name, RuntimePodman, RuntimeDocker)
	}
	savedConnection := ""
	if saved.Runtime == name {
		savedConnection = saved.Connection
	}
	connection := r.preset("connection", savedConnection)
	var rt Runtime
	if name == RuntimeDocker {
		if confirm {
			if err := confirmDocker(r); err != nil {
				return nil, "", err
			}
		}
		rt, err = NewDocker(ctx, env.Deps, DockerOptions{Context: connection})
	} else {
		if connection, err = podmanConnection(ctx, env.Deps, connection); err != nil {
			return nil, "", err
		}
		rt, err = NewPodman(ctx, env.Deps, connection)
	}
	if err != nil {
		return nil, "", err
	}
	engine := rt.Engine()
	daemon := "a rootful daemon"
	if engine.Rootless {
		daemon = "rootless"
	}
	r.say("Using %s (%s).\n", strings.TrimSpace(engine.Name+" "+engine.Version), daemon)
	return rt, connection, nil
}

// chooseSettings resolves the ports and the job timeout over base, and validates the result. The GitHub values stay as
// base has them (the settings, or the adopted stack): the GitHub flow changes them later.
func chooseSettings(r *Resolver, base Settings, runtimeName, connection string) (Settings, error) {
	s := base
	s.Tier, s.Runtime, s.Connection = config.TierCoLocated, runtimeName, connection
	var err error
	if s.HTTPPort, err = r.Int("http-port", "HTTP port on 127.0.0.1", base.HTTPPort); err != nil {
		return Settings{}, err
	}
	if s.MCPPort, err = r.Int("mcp-port", "MCP port on 127.0.0.1", base.MCPPort); err != nil {
		return Settings{}, err
	}
	s.JobTimeout = r.preset("job-timeout", base.JobTimeout)
	return s, s.Validate()
}

// preflight does everything that can fail without changing anything, so a refusal leaves a running stack alone.
func (c *colocatedInstall) preflight(ctx context.Context) error {
	if err := c.checkPorts(ctx); err != nil {
		return err
	}
	var err error
	if c.secrets, err = c.readSecretState(ctx); err != nil {
		return err
	}
	if err := c.checkTokenFile(); err != nil {
		return err
	}
	return c.pullImages(ctx)
}

// pullImages pulls the release image and the database image. Pulling before anything is removed means a wrong digest is found
// while the running service is still there. An image the engine already has is not pulled, so an offline re-run works and a
// locally built image can be named by its digest.
func (c *colocatedInstall) pullImages(ctx context.Context) error {
	postgres, err := PostgresImage()
	if err != nil {
		return err
	}
	for _, img := range []string{c.image, postgres} {
		if c.rt.ImageExists(ctx, img) {
			continue
		}
		if err := c.rt.Pull(ctx, img); err != nil {
			return err
		}
	}
	return nil
}

// stackInstalled is what an existing installation tells the next run.
type stackInstalled struct {
	Settings Settings
	Known    bool // an installation exists and its ports are in Settings
	Image    string
}

// existingStack finds out what is installed. Podman stacks are adopted from their containers: any difference stops the
// command, and the carried fields become the defaults. A Docker stack keeps its secrets in volumes and is known only by
// the settings file of the install that made it.
func existingStack(ctx context.Context, rt Runtime, saved Settings, haveSaved bool) (Settings, stackInstalled, error) {
	return existingStackFor(ctx, rt, saved, haveSaved, false)
}

// existingStackFor is existingStack; with refreshDB (an upgrade) a database on another PostgreSQL pin is no difference.
func existingStackFor(ctx context.Context, rt Runtime, saved Settings, haveSaved bool, refreshDB bool) (Settings, stackInstalled, error) {
	serve, err := rt.Exists(ctx, KindContainer, ContainerServe)
	if err != nil {
		return Settings{}, stackInstalled{}, err
	}
	base := saved
	known := stackInstalled{Settings: saved, Known: serve && haveSaved, Image: saved.Image}
	if !serve {
		return base, stackInstalled{Image: saved.Image}, nil
	}
	if rt.Name() == RuntimeDocker {
		if !haveSaved || saved.Runtime != RuntimeDocker {
			return Settings{}, stackInstalled{}, fmt.Errorf("%w: %s; remove it or use the settings file of the install that made it", ErrForeignContainer, ContainerServe)
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
	adopted, err := adoptContainers(raw, refreshDB)
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

type colocatedInstall struct {
	githubInstall
	image     string
	previous  stackInstalled
	stack     Stack
	tokenFile string
	secrets   secretState

	machineUser string // the rootless user of the Podman machine, once it has been looked up
}

// secretState is what the stack already has of its database secrets, read before anything is changed.
type secretState struct {
	haveDB, haveOwner, haveToken bool
	needAppURL                   bool // the application URL secret is missing, still the owner URL of a v0.1.x stack, or differs between volumes
}

// inMachine says whether the stack runs in a Podman machine (Windows, macOS): its ports are published there, not on this
// PC, and the ssh user of the machine is how to reach it.
func (c colocatedInstall) inMachine() bool {
	_, podman := c.rt.(*Podman)
	return podman && c.env.Deps.GOOS != "linux" && c.s.Connection != ""
}

// lookUpMachineUser finds the rootless user of the machine once.
func (c *colocatedInstall) lookUpMachineUser(ctx context.Context) (string, error) {
	if c.machineUser != "" {
		return c.machineUser, nil
	}
	user, err := connectionUser(ctx, c.rt.(*Podman), c.s.Connection)
	c.machineUser = user
	return user, err
}

// checkPorts refuses a loopback port that something else listens on: on this PC, or inside the Podman machine that
// publishes it. The stack's own service holds its ports while it runs (it is replaced below), so a port it already
// publishes is not a conflict.
func (c *colocatedInstall) checkPorts(ctx context.Context) error {
	free := c.env.PortFree
	where := "on 127.0.0.1"
	if c.inMachine() {
		listening, err := c.machineListening(ctx)
		if err != nil {
			c.say("Note: the ports could not be checked inside the Podman machine (%v); a port that is in use shows up when the service starts.\n", err)
			return nil
		}
		free = func(port int) error {
			if listening[port] {
				return errors.New("something listens on it")
			}
			return nil
		}
		where = "inside the Podman machine " + c.s.Connection
	}
	for _, p := range []struct {
		flag      string
		port, old int
	}{{"--http-port", c.s.HTTPPort, c.previous.Settings.HTTPPort}, {"--mcp-port", c.s.MCPPort, c.previous.Settings.MCPPort}} {
		if c.previous.Known && p.port == p.old {
			continue
		}
		if err := free(p.port); err != nil {
			return fmt.Errorf("port %d %s is in use (%v): choose another with %s", p.port, where, err, p.flag)
		}
	}
	return nil
}

// machineListening lists the TCP ports something listens on inside the Podman machine.
func (c *colocatedInstall) machineListening(ctx context.Context) (map[int]bool, error) {
	user, err := c.lookUpMachineUser(ctx)
	if err != nil {
		return nil, err
	}
	out, err := c.env.Deps.Exec(ctx, "podman", []string{"machine", "ssh", "--username", user, c.s.Connection, "ss -ltn"}, nil)
	if err != nil {
		return nil, fmt.Errorf("ss in the machine: %w", err)
	}
	listening := map[int]bool{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 || fields[0] != "LISTEN" {
			continue
		}
		local := fields[3] // Local Address:Port
		if i := strings.LastIndex(local, ":"); i >= 0 {
			if port, err := strconv.Atoi(local[i+1:]); err == nil {
				listening[port] = true
			}
		}
	}
	return listening, nil
}

// readSecretState reads which database secrets exist and refuses a state the install cannot repair.
func (c colocatedInstall) readSecretState(ctx context.Context) (secretState, error) {
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
	haveToken, err := secrets.Exists(ctx, SecretMCPAuth)
	if err != nil {
		return secretState{}, err
	}
	state := secretState{haveDB: haveDB, haveOwner: haveOwner, haveToken: haveToken, needAppURL: !haveURL}
	if !haveURL {
		return state, nil
	}
	current, err := secrets.Read(ctx, SecretDatabaseURL)
	if err != nil {
		return secretState{}, err
	}
	m := urlUser.FindSubmatch(current)
	if m == nil {
		return secretState{}, fmt.Errorf("%s is not a PostgreSQL URL; remove the stack and its secrets to start over", SecretDatabaseURL)
	}
	// A v0.1.x stack has the owner URL where the application URL is now: its user is the owner.
	if string(m[1]) == ownerRole {
		c.say("Upgrading an existing stack: %s held the owner URL. Creating the role %s and replacing the secret with its URL; the owner URL is now in %s and only the migrate container gets it.\n",
			SecretDatabaseURL, appRole, SecretDatabaseOwnerURL)
		state.needAppURL = true
	}
	// Docker keeps one copy per container, and a write that was interrupted between two of them leaves them different.
	if checker, ok := secrets.(interface {
		Consistent(ctx context.Context, name string) (bool, error)
	}); ok {
		same, err := checker.Consistent(ctx, SecretDatabaseURL)
		if err != nil {
			return secretState{}, err
		}
		if !same {
			c.say("The copies of %s differ (an earlier install was interrupted): the role gets a new password and every copy is rewritten.\n", SecretDatabaseURL)
			state.needAppURL = true
		}
	}
	return state, nil
}

// bringUp creates what is missing and starts the stack in the order of the quadlets: database, role, migrate, service.
// save runs once migrate has succeeded and before the service starts: a service that runs without settings is a container
// that a later run cannot tell from a stranger's.
func (c colocatedInstall) bringUp(ctx context.Context, save func() error) error {
	secrets := c.rt.Secrets()

	if err := c.ensure(ctx, KindNetwork, NetworkName); err != nil {
		return err
	}
	for _, volume := range []string{VolumeData, VolumeDBData} {
		if err := c.ensure(ctx, KindVolume, volume); err != nil {
			return err
		}
	}

	if err := c.ensureOwnerSecrets(ctx); err != nil {
		return err
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
	c.say("Waiting for %s to become healthy (up to 90 s)...\n", ContainerDB)
	if err := c.waitHealthy(ctx); err != nil {
		return err
	}
	if err := c.ensureRole(ctx, c.secrets.needAppURL); err != nil {
		return err
	}
	if err := c.rt.Run(ctx, c.stack.Migrate); err != nil {
		return migratingError{fmt.Errorf("suiteward migrate failed, so the service was not started: %w", err)}
	}
	c.say("suiteward migrate finished: the schema is up to date.\n")
	if err := save(); err != nil {
		return migratingError{err}
	}
	if err := c.rt.Run(ctx, c.stack.Serve); err != nil {
		return migratingError{err}
	}
	github := "not configured"
	if c.s.GitHub.AppID != 0 {
		github = "configured"
	}
	c.say("Started %s and %s (%s), GitHub %s.\n", ContainerDB, ContainerServe, c.image, github)
	return nil
}

// ensureOwnerSecrets makes the owner password and the owner URL. The password is generated once; an existing one is kept,
// because replacing it would lock the owner out.
func (c colocatedInstall) ensureOwnerSecrets(ctx context.Context) error {
	if c.secrets.haveOwner {
		return nil
	}
	secrets := c.rt.Secrets()
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
	return secrets.Write(ctx, SecretDatabaseOwnerURL, []byte(DatabaseURL(ownerRole, password)), false)
}

func (c colocatedInstall) ensure(ctx context.Context, kind Kind, name string) error {
	have, err := c.rt.Exists(ctx, kind, name)
	if err != nil || have {
		return err
	}
	return c.rt.Create(ctx, kind, name)
}

// checkTokenFile refuses to overwrite a token copy that this instance did not make: the file exists, but this instance has
// no MCP token secret, so the file belongs to another instance (or is left by an interrupted install of this one).
func (c colocatedInstall) checkTokenFile() error {
	if c.secrets.haveToken {
		return nil
	}
	if _, err := os.Stat(c.tokenFile); err == nil {
		return fmt.Errorf("%s exists, but this instance has no %s secret, so the file belongs to another instance: "+
			"pass --token-file and --settings with files of their own for a second instance, remove the other instance first, "+
			"or delete the file if an interrupted install left it", c.tokenFile, SecretMCPAuth)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("look for %s: %w", c.tokenFile, err)
	}
	return nil
}

// ensureMCPToken makes the MCP token once. Its copy for the client is written first and restricted to this user, so a
// failure after it leaves a file the next run refuses to overwrite (checkTokenFile says what to do).
func (c colocatedInstall) ensureMCPToken(ctx context.Context) error {
	if c.secrets.haveToken {
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
	if err := restrictPath(ctx, d, path, false); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(token), 0o600); err != nil {
		return fmt.Errorf("write %s: %w", path, err)
	}
	return nil
}

// waitHealthy polls the database for 90 s, one check a second.
func (c colocatedInstall) waitHealthy(ctx context.Context) error {
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

// ensureRole creates the application role, or gives it a new password when its secret is missing, still the owner's or
// different between volumes, so a purged database or an interrupted conversion is repaired.
func (c colocatedInstall) ensureRole(ctx context.Context, needAppURL bool) error {
	exists, err := appRoleExists(ctx, c.rt)
	if err != nil {
		return err
	}
	if !needAppURL && exists {
		return nil
	}
	password, err := setAppRolePassword(ctx, c.rt, exists)
	if err != nil {
		return err
	}
	return c.rt.Secrets().Write(ctx, SecretDatabaseURL, []byte(DatabaseURL(appRole, password)), true)
}

// restartAfterReboot enables podman-restart.service of the rootless user, which is what starts a --restart always
// container after the machine restarts. A failure is reported, not fatal.
func (c *colocatedInstall) restartAfterReboot(ctx context.Context) {
	if _, ok := c.rt.(*Podman); !ok {
		return
	}
	const unit = "podman-restart.service"
	d := c.env.Deps
	var err error
	if machine := c.s.Connection; machine == "" && d.GOOS == "linux" {
		_, err = d.Exec(ctx, "systemctl", []string{"--user", "enable", unit}, nil)
	} else {
		var user string
		if user, err = c.lookUpMachineUser(ctx); err == nil {
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

// connectionUser is the user of the machine connection's ssh URI: the machine's rootless user.
func connectionUser(ctx context.Context, p *Podman, connection string) (string, error) {
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

// finish runs setup, or says how, and prints the next steps.
func (c *colocatedInstall) finish(ctx context.Context, t installTarget) error {
	c.say("The MCP token for a client is in %s (it is not printed).\n", c.tokenFile)
	c.say("Read the status of the service: %s\n", c.statusHint(ctx))
	doctor := "suiteward doctor"
	if !t.defaultPath {
		doctor += " --settings " + t.settingsPath
	}
	if c.s.GitHub.AppID == 0 {
		c.say("GitHub is not configured. Run the install again with --github yes (or the GitHub values) to set it up, then enroll the owner.\n")
		return nil
	}
	if err := c.enroll(ctx, c.readStatus); err != nil {
		return err
	}
	c.say("Then check the instance with `%s`.\n", doctor)
	return nil
}

// statusHint is the command that reads /status of the service: inside the Podman machine when the port is published there.
func (c *colocatedInstall) statusHint(ctx context.Context) string {
	curl := fmt.Sprintf("curl -s http://127.0.0.1:%d/status", c.s.HTTPPort)
	if !c.inMachine() {
		return curl
	}
	user, err := c.lookUpMachineUser(ctx)
	if err != nil {
		user = "user"
	}
	return fmt.Sprintf("podman machine ssh --username %s %s %q", user, c.s.Connection, curl)
}

// engineCommand is how to run the engine's CLI by hand against this stack's engine.
func engineCommand(rt Runtime, goos string) (string, error) {
	var c cli
	switch r := rt.(type) {
	case *Podman:
		c = r.cli
	case *Docker:
		c = r.cli
	default:
		return "", fmt.Errorf("%T is not a container engine this installer drives", rt)
	}
	bin := c.bin
	if strings.Contains(bin, " ") {
		bin = `"` + bin + `"`
		if goos == "windows" {
			bin = "& " + bin
		}
	}
	return strings.Join(append([]string{bin}, c.base...), " "), nil
}
