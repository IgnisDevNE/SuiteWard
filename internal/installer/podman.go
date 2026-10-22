package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// A machine connection that reaches the rootless socket of the machine's user.
var rootlessSocket = regexp.MustCompile(`/run/user/[0-9]+/podman/podman\.sock$`)

// Podman drives rootless Podman.
type Podman struct {
	cli
	secrets podmanSecrets
}

// NewPodman selects the rootless connection and refuses a rootful one. The connection is the explicit one, else
// CONTAINER_CONNECTION, else (off Linux, where Podman runs in a machine) the single machine connection that ends in
// /run/user/<n>/podman/podman.sock. On Linux without a connection it is the local rootless Podman.
func NewPodman(ctx context.Context, d Deps, connection string) (Runtime, error) {
	if err := d.validate(); err != nil {
		return nil, err
	}
	p := &Podman{cli: cli{bin: "podman", d: d}}
	p.secrets = podmanSecrets{p}
	if connection == "" {
		connection = d.Getenv("CONTAINER_CONNECTION")
	}
	if connection == "" && d.GOOS != "linux" {
		var err error
		if connection, err = p.rootlessMachineConnection(ctx); err != nil {
			return nil, err
		}
	}
	if connection != "" {
		p.base = []string{"--connection", connection}
	}
	out, err := p.run(ctx, nil, "info", "--format", "{{.Host.Security.Rootless}}")
	if err != nil {
		return nil, fmt.Errorf("the Podman connection is unavailable: %w", err)
	}
	if strings.TrimSpace(string(out)) != "true" {
		return nil, fmt.Errorf("%w: the co-located and isolated stacks run rootless, so select the machine's rootless connection with --connection NAME", ErrRootful)
	}
	return p, nil
}

func (p *Podman) rootlessMachineConnection(ctx context.Context) (string, error) {
	out, err := p.run(ctx, nil, "system", "connection", "list", "--format", "json")
	if err != nil {
		return "", fmt.Errorf("list the Podman connections: %w", err)
	}
	var listed []struct {
		Name      string
		URI       string
		IsMachine bool
	}
	if err := json.Unmarshal(out, &listed); err != nil {
		return "", fmt.Errorf("read the Podman connections: %w", err)
	}
	var candidates []string
	for _, c := range listed {
		if c.IsMachine && rootlessSocket.MatchString(c.URI) {
			candidates = append(candidates, c.Name)
		}
	}
	if len(candidates) != 1 {
		return "", fmt.Errorf("%w: found %d, select one with --connection NAME (the installer does not change host defaults)", ErrNoConnection, len(candidates))
	}
	return candidates[0], nil
}

func (p *Podman) Name() string { return RuntimePodman }

func (p *Podman) Engine() Engine { return Engine{Name: "Podman", Rootless: true} }

func (p *Podman) Secrets() SecretStore { return p.secrets }

func (p *Podman) Remove(ctx context.Context, container string) error {
	if _, err := p.run(ctx, nil, "rm", "--force", "--ignore", "--time", "45", container); err != nil {
		return fmt.Errorf("remove %s: %w", container, err)
	}
	return nil
}

func (p *Podman) Run(ctx context.Context, spec RunSpec) error {
	var secretArgs []string
	for _, name := range spec.Secrets {
		s, uid, err := secretFor(name, spec.Name)
		if err != nil {
			return err
		}
		secretArgs = append(secretArgs, "--secret", fmt.Sprintf("%s,type=mount,target=/run/secrets/%s,uid=%d,mode=0400", s.name, s.file, uid))
	}
	args, err := runArgs(spec, true, secretArgs)
	if err != nil {
		return err
	}
	if out, err := p.run(ctx, nil, args...); err != nil {
		return runError(spec, out, err)
	}
	return nil
}
