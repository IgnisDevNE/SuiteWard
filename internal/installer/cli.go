package installer

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const kindSecret Kind = "secret" // Podman's own secret store; Docker has no such object

var digestImage = regexp.MustCompile(`@sha256:[0-9a-f]{64}$`)

// cli is what Podman and Docker share: both are driven through their command line with the same verbs.
type cli struct {
	bin  string
	base []string // global flags that select the engine, before the subcommand
	d    Deps
}

func (c *cli) args(a ...string) []string { return append(slices.Clone(c.base), a...) }

func (c *cli) run(ctx context.Context, stdin io.Reader, a ...string) ([]byte, error) {
	return c.d.Exec(ctx, c.bin, c.args(a...), stdin)
}

// Exists lists objects whose name contains name and looks for the exact one: the name filter is a substring match.
func (c *cli) Exists(ctx context.Context, kind Kind, name string) (bool, error) {
	var list []string
	switch kind {
	case KindContainer:
		list = []string{"container", "ls", "--all", "--filter", "name=" + name, "--format", "{{.Names}}"}
	case KindVolume, KindNetwork, kindSecret:
		list = []string{string(kind), "ls", "--filter", "name=" + name, "--format", "{{.Name}}"}
	default:
		return false, fmt.Errorf("cannot look for a %s", kind)
	}
	out, err := c.run(ctx, nil, list...)
	if err != nil {
		return false, fmt.Errorf("look for the %s %s: %w", kind, name, err)
	}
	for _, line := range strings.Split(string(out), "\n") {
		if slices.Contains(strings.Split(strings.TrimSpace(line), ","), name) {
			return true, nil
		}
	}
	return false, nil
}

func (c *cli) Create(ctx context.Context, kind Kind, name string) error {
	if kind != KindVolume && kind != KindNetwork {
		return fmt.Errorf("cannot create a %s with Create", kind)
	}
	if _, err := c.run(ctx, nil, string(kind), "create", name); err != nil {
		return fmt.Errorf("create the %s %s: %w", kind, name, err)
	}
	return nil
}

// Delete removes a volume or a network; one that does not exist is not an error.
func (c *cli) Delete(ctx context.Context, kind Kind, name string) error {
	if kind != KindVolume && kind != KindNetwork {
		return fmt.Errorf("cannot delete a %s with Delete", kind)
	}
	have, err := c.Exists(ctx, kind, name)
	if err != nil || !have {
		return err
	}
	if _, err := c.run(ctx, nil, string(kind), "rm", name); err != nil {
		return fmt.Errorf("remove the %s %s: %w", kind, name, err)
	}
	return nil
}

// ImageExists says whether the engine already has the image. A failing inspect is "not here": the pull that follows
// reports a real problem with the engine or the registry.
func (c *cli) ImageExists(ctx context.Context, image string) bool {
	_, err := c.run(ctx, nil, "image", "inspect", image)
	return err == nil
}

// runError is the error of a failed run. A foreground container (migrate) logs why it failed on stdout, so the end of
// it is kept; a detached container prints only its ID there.
func runError(spec RunSpec, out []byte, err error) error {
	if !spec.Detach {
		if tail := stdoutTail(out); tail != "" {
			return fmt.Errorf("run %s: %w; its output ends: %s", spec.Name, err, tail)
		}
	}
	return fmt.Errorf("run %s: %w", spec.Name, err)
}

func (c *cli) Pull(ctx context.Context, image string) error {
	if !digestImage.MatchString(image) {
		return fmt.Errorf("%w: %s", ErrNotDigestImage, image)
	}
	if _, err := c.run(ctx, nil, "pull", image); err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	return nil
}

func (c *cli) Exec(ctx context.Context, container string, argv []string, stdin io.Reader) ([]byte, error) {
	return c.run(ctx, stdin, append([]string{"exec", "-i", container}, argv...)...)
}

func (c *cli) ExecTTY(ctx context.Context, container string, argv []string) error {
	name, args := c.ExecTTYCommand(container, argv)
	return c.d.Interactive(ctx, name, args)
}

func (c *cli) ExecTTYCommand(container string, argv []string) (string, []string) {
	return c.bin, c.args(append([]string{"exec", "-it", container}, argv...)...)
}

// runArgs is the argument list of `run`; secretArgs are the runtime's own way of mounting the container's secrets.
func runArgs(spec RunSpec, replace bool, secretArgs []string) ([]string, error) {
	if spec.Name == "" || spec.Image == "" || spec.Network == "" {
		return nil, fmt.Errorf("a container needs a name, an image and a network: %+v", spec)
	}
	a := []string{"run"}
	if spec.Detach {
		a = append(a, "--detach")
	} else {
		a = append(a, "--rm")
	}
	if replace {
		a = append(a, "--replace")
	}
	a = append(a, "--name", spec.Name, "--network", spec.Network)
	if spec.Restart != "" {
		a = append(a, "--restart", spec.Restart)
	}
	a = append(a, "--read-only", "--cap-drop", "all", "--security-opt", "no-new-privileges")
	if spec.User != "" {
		a = append(a, "--user", spec.User)
	}
	for _, t := range spec.Tmpfs {
		a = append(a, "--tmpfs", t)
	}
	if spec.Memory != "" {
		a = append(a, "--memory", spec.Memory)
	}
	if spec.StopTimeout > 0 {
		a = append(a, "--stop-timeout", strconv.Itoa(spec.StopTimeout))
	}
	for _, e := range spec.Env {
		a = append(a, "--env", e)
	}
	a = append(a, secretArgs...)
	for _, v := range spec.Volumes {
		a = append(a, "--volume", v)
	}
	if h := spec.Health; h != nil {
		a = append(a, "--health-cmd", h.Cmd, "--health-interval", h.Interval, "--health-timeout", h.Timeout, "--health-retries", strconv.Itoa(h.Retries))
	}
	for _, p := range spec.Publish {
		a = append(a, "--publish", p)
	}
	return append(append(a, spec.Image), spec.Args...), nil
}
func (c *cli) State(ctx context.Context, container string) (ContainerState, error) {
	// {{json .State}} rather than a template over .State.Health: a container without a health check has no Health.
	out, err := c.run(ctx, nil, "inspect", "--type", "container", "--format", "{{json .State}}", container)
	if err != nil {
		return ContainerState{}, fmt.Errorf("inspect %s: %w", container, err)
	}
	var state struct {
		Status string
		Health *struct{ Status string }
	}
	if err := json.Unmarshal(out, &state); err != nil {
		return ContainerState{}, fmt.Errorf("read the state of %s: %w", container, err)
	}
	result := ContainerState{Status: state.Status}
	if state.Health != nil {
		result.Health = state.Health.Status
	}
	return result, nil
}

// Inspect is `inspect --type container`: the service container and the network are both called suiteward, and without the
// type the answer may be the network's.
func (c *cli) Inspect(ctx context.Context, container string) ([]byte, error) {
	out, err := c.run(ctx, nil, "inspect", "--type", "container", container)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", container, err)
	}
	return out, nil
}

// Start starts a container that exists, as it was made: it is not created again.
func (c *cli) Start(ctx context.Context, container string) error {
	if _, err := c.run(ctx, nil, "start", container); err != nil {
		return fmt.Errorf("start %s: %w", container, err)
	}
	return nil
}
