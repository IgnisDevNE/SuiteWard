package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Docker drives Docker, never trusting the command name: on a PC that also runs Podman, `docker` may be Podman's shim and
// the default endpoint may be served by the Podman machine (spike S2). The CLI is addressed by path and the engine by an
// explicit context, and the engine is identified before anything is created.
type Docker struct {
	cli
	secrets dockerSecrets
	engine  Engine
}

// DockerOptions choose the Docker CLI and context; zero values pick Docker Desktop's on Windows and macOS.
type DockerOptions struct{ Binary, Context string }

const dockerDesktopRelativeCLI = `\Docker\Docker\resources\bin\docker.exe`

// NewDocker identifies the engine behind the CLI and refuses Podman's docker shim.
func NewDocker(ctx context.Context, d Deps, o DockerOptions) (Runtime, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	bin, dockerContext := o.Binary, o.Context
	desktop := d.GOOS == "windows" || d.GOOS == "darwin"
	if bin == "" {
		bin = "docker"
		if d.GOOS == "windows" {
			programFiles := d.Getenv("ProgramFiles")
			if programFiles == "" {
				programFiles = `C:\Program Files`
			}
			bin = programFiles + dockerDesktopRelativeCLI
		}
	}
	if dockerContext == "" && desktop {
		dockerContext = "desktop-linux"
	}
	dk := &Docker{cli: cli{bin: bin, d: d}}
	dk.secrets = dockerSecrets{dk}
	if dockerContext != "" {
		dk.base = []string{"--context", dockerContext}
	}
	engine, err := dk.identify(ctx)
	if err != nil {
		return nil, err
	}
	dk.engine = engine
	return dk, nil
}

// identify reads the server behind the selected endpoint and refuses Podman.
func (dk *Docker) identify(ctx context.Context) (Engine, error) {
	out, err := dk.run(ctx, nil, "version", "--format", "{{json .Server}}")
	if err != nil {
		return Engine{}, fmt.Errorf("the container program (Docker) does not answer (is it started?): %w", err)
	}
	var server struct {
		Platform   struct{ Name string }
		Version    string
		Components []engineComponent
	}
	if err := json.Unmarshal(out, &server); err != nil {
		return Engine{}, fmt.Errorf("read the Docker server version: %w", err)
	}
	if server.Platform.Name == "" && server.Version == "" {
		return Engine{}, fmt.Errorf("the container program (Docker) does not answer (is it started?): %s reported no server", dk.bin)
	}
	// Only Docker names its engine "Engine": Podman's compatible API says "Podman Engine" and its own API names no components.
	isDocker := slices.ContainsFunc(server.Components, func(c engineComponent) bool { return c.Name == "Engine" })
	if !isDocker {
		what := "a container program that does not identify itself as Docker"
		if strings.Contains(strings.ToLower(server.Platform.Name+componentNames(server.Components)), "podman") || (server.Platform.Name == "" && len(server.Components) == 0) {
			what = "Podman"
		}
		return Engine{}, fmt.Errorf("%w: the container program behind %s is %s %s (%s); select Docker's own context or CLI, or install with --runtime podman",
			ErrNotDocker, dk.bin, what, server.Version, server.Platform.Name)
	}
	out, err = dk.run(ctx, nil, "info", "--format", "{{json .SecurityOptions}}")
	if err != nil {
		return Engine{}, fmt.Errorf("read the Docker security options: %w", err)
	}
	var security []string
	if err := json.Unmarshal(out, &security); err != nil {
		return Engine{}, fmt.Errorf("read the Docker security options: %w", err)
	}
	return Engine{Name: server.Platform.Name, Version: server.Version, Rootless: slices.Contains(security, "name=rootless")}, nil
}

func (dk *Docker) Name() string { return RuntimeDocker }

func (dk *Docker) Engine() Engine { return dk.engine }

func (dk *Docker) Secrets() SecretStore { return dk.secrets }

// Remove stops the container with the stack's 45 s grace period and removes it; Docker's rm --force would kill it at once.
func (dk *Docker) Remove(ctx context.Context, container string) error {
	if ok, err := dk.Exists(ctx, KindContainer, container); err != nil || !ok {
		return err
	}
	if _, err := dk.run(ctx, nil, "stop", "--time", "45", container); err != nil {
		return fmt.Errorf("stop %s: %w", container, err)
	}
	if _, err := dk.run(ctx, nil, "rm", "--force", container); err != nil {
		return fmt.Errorf("remove %s: %w", container, err)
	}
	return nil
}

