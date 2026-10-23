package installer

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
		return Engine{}, fmt.Errorf("the Docker engine does not answer: %w", err)
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
		return Engine{}, fmt.Errorf("the Docker engine does not answer: %s reported no server", dk.bin)
	}
	// Only Docker names its engine "Engine": Podman's compatible API says "Podman Engine" and its own API names no components.
	isDocker := slices.ContainsFunc(server.Components, func(c engineComponent) bool { return c.Name == "Engine" })
	if !isDocker {
		what := "an engine that does not identify itself as Docker"
		if strings.Contains(strings.ToLower(server.Platform.Name+componentNames(server.Components)), "podman") || (server.Platform.Name == "" && len(server.Components) == 0) {
			what = "Podman"
		}
		return Engine{}, fmt.Errorf("%w: the engine behind %s is %s %s (%s); select Docker's own context or CLI, or install the stack with --runtime podman",
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

// ExportVolume reads the volume with a throwaway container, because Docker has no `volume export`. The container is the
// database's own image (it is here, and it has tar), with no network, a read-only root and the volume mounted read-only, as the
// service user that owns its files; the tarball comes back on its standard output.
func (dk *Docker) ExportVolume(ctx context.Context, volume, path string) error {
	raw, err := dk.Inspect(ctx, ContainerDB)
	if err != nil {
		return err
	}
	in, err := parseInspect(ContainerDB, raw)
	if err != nil {
		return err
	}
	if in.Config.Image == "" {
		return fmt.Errorf("the container %s reports no image", ContainerDB)
	}
	// shortcut: the tarball is held in memory until it is written, so the artifact volume must fit in the installer's memory, stream it to the file when volumes outgrow that.
	tarball, err := dk.run(ctx, nil, "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "all",
		"--security-opt", "no-new-privileges", "--user", serviceUser, "--volume", volume+":/data:ro",
		in.Config.Image, "tar", "-cf", "-", "-C", "/data", ".")
	if err != nil {
		return fmt.Errorf("export the volume %s: %w", volume, err)
	}
	return writePrivateFile(path, tarball)
}

func componentNames(components []engineComponent) string {
	names := make([]string, len(components))
	for i, c := range components {
		names[i] = c.Name
	}
	return " " + strings.Join(names, " ")
}

type engineComponent struct{ Name string }

// ImportVolume writes the tarball into the volume.
func (dk *Docker) ImportVolume(_ context.Context, _, _ string) error {
	return errors.New("import is not implemented")
}
