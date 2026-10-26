//go:build sandbox

package sandbox_test

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"testing/iotest"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

// These tests run real containers on Docker. They skip when the engine is not there, and pull the pinned image when it is missing.

func docker(t *testing.T) sandbox.Runtime {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	rt := sandbox.NewCLI("docker")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	name, _, err := rt.Info(ctx)
	if err != nil {
		t.Skipf("the docker engine does not answer: %v", err)
	}
	if name != "docker" {
		t.Skipf("the docker command is %s, not Docker", name)
	}
	if command("docker", "image", "inspect", pinnedImage(t)).Run() != nil {
		if out, err := command("docker", "pull", pinnedImage(t)).CombinedOutput(); err != nil {
			t.Skipf("cannot pull the pinned image: %v: %s", err, out)
		}
	}
	t.Cleanup(func() {
		out, err := exec.Command("docker", "ps", "-a", "-q", "--filter", "name=suiteward-sandbox-").Output()
		if err != nil || strings.TrimSpace(string(out)) != "" {
			t.Errorf("a sandbox container was left behind: %q, %v", out, err)
		}
	})
	return rt
}

func spec(t *testing.T, stdin []byte, argv ...string) sandbox.Spec {
	t.Helper()
	return sandbox.Spec{
		Image:       pinnedImage(t),
		Env:         []string{"PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Argv:        argv,
		Workdir:     ".",
		Limits:      sandbox.Limits{MemoryMiB: 256, CPUs: 1, Pids: 128, TmpfsMiB: 64, KillAfter: time.Minute},
		Stdin:       bytes.NewReader(stdin),
		StdoutLimit: 1 << 20,
	}
}

func tarOf(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	for _, name := range []string{"pkg/x/f.txt", "top.txt", "link"} {
		body, ok := files[name]
		if !ok {
			continue
		}
		h := &tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}
		if name == "link" {
			h = &tar.Header{Name: name, Typeflag: tar.TypeSymlink, Linkname: body}
		}
		if err := w.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			if _, err := w.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func run(t *testing.T, rt sandbox.Runtime, s sandbox.Spec) sandbox.Outcome {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := rt.Run(ctx, s)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return out
}

func TestTheSandboxHasNoNetworkAndAReadOnlyRoot(t *testing.T) {
	rt := docker(t)
	probe := `ls /sys/class/net; touch /etc/x 2>/dev/null && echo root-writable; echo ok > /work/w && echo work-writable; id -u`
	out := run(t, rt, spec(t, tarOf(t, nil), "sh", "-c", probe))
	if got, want := string(out.Stdout), "lo\nwork-writable\n65534\n"; got != want || out.ExitCode != 0 {
		t.Errorf("the container saw %q (exit %d), want %q: only the loopback, a writable /work, a read-only root, uid 65534", got, out.ExitCode, want)
	}
}

func TestTheStdinTarIsExtractedUnderWorkAndTheExitStatusIsThatOfArgv(t *testing.T) {
	rt := docker(t)
	files := map[string]string{"pkg/x/f.txt": "hello", "top.txt": "top", "link": "top.txt"}
	s := spec(t, tarOf(t, files), "sh", "-c", "pwd; cat f.txt; echo; cat /work/link; echo; exit 7")
	s.Workdir = "pkg/x"
	out := run(t, rt, s)
	if got, want := string(out.Stdout), "/work/pkg/x\nhello\ntop\n"; got != want {
		t.Errorf("the container printed %q, want %q", got, want)
	}
	if out.ExitCode != 7 || out.OOMKilled || out.Killed {
		t.Errorf("the exit status must pass through exec: %+v", out)
	}
}

func TestOnlyTheGivenEnvironmentReachesTheContainer(t *testing.T) {
	rt := docker(t)
	t.Setenv("SUITEWARD_SANDBOX_LEAK", "leaked")
	s := spec(t, tarOf(t, nil), "env")
	s.Env = append(s.Env, "ONLY=this")
	out := run(t, rt, s)
	lines := strings.Split(string(out.Stdout), "\n")
	if !contains(lines, "ONLY=this") || contains(lines, "SUITEWARD_SANDBOX_LEAK=leaked") {
		t.Errorf("the environment must be the given one: %q", lines)
	}
}

func contains(lines []string, want string) bool {
	for _, l := range lines {
		if l == want {
			return true
		}
	}
	return false
}

func TestAMemoryHogIsSeenAsOOMKilled(t *testing.T) {
	rt := docker(t)
	s := spec(t, tarOf(t, nil), "tail", "/dev/zero")
	s.Limits.MemoryMiB = 64
	s.Limits.TmpfsMiB = 16
	out := run(t, rt, s)
	if !out.OOMKilled {
		t.Errorf("the memory limit killed the hog, and the runtime must say so: %+v", out)
	}
}

func TestKillAfterKillsARunawayContainer(t *testing.T) {
	rt := docker(t)
	s := spec(t, tarOf(t, nil), "sleep", "300")
	s.Limits.KillAfter = 2 * time.Second
	start := time.Now()
	out := run(t, rt, s)
	if !out.Killed || out.ExitCode != 137 {
		t.Errorf("want the executor's kill with the status 137: %+v", out)
	}
	if took := time.Since(start); took > time.Minute {
		t.Errorf("the kill took %s", took)
	}
}

func TestTheStdoutIsCapped(t *testing.T) {
	rt := docker(t)
	s := spec(t, tarOf(t, nil), "sh", "-c", "yes | head -c 100000")
	s.StdoutLimit = 1000
	out := run(t, rt, s)
	if len(out.Stdout) != 1000 || !out.Truncated || out.ExitCode != 0 {
		t.Errorf("want 1000 bytes, truncated: %d bytes, %+v", len(out.Stdout), out.Truncated)
	}
}

func TestAnImageTheRuntimeLacksIsUnavailableAndNothingIsPulled(t *testing.T) {
	rt := docker(t)
	s := spec(t, tarOf(t, nil), "true")
	s.Image = "docker.io/library/suiteward-no-such-image@sha256:" + strings.Repeat("0", 64)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := rt.Run(ctx, s); !errors.Is(err, sandbox.ErrUnavailable) {
		t.Errorf("want ErrUnavailable, got %v", err)
	}
}

func TestABrokenStdinStopsTheContainerBeforeArgvStarts(t *testing.T) {
	rt := docker(t)
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if err := w.WriteHeader(&tar.Header{Name: "top.txt", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("top")); err != nil {
		t.Fatal(err)
	}
	if err := w.Flush(); err != nil { // the tar stops at a file boundary, which busybox tar takes for a finished tar
		t.Fatal(err)
	}
	boom := errors.New("the archive stream broke")
	s := spec(t, nil, "sleep", "300")
	s.Stdin = io.MultiReader(&buf, iotest.ErrReader(boom))
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := rt.Run(ctx, s); !errors.Is(err, boom) {
		t.Fatalf("want the error of the standard input, got %v", err)
	}
	// Had tar taken the stream for a whole tar, `sleep 300` would have run until KillAfter.
	if took := time.Since(start); took > 30*time.Second {
		t.Errorf("Argv ran on a part of the tree: the run took %s", took)
	}
}

// command is exec.Command, as the installer takes exec.CommandContext: the tests run Docker on purpose, with constants and the pin of
// the Containerfile, so gosec's G204 (a subprocess launched with a variable) does not apply.
var command = exec.Command

// pinnedImage is the Go builder image of deploy/Containerfile, so that the one pin of the repository is the only copy of it: the
// Containerfile spells the tag with ${GO_VERSION}, which .go-version fills in.
func pinnedImage(t *testing.T) string {
	t.Helper()
	containerfile, err := os.ReadFile("../../../deploy/Containerfile")
	if err != nil {
		t.Fatalf("read the Containerfile: %v", err)
	}
	version, err := os.ReadFile("../../../.go-version")
	if err != nil {
		t.Fatalf("read .go-version: %v", err)
	}
	pin := regexp.MustCompile(`docker\.io/library/golang:\$\{GO_VERSION\}-alpine@sha256:[0-9a-f]{64}`).Find(containerfile)
	if pin == nil {
		t.Fatal("the Containerfile no longer pins the golang builder image by digest")
	}
	return strings.ReplaceAll(string(pin), "${GO_VERSION}", strings.TrimSpace(string(version)))
}
