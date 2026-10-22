package installer

import (
	"context"
	"errors"
	"strings"
	"testing"
)

const machineConnections = `[
 {"Name":"podman-machine-default","URI":"ssh://user@127.0.0.1:1/run/user/1000/podman/podman.sock","IsMachine":true},
 {"Name":"podman-machine-default-root","URI":"ssh://root@127.0.0.1:1/run/podman/podman.sock","IsMachine":true},
 {"Name":"remote","URI":"ssh://someone@example.org/run/user/1000/podman/podman.sock","IsMachine":false}
]`

func newRootlessPodman(t *testing.T, f *fakeEngine, connection string) Runtime {
	t.Helper()
	f.on("info --format", "true\n")
	rt, err := NewPodman(context.Background(), f.deps(), connection)
	if err != nil {
		t.Fatal(err)
	}
	return rt
}

func TestPodmanSelectsTheSingleRootlessMachineConnection(t *testing.T) {
	f := newFake("windows").on("system connection list", machineConnections)
	rt := newRootlessPodman(t, f, "")
	if rt.Name() != RuntimePodman || !rt.Engine().Rootless {
		t.Fatalf("unexpected runtime %s %+v", rt.Name(), rt.Engine())
	}
	if _, err := rt.Exists(context.Background(), KindVolume, VolumeData); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.calls {
		if len(c.args) >= 3 && c.args[0] == "system" {
			continue // the listing itself has no connection yet
		}
		if len(c.args) < 2 || c.args[0] != "--connection" || c.args[1] != "podman-machine-default" {
			t.Errorf("a call does not use the rootless connection: %v", c.args)
		}
	}
}

func TestPodmanConnectionPrecedence(t *testing.T) {
	f := newFake("windows")
	f.env["CONTAINER_CONNECTION"] = "from-env"
	newRootlessPodman(t, f, "")
	if got := f.calls[0].args[:2]; got[0] != "--connection" || got[1] != "from-env" {
		t.Fatalf("the environment connection was not used: %v", f.calls[0].args)
	}
	f = newFake("windows")
	f.env["CONTAINER_CONNECTION"] = "from-env"
	newRootlessPodman(t, f, "explicit")
	if got := f.calls[0].args[:2]; got[1] != "explicit" {
		t.Fatalf("the explicit connection did not win: %v", f.calls[0].args)
	}
	if len(f.find("system connection list")) != 0 {
		t.Fatal("connections were listed although one was named")
	}
}

func TestPodmanNeedsExactlyOneRootlessMachineConnection(t *testing.T) {
	two := `[{"Name":"a","URI":"ssh://u@h:1/run/user/1000/podman/podman.sock","IsMachine":true},
	         {"Name":"b","URI":"ssh://u@h:2/run/user/1001/podman/podman.sock","IsMachine":true}]`
	for name, list := range map[string]string{"two": two, "none": `[{"Name":"r","URI":"ssh://root@h:1/run/podman/podman.sock","IsMachine":true}]`} {
		f := newFake("windows").on("system connection list", list).on("info --format", "true\n")
		if _, err := NewPodman(context.Background(), f.deps(), ""); !errors.Is(err, ErrNoConnection) {
			t.Errorf("%s: got %v, want ErrNoConnection", name, err)
		}
	}
}

func TestPodmanRefusesARootfulConnectionBeforeChangingAnything(t *testing.T) {
	f := newFake("windows").on("info --format", "false\n")
	_, err := NewPodman(context.Background(), f.deps(), "podman-machine-default-root")
	if !errors.Is(err, ErrRootful) {
		t.Fatalf("got %v, want ErrRootful", err)
	}
	for _, line := range f.lines() {
		for _, change := range []string{"run", "rm", "secret create", "volume create", "network create", "pull"} {
			if strings.Contains(line, " "+change) {
				t.Errorf("podman changed something (%q) before the refusal", line)
			}
		}
	}
}

func TestPodmanOnLinuxUsesTheLocalRootlessPodman(t *testing.T) {
	f := newFake("linux")
	newRootlessPodman(t, f, "")
	if len(f.find("system connection list")) != 0 {
		t.Fatal("a machine was looked for on Linux")
	}
	for _, c := range f.calls {
		if c.args[0] == "--connection" {
			t.Fatalf("a connection was selected on Linux without being asked: %v", c.args)
		}
	}
}

