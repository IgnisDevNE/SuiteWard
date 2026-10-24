package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/installer/ui"
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
	scr := r.screen
	scr.Steps(colocatedInstallSteps...)
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
			return fmt.Errorf("--image names %s but %s is installed: changing the release is an upgrade, which backs up first; use `%s install --upgrade`", want, previous.Image, env.Deps.command())
		}
	}
	s, err := chooseSettings(r, base, rt.Name(), connection)
	if err != nil {
		return err
	}
	image, err := askImage(r, previous)
	if err != nil {
		return err
	}
	s.Image = image
	c := colocatedInstall{githubInstall: githubInstall{rt: rt, env: env, out: env.Term.Out, s: s, pendingPath: pendingAppPath(t.settingsPath), enrollHint: enrollCommand(env.Deps.command(), r, false)}, image: image, tokenFile: r.preset("token-file", ""), previous: previous}
	if c.tokenFile == "" {
		if c.tokenFile, err = defaultTokenFile(env.Deps); err != nil {
			return err
		}
	}
	scr.Step(2)
	if err := c.preflight(ctx); err != nil {
		return err
	}
	scr.Step(3)
	if err := c.githubFlow(ctx, r, filepath.Dir(t.settingsPath)); err != nil {
		return err
	}
	if c.stack, err = ColocatedStack(c.s, image); err != nil {
		return err
	}
	if err := c.planKeep(ctx); err != nil {
		return err
	}
	scr.Step(4)
	save := func() error {
		if err := SaveSettings(t.settingsPath, c.s); err != nil {
			return err
		}
		c.clearPending() // the settings name the App now
		return nil
	}
	if err := c.bringUp(ctx, save); err != nil {
		return err
	}
	c.restartAfterReboot(ctx)
	scr.Step(5)
	return c.finish(ctx, scr, t)
}

