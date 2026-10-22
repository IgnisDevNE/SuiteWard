package installer

import (
	"context"
	"encoding/json"
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
		Components []struct{ Name string }
	}
	if err := json.Unmarshal(out, &server); err != nil {
		return Engine{}, fmt.Errorf("read the Docker server version: %w", err)
	}
	if server.Platform.Name == "" && server.Version == "" {
		return Engine{}, fmt.Errorf("the Docker engine does not answer: %s reported no server", dk.bin)
	}
	podman := strings.Contains(strings.ToLower(server.Platform.Name), "podman")
	for _, c := range server.Components {
		podman = podman || strings.Contains(strings.ToLower(c.Name), "podman")
	}
	if podman {
		return Engine{}, fmt.Errorf("%w: the engine behind %s is Podman %s (%s); select Docker's own context or CLI, or install the stack with --runtime podman",
			ErrNotDocker, dk.bin, server.Version, server.Platform.Name)
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
	if _, err := dk.run(ctx, nil, args...); err != nil {
		return fmt.Errorf("run %s: %w", spec.Name, err)
	}
	return nil
}