func TestPodmanRunArguments(t *testing.T) {
	f := newFake("linux")
	rt := newRootlessPodman(t, f, "")
	err := rt.Run(context.Background(), servingSpec())
	if err != nil {
		t.Fatal(err)
	}
	want := "run --detach --replace --name suiteward --network suiteward --restart always --read-only --cap-drop all " +
		"--security-opt no-new-privileges --user 65532:65532 --tmpfs /tmp --memory 256m --stop-timeout 45 --env A=b " +
		"--secret suiteward-database-url,type=mount,target=/run/secrets/database-url,uid=65532,mode=0400 " +
		"--secret suiteward-mcp-token,type=mount,target=/run/secrets/mcp-token,uid=65532,mode=0400 " +
		"--volume suiteward-data:/data --health-cmd pg_isready --health-interval 5s --health-timeout 3s --health-retries 12 " +
		"--publish 127.0.0.1:8081:8080 " + testImage + " serve"
	got := f.find("run --detach")
	if len(got) != 1 || got[0].line() != want {
		t.Fatalf("got  %v\nwant %s", got, want)
	}
}

func TestPodmanForegroundRunIsRemovedOnExit(t *testing.T) {
	f := newFake("linux")
	rt := newRootlessPodman(t, f, "")
	spec := servingSpec()
	spec.Detach, spec.Restart = false, ""
	if err := rt.Run(context.Background(), spec); err != nil {
		t.Fatal(err)
	}
	line := f.calls[len(f.calls)-1].line()
	if !strings.HasPrefix(line, "run --rm --replace --name suiteward ") || strings.Contains(line, "--detach") || strings.Contains(line, "--restart") {
		t.Fatalf("unexpected foreground run: %s", line)
	}
}

func TestPodmanRunRefusesAnUnknownSecret(t *testing.T) {
	f := newFake("linux")
	rt := newRootlessPodman(t, f, "")
	spec := servingSpec()
	spec.Secrets = []string{"not-in-the-catalog"}
	if err := rt.Run(context.Background(), spec); err == nil {
		t.Fatal("an uncatalogued secret was mounted")
	}
	if len(f.find("run ")) != 0 {
		t.Fatal("podman run was called")
	}
}

func TestPodmanCommonOperations(t *testing.T) {
	ctx := context.Background()
	f := newFake("linux")
	rt := newRootlessPodman(t, f, "")

	f.on("container ls", "suiteward-db\nsuiteward\n")
	if ok, err := rt.Exists(ctx, KindContainer, "suiteward"); err != nil || !ok {
		t.Fatalf("exists: %v %v", ok, err)
	}
	f.rules = nil
	f.on("container ls", "suiteward-db\n") // a name that merely contains the asked one is not it
	if ok, err := rt.Exists(ctx, KindContainer, "suiteward"); err != nil || ok {
		t.Fatalf("a substring match was taken for existence: %v %v", ok, err)
	}

	if err := rt.Create(ctx, KindVolume, VolumeData); err != nil {
		t.Fatal(err)
	}
	if err := rt.Create(ctx, KindNetwork, NetworkName); err != nil {
		t.Fatal(err)
	}
	if err := rt.Create(ctx, KindContainer, "x"); err == nil {
		t.Fatal("a container was created through Create")
	}
	if len(f.find("volume create "+VolumeData)) != 1 || len(f.find("network create "+NetworkName)) != 1 {
		t.Fatalf("calls: %v", f.lines())
	}

	if err := rt.Remove(ctx, "suiteward"); err != nil {
		t.Fatal(err)
	}
	if len(f.find("rm --force --ignore --time 45 suiteward")) != 1 {
		t.Fatalf("calls: %v", f.lines())
	}

	f.rules = nil
	f.on("inspect", `{"Status":"running","Health":{"Status":"healthy","FailingStreak":0}}`+"\n")
	state, err := rt.State(ctx, ContainerDB)
	if err != nil || state.Status != "running" || state.Health != "healthy" {
		t.Fatalf("state: %+v %v", state, err)
	}
	if len(f.find("inspect --type container --format {{json .State}} "+ContainerDB)) != 1 {
		t.Fatalf("calls: %v", f.lines())
	}
	// A container without a health check has no Health in its state; the template of an earlier version failed on it.
	f.rules = nil
	f.on("inspect", `{"Status":"running"}`+"\n")
	state, err = rt.State(ctx, ContainerServe)
	if err != nil || state.Status != "running" || state.Health != "" {
		t.Fatalf("state without health: %+v %v", state, err)
	}
	f.rules = nil
	f.on("inspect", "not json\n")
	if _, err := rt.State(ctx, ContainerServe); err == nil {
		t.Fatal("unreadable inspect output was accepted")
	}
}