// openRuntime chooses the runtime and the connection and identifies the engine behind it. Docker needs a confirmation
// when the command installs.
func openRuntime(ctx context.Context, r *Resolver, env Env, saved Settings, confirm bool) (Runtime, string, error) {
	name := r.preset("runtime", saved.Runtime) // only an install asks which runtime to use
	var err error
	if confirm {
		if name, err = chooseRuntime(r, saved.Runtime); err != nil {
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
	r.screen.Check("Using %s.\n", engineLine(rt.Engine()))
	return rt, connection, nil
}

// runtimeChoices are the answers of the runtime question.
var runtimeChoices = []ui.Choice{
	{Name: RuntimePodman, Desc: "recommended: rootless"},
	{Name: RuntimeDocker, Desc: "discouraged: root daemon, secrets in volumes"},
}

// chooseRuntime asks which runtime to use: a page and a choice on a full screen, the typed question it always was on a plain one.
func chooseRuntime(r *Resolver, def string) (string, error) {
	r.explain([]string{"runtime"}, "Container runtime",
		ui.Para("SuiteWard runs its database and its service in containers. Podman is recommended: it runs them rootless, with the secrets in its own store. "+
			"Docker works too, but its daemon runs as root and keeps the secrets in volumes that anyone who controls the daemon can read, so choosing it asks for a typed yes."))
	return r.selectAs("runtime", "Container runtime", "Container runtime (podman recommended, docker discouraged)", runtimeChoices, def)
}

// engineLine describes the engine a command works with: "Podman 5.8.2 (rootless)".
func engineLine(e Engine) string {
	daemon := "a rootful daemon"
	if e.Rootless {
		daemon = "rootless"
	}
	return fmt.Sprintf("%s (%s)", strings.TrimSpace(e.Name+" "+e.Version), daemon)
}

// chooseSettings resolves the ports and the job timeout over base, and validates the result. The GitHub values stay as
// base has them (the settings, or the adopted stack): the GitHub flow changes them later.
func chooseSettings(r *Resolver, base Settings, runtimeName, connection string) (Settings, error) {
	s := base
	s.Tier, s.Runtime, s.Connection = config.TierCoLocated, runtimeName, connection
	r.explain([]string{"http-port", "mcp-port"}, "Ports",
		ui.Para("SuiteWard listens on two ports, both only on 127.0.0.1: nothing outside this PC can reach them."),
		ui.Fields(
			ui.KV{Key: "HTTP", Value: fmt.Sprintf("Offered: %d. Serves /status and the local change report page.", base.HTTPPort)},
			ui.KV{Key: "MCP", Value: fmt.Sprintf("Offered: %d. Your coding agent's client connects to it with the token.", base.MCPPort)}),
		ui.Para("Change a port only if something else on this PC uses it: the installer refuses a port that is busy."))
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

// askImage asks for the release image. The installed one is kept by a re-run; a fresh install takes the release this binary carries (a
// development build carries none, and then --image is required).
func askImage(r *Resolver, previous stackInstalled) (string, error) {
	defaultImage, keep := previous.Image, "Enter keeps the image that is installed."
	if defaultImage == "" {
		if embedded, ok := ReleaseImage(); ok {
			defaultImage, keep = embedded, "Enter keeps the release this binary carries."
		} else {
			keep = "This build carries no release image, so give one: " + releaseImageRepo + "@sha256:<64 hex>."
		}
	}
	r.explain([]string{"image"}, "Release image",
		ui.Para("SuiteWard runs from one container image, pinned by its digest so that exactly the release you chose runs. "+keep))
	asked, err := r.Value("image", "Release image (pinned by digest)", defaultImage)
	if err != nil {
		return "", err
	}
	return parseImage(asked)
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
	return pullMissing(ctx, screenOn(c.out), c.rt, c.image, postgres)
}

// pullMissing pulls the images the engine does not have yet, each under a spinner on a full screen.
func pullMissing(ctx context.Context, scr *ui.Screen, rt Runtime, images ...string) error {
	for _, img := range images {
		if rt.ImageExists(ctx, img) {
			continue
		}
		if err := working(scr, "Pulling "+imageLabel(img), func() error { return rt.Pull(ctx, img) }); err != nil {
			return err
		}
	}
	return nil
}

// imageLabel is an image as the spinner names it: its name without the registry, and the start of its digest.
func imageLabel(image string) string {
	name, digest, pinned := strings.Cut(image, "@")
	name = name[strings.LastIndex(name, "/")+1:]
	if !pinned {
		return name
	}
	return name + "@" + digest[:min(len(digest), len("sha256:")+12)]
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
	base.BackupRecipient = saved.BackupRecipient // the containers know nothing of it
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

	// A re-run leaves these containers alone: they run as the install that made them defined them (planKeep). An upgrade keeps
	// neither.
	keepDB, keepServe bool

	machineUser string // the rootless user of the Podman machine, once it has been looked up

	copyRows summaryRows // what the offer of the copy of the program says in the summary (installSelf, upgradeSelf)
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
			screenOn(c.out).Warn("Note: the ports could not be checked inside the Podman machine (%v); a port that is in use shows up when the service starts.\n", err)
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
	return listeningPorts(out), nil
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
		screenOn(c.out).Warn("Upgrading an existing stack: %s held the owner URL. Creating the role %s and replacing the secret with its URL; the owner URL is now in %s and only the migrate container gets it.\n",
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
			screenOn(c.out).Warn("The copies of %s differ (an earlier install was interrupted): the role gets a new password and every copy is rewritten.\n", SecretDatabaseURL)
			state.needAppURL = true
		}
	}
	return state, nil
}

// bringUp creates what is missing and starts the stack in the order of the quadlets: database, role, migrate, service.
// save runs once migrate has succeeded and before the service starts: a service that runs without settings is a container
// that a later run cannot tell from a stranger's.
func (c colocatedInstall) bringUp(ctx context.Context, save func() error) error {
	if err := c.ensureObjects(ctx); err != nil {
		return err
	}

	// The old service, and a migrate container an interrupted run left, go first: the database may restart below and the
	// application secret may be replaced. A service that a re-run keeps stays.
	remove := []string{ContainerMigrate}
	if !c.keepServe {
		remove = []string{ContainerServe, ContainerMigrate}
	}
	scr := screenOn(c.out)
	for _, name := range remove {
		if err := working(scr, "Stopping "+name, func() error { return c.rt.Remove(ctx, name) }); err != nil { // the service gets up to 45 s to stop
			return err
		}
	}
	if !c.keepDB {
		if err := c.rt.Run(ctx, c.stack.DB); err != nil {
			return err
		}
	}
	c.say("Waiting for %s to become healthy (up to 90 s)...\n", ContainerDB)
	if err := c.waitHealthy(ctx); err != nil {
		return err
	}
	if err := c.ensureRole(ctx, c.secrets.needAppURL); err != nil {
		return err
	}
	if err := working(scr, "Running the migration", func() error { return c.rt.Run(ctx, c.stack.Migrate) }); err != nil {
		service := "so the service was not started"
		if c.keepServe {
			service = "the running service was left as it was"
		}
		return migratingError{fmt.Errorf("suiteward migrate failed, %s: %w", service, err)}
	}
	scr.Check("suiteward migrate finished: the schema is up to date.\n")
	if err := save(); err != nil {
		return migratingError{err}
	}
	if !c.keepServe {
		if err := c.rt.Run(ctx, c.stack.Serve); err != nil {
			return migratingError{err}
		}
	}
	c.reportStarted()
	return nil
}

// ensureObjects creates the network, the volumes and the secrets that are missing.
func (c colocatedInstall) ensureObjects(ctx context.Context) error {
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
	if c.githubKey == nil {
		return nil
	}
	return c.rt.Secrets().Write(ctx, SecretGitHubAppKey, c.githubKey, false)
}

// reportStarted says which containers were started and which a re-run left running.
func (c colocatedInstall) reportStarted() {
	scr := screenOn(c.out)
	var started []string
	for _, part := range []struct {
		name string
		kept bool
	}{{ContainerDB, c.keepDB}, {ContainerServe, c.keepServe}} {
		if part.kept {
			scr.Check("%s already runs as installed: it was not recreated.\n", part.name)
		} else {
			started = append(started, part.name)
		}
	}
	if len(started) == 0 {
		return
	}
	github := "not configured"
	if c.s.GitHub.AppID != 0 {
		github = "configured"
	}
	scr.Check("Started %s (%s), GitHub %s.\n", strings.Join(started, " and "), c.image, github)
}

// planKeep decides which containers a re-run leaves alone: the ones that run as the install that made them defined them.
// shortcut: Docker only. Its stack is known by the settings file of that install, so the settings and the image are what the
// containers were made from; a Podman re-run still replaces both containers (its stack is adopted from them instead), extend
// this when a Podman re-run should keep them too. Two effects are known: a plain re-run does not move the database to a newer
// PostgreSQL pin (`install --upgrade` does), and a change of the container definition that comes with a newer binary but the
// same image is not detected (run `uninstall`, then `install`). An upgrade never calls it: it replaces on purpose, after a backup.
func (c *colocatedInstall) planKeep(ctx context.Context) error {
	s := c.secrets
	// A settings file from before the image was saved cannot tell what the service was made from.
	if c.rt.Name() != RuntimeDocker || !c.previous.Known || c.previous.Image == "" || !s.haveDB || !s.haveOwner {
		return nil
	}
	exists, err := c.rt.Exists(ctx, KindContainer, ContainerDB)
	if err != nil || !exists {
		return err
	}
	db, err := c.rt.State(ctx, ContainerDB)
	if err != nil {
		return err
	}
	if c.keepDB = db.Status == "running" && db.Health == "healthy"; !c.keepDB {
		return nil
	}
	was, err := ColocatedStack(c.previous.Settings, c.previous.Image)
	if err != nil {
		return err
	}
	// The service reads its secrets when it starts, so one that this run makes or rewrites needs a new service.
	if !reflect.DeepEqual(was.Serve, c.stack.Serve) || !s.haveToken || s.needAppURL || c.githubKey != nil {
		return nil
	}
	// ensureRole rewrites the application URL when the role is missing.
	if role, err := appRoleExists(ctx, c.rt); err != nil || !role {
		return err
	}
	serve, err := c.rt.State(ctx, ContainerServe)
	if err != nil {
		return err
	}
	c.keepServe = serve.Status == "running"
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
			screenOn(c.out).Warn("Warning: the secret %s exists but %s does not. Remove the stack and its secrets to generate a new token.\n", SecretMCPAuth, c.tokenFile)
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
	bar := barOn(screenOn(c.out), "Database")
	for i := range 90 {
		if err := ctx.Err(); err != nil {
			return err
		}
		state, err := c.rt.State(ctx, ContainerDB)
		if err != nil {
			return err
		}
		switch {
		case state.Status == "running" && state.Health == "healthy":
			bar.set(1, "healthy")
			return nil
		case slices.Contains([]string{"exited", "stopped", "dead"}, state.Status):
			return fmt.Errorf("%s exited before it became healthy (%s)", ContainerDB, logs)
		}
		bar.set(float64(i)/90, fmt.Sprintf("starting, %d s", i))
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

const (
	restartUnit      = "podman-restart.service"
	restartEnableFix = "enable " + restartUnit + " as the rootless user: systemctl --user enable " + restartUnit
	// restartReloadCommand makes the user's systemd see the link, then prints whether the unit is enabled.
	restartReloadCommand = "systemctl --user daemon-reload && systemctl --user is-enabled " + restartUnit
)

// plainUser is a machine user name that can go into a path and into a shell command that runs as the machine's default ssh user.
var plainUser = regexp.MustCompile(`^[a-z_][a-z0-9_-]*$`)

// restartLinkCommand links podman-restart.service for user in a Podman machine. It runs as the machine's default ssh user, which
// is root on a Windows WSL machine: Podman's WSL machine creates the user's systemd directories owned by root, so the user's own
// `systemctl --user enable` is denied there.
func restartLinkCommand(user string) string {
	wants := "/home/" + user + "/.config/systemd/user/default.target.wants"
	return "mkdir -p " + wants + " && ln -sf /usr/lib/systemd/user/" + restartUnit + " " + wants + "/" + restartUnit
}

// restartFix is what to run by hand to enable podman-restart.service in a Podman machine: the link as the machine's default ssh
// user, then the reload and check as the user. A user name that is not plain gets the generic fix, never a command built from it.
func restartFix(machine, user string) string {
	if !plainUser.MatchString(user) {
		return restartEnableFix
	}
	return fmt.Sprintf("in the machine, link %s as the machine's default ssh user (root on a Windows WSL machine): podman machine ssh %s %s; then reload and check as the user (it prints enabled): podman machine ssh --username %s %s %s",
		restartUnit, machine, quoteArg(restartLinkCommand(user)), user, machine, quoteArg(restartReloadCommand))
}

// restartAfterReboot enables podman-restart.service of the rootless user, which is what starts a --restart always
// container after the machine restarts. A failure is reported, not fatal.
func (c *colocatedInstall) restartAfterReboot(ctx context.Context) {
	if _, ok := c.rt.(*Podman); !ok {
		return
	}
	d := c.env.Deps
	var err error
	fix := restartEnableFix
	if c.s.Connection == "" && d.GOOS == "linux" {
		_, err = d.Exec(ctx, "systemctl", []string{"--user", "enable", restartUnit}, nil)
	} else {
		var user string
		if user, err = c.lookUpMachineUser(ctx); err == nil {
			err = c.enableInMachine(ctx, user)
			fix = restartFix(c.s.Connection, user)
		}
	}
	if err != nil {
		screenOn(c.out).Warn("Note: %s could not be enabled (%v). Without it, containers do not restart after the machine restarts: %s.\n", restartUnit, err, fix)
	}
	if d.GOOS != "linux" {
		screenOn(c.out).Warn("Note: the Podman machine is not started at logon. After this PC restarts, run `podman machine start`; the stack comes back with it.\n")
	}
}

// enableInMachine enables podman-restart.service for user in the Podman machine: nothing when it is enabled already, the user's
// own enable, and when that is denied the link as the default ssh user followed by a reload and a check that it took.
func (c *colocatedInstall) enableInMachine(ctx context.Context, user string) error {
	d, machine := c.env.Deps, c.s.Connection
	asUser := func(command string) ([]byte, error) {
		return d.Exec(ctx, "podman", []string{"machine", "ssh", "--username", user, machine, command}, nil)
	}
	// is-enabled exits 1 for a unit that is not enabled: only a clean "enabled" is an answer, the rest goes on to the enable.
	if out, err := asUser("systemctl --user is-enabled " + restartUnit); err == nil && strings.TrimSpace(string(out)) == "enabled" {
		return nil
	}
	_, err := asUser("systemctl --user enable " + restartUnit)
	if err == nil {
		return nil
	}
	if !plainUser.MatchString(user) {
		return fmt.Errorf("%w; the machine user %q is not a plain name, so the link is not made for it", err, user)
	}
	if _, linkErr := d.Exec(ctx, "podman", []string{"machine", "ssh", machine, restartLinkCommand(user)}, nil); linkErr != nil {
		return fmt.Errorf("%w; the link as the default ssh user failed too: %v", err, linkErr)
	}
	if out, checkErr := asUser(restartReloadCommand); checkErr != nil || strings.TrimSpace(string(out)) != "enabled" {
		return fmt.Errorf("%w; the link as the default ssh user was made, but the unit is still not enabled after a reload", err)
	}
	return nil
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

// finish runs setup, or says how, prints the next steps and, on a full screen, ends with the summary.
func (c *colocatedInstall) finish(ctx context.Context, scr *ui.Screen, t installTarget) error {
	c.say("The MCP token for a client is in %s (it is not printed).\n", c.tokenFile)
	if c.s.GitHub.AppID == 0 {
		c.say("Read the status of the service: %s\n", c.statusHint(ctx)) // nothing has waited for the service: it may not answer from this PC yet
		c.say("GitHub is not configured. Run the install again with --github yes (or the GitHub values) to set it up, then enroll the owner.\n")
	} else {
		if err := c.enroll(ctx, c.readStatus); err != nil {
			return err
		}
		c.say("Read the status of the service: %s\n", c.statusHint(ctx)) // after the enrollment, which waited for the service to answer
		c.say("Then check the instance with `%s`.\n", doctorHint(c.env.Deps.command(), t.settingsPath, t.defaultPath))
	}
	scr.Step(6)
	c.installSelf(ctx)
	return showSummary(scr, func() []ui.KV { return c.installSummary(ctx, t) })
}

// statusHint is what reads /status of the service: the address when this PC reaches it, otherwise the command that reads it inside the
// Podman machine that publishes the port, which does not always answer from the PC (WSL's port forwarding).
func (c *colocatedInstall) statusHint(ctx context.Context) string {
	curl := fmt.Sprintf("curl -s http://127.0.0.1:%d/status", c.s.HTTPPort)
	if !c.inMachine() {
		return curl
	}
	if c.answersHere(ctx) {
		return statusURL(c.s.HTTPPort)
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
