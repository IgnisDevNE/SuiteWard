package installer

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const dockerDesktopCLI = `C:\Program Files\Docker\Docker\resources\bin\docker.exe`

// What `docker version --format "{{json .Server}}"` prints, as far as the engine check reads it.
const (
	dockerDesktopServer = `{"Platform":{"Name":"Docker Desktop 4.50.0 (209931)"},"Version":"29.9.2","Components":[{"Name":"Engine","Version":"29.9.2"}]}`
	podmanBehindDocker  = `{"Platform":{"Name":"linux/amd64/fedora-43"},"Version":"6.1.4","Components":[{"Name":"Podman Engine","Version":"6.1.4"}]}`
)

func newDocker(t *testing.T, f *fakeEngine, server, security string, o DockerOptions) Runtime {
	t.Helper()
	f.on("version --format", server+"\n").on("info --format", security+"\n")
	rt, err := NewDocker(context.Background(), f.deps(), o)
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func TestDockerOnWindowsUsesDockerDesktopsCLIAndContext(t *testing.T) {
	f := newFake("windows")
	rt := newDocker(t, f, dockerDesktopServer, `["name=seccomp,profile=builtin"]`, DockerOptions{})
	for _, c := range f.calls {
		if c.name != dockerDesktopCLI {
			t.Errorf("ran %q, not Docker Desktop's CLI by its path", c.name)
		}
		if c.args[0] != "--context" || c.args[1] != "desktop-linux" {
			t.Errorf("a call does not name the context: %v", c.args)
		}
	}
	e := rt.Engine()
	if rt.Name() != RuntimeDocker || e.Rootless || !strings.Contains(e.Name, "Docker Desktop") || e.Version != "29.9.2" {
		t.Fatalf("engine: %+v", e)
	}
}

func TestDockerUsesTheProgramFilesOfThisMachine(t *testing.T) {
	f := newFake("windows")
	f.env["ProgramFiles"] = `D:\Apps`
	newDocker(t, f, dockerDesktopServer, `[]`, DockerOptions{})
	if got := f.calls[0].name; got != `D:\Apps\Docker\Docker\resources\bin\docker.exe` {
		t.Fatalf("got %s", got)
	}
}

func TestDockerOptionsOverrideTheDefaults(t *testing.T) {
	f := newFake("windows")
	newDocker(t, f, dockerDesktopServer, `[]`, DockerOptions{Binary: "/opt/docker", Context: "other"})
	if f.calls[0].name != "/opt/docker" || f.calls[0].args[1] != "other" {
		t.Fatalf("got %+v", f.calls[0])
	}
}

func TestDockerOnLinuxUsesTheDefaultContext(t *testing.T) {
	f := newFake("linux")
	newDocker(t, f, dockerDesktopServer, `["name=rootless"]`, DockerOptions{})
	if f.calls[0].name != "docker" || f.calls[0].args[0] == "--context" {
		t.Fatalf("got %+v", f.calls[0])
	}
}

func TestDockerReportsARootlessEngine(t *testing.T) {
	f := newFake("linux")
	rt := newDocker(t, f, dockerDesktopServer, `["name=seccomp,profile=builtin","name=rootless"]`, DockerOptions{})
	if !rt.Engine().Rootless {
		t.Fatal("a rootless engine was reported as rootful")
	}
}

func TestDockerRefusesPodmanBehindTheDockerName(t *testing.T) {
	f := newFake("windows").on("version --format", podmanBehindDocker+"\n")
	_, err := NewDocker(context.Background(), f.deps(), DockerOptions{})
	if !errors.Is(err, ErrNotDocker) {
		t.Fatalf("got %v, want ErrNotDocker", err)
	}
	if !strings.Contains(err.Error(), "Podman") {
		t.Fatalf("the error does not say what the engine is: %v", err)
	}
	for _, line := range f.lines() {
		if !strings.Contains(line, "version") && !strings.Contains(line, "info") {
			t.Errorf("something other than identification ran: %s", line)
		}
	}
}

func TestDockerEngineThatDoesNotAnswerIsAnError(t *testing.T) {
	f := newFake("linux").fail("version --format", errors.New("cannot connect to the daemon"))
	_, err := NewDocker(context.Background(), f.deps(), DockerOptions{})
	if err == nil || errors.Is(err, ErrNotDocker) {
		t.Fatalf("got %v", err)
	}
	f = newFake("linux").on("version --format", "not json")
	if _, err := NewDocker(context.Background(), f.deps(), DockerOptions{}); err == nil {
		t.Fatal("unreadable version output was accepted")
	}
}

func TestDockerRunRemovesThenRunsWithoutReplace(t *testing.T) {
	f := newFake("linux")
	rt := newDocker(t, f, dockerDesktopServer, `[]`, DockerOptions{})
	f.on("container ls", "suiteward\n")
	if err := rt.Run(context.Background(), servingSpec()); err != nil {
		t.Fatal(err)
	}
	lines := f.lines()
	var runAt, stopAt, rmAt = -1, -1, -1
	for i, l := range lines {
		switch {
		case strings.HasPrefix(l, "run "):
			runAt = i
		case strings.HasPrefix(l, "stop "):
			stopAt = i
		case strings.HasPrefix(l, "rm "):
			rmAt = i
		}
	}
	if stopAt < 0 || stopAt >= rmAt || rmAt >= runAt {
		t.Fatalf("want stop, rm, then run: %v", lines)
	}
	if strings.Contains(lines[runAt], "--replace") {
		t.Fatalf("docker run has no --replace: %s", lines[runAt])
	}
	// Docker's secrets are the consumer's own volume mounted read-only; --secret exists only in Swarm.
	if strings.Contains(lines[runAt], "--secret") || !strings.Contains(lines[runAt], "--volume suiteward-secrets-suiteward:/run/secrets:ro ") {
		t.Fatalf("unexpected secret mount: %s", lines[runAt])
	}
	if !strings.Contains(lines[runAt], "--volume suiteward-data:/data") {
		t.Fatalf("the data volume is missing: %s", lines[runAt])
	}
}

func TestDockerRunWithoutSecretsMountsNoSecretVolume(t *testing.T) {
	f := newFake("linux")
	rt := newDocker(t, f, dockerDesktopServer, `[]`, DockerOptions{})
	spec := servingSpec()
	spec.Secrets = nil
	if err := rt.Run(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(f.calls[len(f.calls)-1].line(), "/run/secrets") {
		t.Fatal("a secret volume was mounted for a container without secrets")
	}
	if len(f.find("rm ")) != 0 {
		t.Fatal("rm ran although no container existed")
	}
}

func TestDockerOperationsUseTheContext(t *testing.T) {
	ctx := context.Background()
	f := newFake("windows")
	rt := newDocker(t, f, dockerDesktopServer, `[]`, DockerOptions{})
	before := len(f.calls)
	if err := rt.Create(ctx, KindNetwork, NetworkName); err != nil {
		t.Fatal(err)
	}
	if err := rt.Pull(ctx, testImage); err != nil {
		t.Fatal(err)
	}
	if err := rt.ExecTTY(ctx, ContainerServe, []string{"/suiteward", "setup"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range append(f.calls[before:], f.tty...) {
		if c.name != dockerDesktopCLI || c.args[0] != "--context" || c.args[1] != "desktop-linux" {
			t.Errorf("a call escaped the selected engine: %+v", c)
		}
	}
	if len(f.tty) != 1 || !strings.HasSuffix(f.tty[0].line(), "exec -it suiteward /suiteward setup") {
		t.Fatalf("tty calls: %+v", f.tty)
	}
}