func TestPodmanExecAndTTYExec(t *testing.T) {
	ctx := context.Background()
	f := newFake("linux")
	rt := newRootlessPodman(t, f, "")
	f.on("exec -i suiteward-db psql", "1\n")
	out, err := rt.Exec(ctx, ContainerDB, []string{"psql", "-f", "-"}, strings.NewReader("SELECT 1"))
	if err != nil || string(out) != "1\n" {
		t.Fatalf("exec: %q %v", out, err)
	}
	last := f.calls[len(f.calls)-1]
	if last.line() != "exec -i suiteward-db psql -f -" || last.stdin != "SELECT 1" {
		t.Fatalf("unexpected exec call: %+v", last)
	}

	before := len(f.calls)
	if err := rt.ExecTTY(ctx, ContainerServe, []string{"/suiteward", "setup"}); err != nil {
		t.Fatal(err)
	}
	if len(f.calls) != before {
		t.Fatal("the terminal exec went through the capturing Exec, which would record its output")
	}
	if len(f.tty) != 1 || f.tty[0].line() != "exec -it suiteward /suiteward setup" {
		t.Fatalf("tty calls: %+v", f.tty)
	}
}

func TestPodmanPullNeedsADigest(t *testing.T) {
	ctx := context.Background()
	f := newFake("linux")
	rt := newRootlessPodman(t, f, "")
	for _, image := range []string{"ghcr.io/ignisdevne/suiteward:latest", "localhost/suiteward:local", "x@sha256:abc", "x@sha256:" + strings.Repeat("A", 64)} {
		if err := rt.Pull(ctx, image); !errors.Is(err, ErrNotDigestImage) {
			t.Errorf("%s: got %v, want ErrNotDigestImage", image, err)
		}
	}
	if len(f.find("pull")) != 0 {
		t.Fatal("pull ran for a tag")
	}
	if err := rt.Pull(ctx, testImage); err != nil {
		t.Fatal(err)
	}
	if len(f.find("pull "+testImage)) != 1 {
		t.Fatalf("calls: %v", f.lines())
	}
}

// servingSpec is a serve-like container with every kind of argument.
func servingSpec() RunSpec {
	return RunSpec{
		Name: ContainerServe, Image: testImage, Network: NetworkName, Detach: true, Restart: "always", User: "65532:65532",
		Tmpfs: []string{"/tmp"}, Memory: "256m", StopTimeout: 45, Env: []string{"A=b"},
		Secrets: []string{SecretDatabaseURL, SecretMCPAuth}, Volumes: []string{VolumeData + ":/data"},
		Health:  &Health{Cmd: "pg_isready", Interval: "5s", Timeout: "3s", Retries: 12},
		Publish: []string{"127.0.0.1:8081:8080"}, Args: []string{"serve"},
	}
}

func TestInspectAsksForAContainer(t *testing.T) {
	// The service container and the network are both called suiteward; without --type inspect may answer for the network.
	f := newFake("linux")
	rt := newRootlessPodman(t, f, "")
	f.on("inspect", `[{"Name":"suiteward"}]`)
	out, err := rt.Inspect(context.Background(), ContainerServe)
	if err != nil || string(out) != `[{"Name":"suiteward"}]` {
		t.Fatalf("got %q, %v", out, err)
	}
	if len(f.find("inspect --type container suiteward")) != 1 {
		t.Fatalf("calls: %v", f.lines())
	}
}
