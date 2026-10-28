//go:build sandbox

package sandbox

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"
)

// These tests run real containers on the engine that SUITEWARD_SANDBOX_TEST_RUNTIME names, docker (the default) or podman, and pull the
// pinned image when it is missing. The default engine skips them, with the reason, when it is not there. An engine that was asked for
// fails them instead: a skip would prove nothing about it.

// testEngine is the command of the engine under test, and what reports it unavailable: a skip for the default, a failure for an engine
// that was asked for.
func testEngine(t *testing.T) (engine string, unavailable func(format string, args ...any)) {
	t.Helper()
	switch engine = os.Getenv("SUITEWARD_SANDBOX_TEST_RUNTIME"); engine {
	case "":
		return "docker", t.Skipf
	case "docker", "podman":
		return engine, t.Fatalf
	}
	t.Fatalf("SUITEWARD_SANDBOX_TEST_RUNTIME is %q; want docker or podman", engine)
	return "", nil
}

func engineRuntime(t *testing.T) *cli {
	t.Helper()
	engine, unavailable := testEngine(t)
	if _, err := exec.LookPath(engine); err != nil {
		unavailable("no %s command", engine)
	}
	recorded := &recorder{}
	rt := newCLI(engine, recorded.exec)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	name, _, err := rt.Info(ctx)
	if err != nil {
		unavailable("the %s engine does not answer: %v", engine, err)
	}
	if name != engine {
		unavailable("the %s command is %s, not %s", engine, name, engine)
	}
	if command(engine, "image", "inspect", pinnedImage(t)).Run() != nil {
		if out, err := command(engine, "pull", pinnedImage(t)).CombinedOutput(); err != nil {
			unavailable("cannot pull the pinned image: %v: %s", err, out)
		}
	}
	t.Cleanup(func() {
		for _, name := range recorded.created() {
			if command(engine, "inspect", "--type", "container", name).Run() == nil {
				t.Errorf("the sandbox container %s was left behind", name)
			}
		}
	})
	return rt
}

// recorder runs the commands of the CLI for real and notes the name of each container that a run creates, so that a test looks only at
// the containers it created: other runs of these tests may share the engine.
type recorder struct {
	mu    sync.Mutex
	names []string
}

func (r *recorder) exec(ctx context.Context, name string, args []string, stdin io.Reader, stdout io.Writer) (int, string, error) {
	if i := slices.Index(args, "--name"); args[0] == "run" && i >= 0 {
		r.mu.Lock()
		r.names = append(r.names, args[i+1])
		r.mu.Unlock()
	}
	return osExec(ctx, name, args, stdin, stdout)
}

func (r *recorder) created() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return slices.Clone(r.names)
}