// Run removes a container of the same name first (Docker has no --replace) and mounts the container's own secret volume.
func (dk *Docker) Run(ctx context.Context, spec RunSpec) error {
	var secretArgs []string
	for _, name := range spec.Secrets {
		if _, _, err := secretFor(name, spec.Name); err != nil {
			return err
		}
	}
	if len(spec.Secrets) > 0 {
		secretArgs = []string{"--volume", secretVolume(spec.Name) + ":/run/secrets:ro"}
	}
	args, err := runArgs(spec, false, secretArgs)
	if err != nil {
		return err
	}
	if err := dk.Remove(ctx, spec.Name); err != nil {
		return err
	}
	if out, err := dk.run(ctx, nil, args...); err != nil {
		return runError(spec, out, err)
	}
	return nil
}

// serviceUser owns the files of the artifact volume: the user the service image runs as.
const serviceUser = "65532:65532"

// helperImage is the image of the throwaway containers that read and write a volume: the database's own (it is here, and it has tar).
func (dk *Docker) helperImage(ctx context.Context) (string, error) {
	raw, err := dk.Inspect(ctx, ContainerDB)
	if err != nil {
		return "", err
	}
	in, err := parseInspect(ContainerDB, raw)
	if err != nil {
		return "", err
	}
	if in.Config.Image == "" {
		return "", fmt.Errorf("the container %s reports no image", ContainerDB)
	}
	return in.Config.Image, nil
}

// confined are the flags of a throwaway container that touches a volume: no network, a read-only root, no capabilities, the service
// user that owns the files.
var confined = []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "all", "--security-opt", "no-new-privileges", "--user", serviceUser}

// ExportVolume reads the volume with a throwaway container, because Docker has no `volume export`. The container is confined and
// mounts the volume read-only; the tarball comes back on its standard output.
func (dk *Docker) ExportVolume(ctx context.Context, volume, path string) error {
	image, err := dk.helperImage(ctx)
	if err != nil {
		return err
	}
	// shortcut: the tarball is held in memory until it is written, so the artifact volume must fit in the installer's memory, stream it to the file when volumes outgrow that.
	tarball, err := dk.run(ctx, nil, append(slices.Clone(confined), "--volume", volume+":/data:ro", image, "tar", "-cf", "-", "-C", "/data", ".")...)
	if err != nil {
		return fmt.Errorf("export the volume %s: %w", volume, err)
	}
	return writePrivateFile(path, tarball)
}

// importFlags are the flags of the throwaway container that writes a volume. A volume that Docker has just created is owned by root,
// and the helper image has nothing at /data to copy an ownership from, so the service user could not write it. The container runs as
// root, with no network, a read-only root and no new privileges, and with only the three capabilities that let tar do what a root tar does:
// restore the owner and the group that the tarball records (the service user's), change the mode and the times of files that user
// owns, and enter the directories it made.
var importFlags = []string{
	"run", "--rm", "--interactive", "--network", "none", "--read-only", "--cap-drop", "all",
	"--cap-add", "CHOWN", "--cap-add", "FOWNER", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges", "--user", "0",
}

// ImportVolume writes the volume with a throwaway container, because Docker has no `volume import`: the container of importFlags with
// the volume mounted writable and the tarball on its standard input, which is the file, streamed. The owners are numeric, so that a name
// in the tarball cannot map to another user of the helper image.
func (dk *Docker) ImportVolume(ctx context.Context, volume, path string) error {
	image, err := dk.helperImage(ctx)
	if err != nil {
		return err
	}
	f, err := os.Open(filepath.Clean(path))
	if err != nil {
		return fmt.Errorf("read the tarball %s: %w", path, err)
	}
	defer func() { _ = f.Close() }()
	args := append(slices.Clone(importFlags), "--volume", volume+":/data", image, "tar", "--numeric-owner", "-xf", "-", "-C", "/data")
	if _, err := dk.run(ctx, f, args...); err != nil {
		return fmt.Errorf("import the volume %s: %w", volume, err)
	}
	return nil
}

func componentNames(components []engineComponent) string {
	names := make([]string, len(components))
	for i, c := range components {
		names[i] = c.Name
	}
	return " " + strings.Join(names, " ")
}

type engineComponent struct{ Name string }