func engineSpec(t *testing.T, stdin []byte, argv ...string) Spec {
	t.Helper()
	return Spec{
		Image:       pinnedImage(t),
		Env:         []string{"PATH=/usr/local/go/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Argv:        argv,
		Workdir:     ".",
		Limits:      Limits{MemoryMiB: 256, CPUs: 1, Pids: 128, TmpfsMiB: 64},
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
	// The completion marker is the last entry: the container refuses to start Argv without it.
	if err := w.WriteHeader(&tar.Header{Name: CompleteMarker, Mode: 0o644, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func runOnEngine(t *testing.T, rt Runtime, s Spec) Outcome {
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
	rt := engineRuntime(t)
	probe := `ls /sys/class/net; touch /tmp/x 2>/dev/null && echo root-writable; echo ok > /work/w && echo work-writable; id -u`
	out := runOnEngine(t, rt, engineSpec(t, tarOf(t, nil), "sh", "-c", probe))
	if got, want := string(out.Stdout), "lo\nwork-writable\n65534\n"; got != want || out.ExitCode != 0 {
		t.Errorf("the container saw %q (exit %d), want %q: only the loopback, a writable /work, a read-only root, uid 65534", got, out.ExitCode, want)
	}
}

func TestTheStdinTarIsExtractedUnderWorkAndTheExitStatusIsThatOfArgv(t *testing.T) {
	rt := engineRuntime(t)
	files := map[string]string{"pkg/x/f.txt": "hello", "top.txt": "top", "link": "top.txt"}
	s := engineSpec(t, tarOf(t, files), "sh", "-c", "pwd; cat f.txt; echo; cat /work/link; echo; exit 7")
	s.Workdir = "pkg/x"
	out := runOnEngine(t, rt, s)
	if got, want := string(out.Stdout), "/work/pkg/x\nhello\ntop\n"; got != want {
		t.Errorf("the container printed %q, want %q", got, want)
	}
	if out.ExitCode != 7 || out.OOMKilled {
		t.Errorf("the exit status must pass through exec: %+v", out)
	}
}

func TestTheEnvironmentOfArgvIsExactlyTheGivenOne(t *testing.T) {
	rt := engineRuntime(t)
	t.Setenv("SUITEWARD_SANDBOX_LEAK", "leaked")
	s := engineSpec(t, tarOf(t, nil), "env")
	s.Env = append(s.Env, "ONLY=this", "EMPTY=")
	out := runOnEngine(t, rt, s)
	got := strings.Fields(string(out.Stdout))
	want := slices.Clone(s.Env)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) || out.ExitCode != 0 {
		t.Errorf("the environment must be the given one and no other:\n got %q\nwant %q", got, want)
	}
	// The image has an environment of its own (GOLANG_VERSION, GOPATH), and the runtime adds HOSTNAME and HOME: none reaches Argv.
	for _, name := range []string{"HOSTNAME", "GOLANG_VERSION", "GOPATH", "HOME", "SUITEWARD_SANDBOX_LEAK"} {
		for _, line := range got {
			if strings.HasPrefix(line, name+"=") {
				t.Errorf("%s reached Argv: %s", name, line)
			}
		}
	}
}

func TestAMemoryHogIsSeenAsOOMKilled(t *testing.T) {
	rt := engineRuntime(t)
	s := engineSpec(t, tarOf(t, nil), "tail", "/dev/zero")
	s.Limits.MemoryMiB = 64
	s.Limits.TmpfsMiB = 16
	out := runOnEngine(t, rt, s)
	if !out.OOMKilled {
		t.Errorf("the memory limit killed the hog, and the runtime must say so: %+v", out)
	}
}

func TestTheStdoutIsCapped(t *testing.T) {
	rt := engineRuntime(t)
	s := engineSpec(t, tarOf(t, nil), "sh", "-c", "yes | head -c 100000")
	s.StdoutLimit = 1000
	out := runOnEngine(t, rt, s)
	if len(out.Stdout) != 1000 || !out.Truncated || out.ExitCode != 0 {
		t.Errorf("want 1000 bytes, truncated: %d bytes, %+v", len(out.Stdout), out.Truncated)
	}
}

func TestAnImageTheRuntimeLacksIsUnavailableAndNothingIsPulled(t *testing.T) {
	rt := engineRuntime(t)
	s := engineSpec(t, tarOf(t, nil), "true")
	s.Image = "docker.io/library/suiteward-no-such-image@sha256:" + strings.Repeat("0", 64)
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if _, err := rt.Run(ctx, s); !errors.Is(err, ErrUnavailable) {
		t.Errorf("want ErrUnavailable, got %v", err)
	}
}

func TestABrokenStdinStopsTheContainerBeforeArgvStarts(t *testing.T) {
	rt := engineRuntime(t)
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
	s := engineSpec(t, nil, "sleep", "300")
	s.Stdin = io.MultiReader(&buf, iotest.ErrReader(boom))
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	if _, err := rt.Run(ctx, s); !errors.Is(err, boom) {
		t.Fatalf("want the error of the standard input, got %v", err)
	}
	// Had tar taken the stream for a whole tar, `sleep 300` would have started.
	if took := time.Since(start); took > 30*time.Second {
		t.Errorf("Argv ran on a part of the tree: the run took %s", took)
	}
}

// command is exec.Command, as the installer takes exec.CommandContext: the tests run the container engine on purpose, with constants and the pin of
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

// unmarkedTar is a whole tar, footer included, that lacks the completion marker.
func unmarkedTar(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := tar.NewWriter(&buf)
	if err := w.WriteHeader(&tar.Header{Name: "top.txt", Mode: 0o644, Size: 3, Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := w.Write([]byte("top")); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A failure before Argv starts is not the status of Argv: tar, the test for the marker and cd exit with the reserved status.
func TestAFailureBeforeArgvIsASetupFailureAndArgvNeverStarts(t *testing.T) {
	rt := engineRuntime(t)
	for name, tc := range map[string]struct {
		stdin   []byte
		workdir string
	}{
		"a whole tar with no completion marker": {unmarkedTar(t), "."},
		"bytes that are not a tar":              {[]byte("this is not a tar"), "."},
		"a work directory that is not in it":    {tarOf(t, nil), "no/such/dir"},
	} {
		t.Run(name, func(t *testing.T) {
			s := engineSpec(t, tc.stdin, "sleep", "300") // Argv would run until the context ends if it started, and then it is no setup failure
			s.Workdir = tc.workdir
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			if _, err := rt.Run(ctx, s); !errors.Is(err, ErrSetupFailed) {
				t.Errorf("want ErrSetupFailed, got %v", err)
			}
		})
	}
}
