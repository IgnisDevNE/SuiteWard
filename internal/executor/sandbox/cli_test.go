package sandbox

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"testing/iotest"
	"time"
)

const pinned = "registry.example/sandbox/runtime:1@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"

const (
	exitedState = `{"Status":"exited","Running":false,"OOMKilled":false,"ExitCode":0,"Error":"","StartedAt":"2026-10-26T21:58:19.830463123Z"}`
	wantScript  = `tar -x -C /work && cd "/work/$1" && shift && exec "$@"`
)

type call struct {
	name string
	args []string
	ctx  error // the error of the context the call was made with
}

// fakeRuntime stands in for the CLI of a runtime: it records every command and answers the ones a run makes.
type fakeRuntime struct {
	mu    sync.Mutex
	calls []call

	onRun    func(ctx context.Context, stdin io.Reader, stdout io.Writer) (int, string, error)
	state    string // what `inspect` prints; empty means the runtime has no such container
	killExit func() int
	version  string // what `version` prints; empty means the runtime does not answer
	killed   chan struct{}
	killOnce sync.Once
}

func newFake() *fakeRuntime {
	return &fakeRuntime{state: exitedState, killed: make(chan struct{})}
}

func (f *fakeRuntime) exec(ctx context.Context, name string, args []string, stdin io.Reader, stdout io.Writer) (int, string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, call{name, slices.Clone(args), ctx.Err()})
	f.mu.Unlock()
	switch args[0] {
	case "run":
		if f.onRun != nil {
			return f.onRun(ctx, stdin, stdout)
		}
		if _, err := io.Copy(io.Discard, stdin); err != nil {
			return 1, "", err
		}
		return 0, "", nil
	case "inspect":
		if f.state == "" {
			return 1, "Error: No such object", nil
		}
		_, err := io.WriteString(stdout, f.state+"\n")
		return 0, "", err
	case "kill":
		if f.killExit != nil && f.killExit() != 0 {
			return 1, "Error response from daemon: No such container", nil
		}
		f.killOnce.Do(func() { close(f.killed) })
		return 0, "", nil
	case "version":
		if f.version == "" {
			return 1, "Cannot connect to the runtime", nil
		}
		_, err := io.WriteString(stdout, f.version)
		return 0, "", err
	default: // rm
		return 0, "", nil
	}
}

func (f *fakeRuntime) commands(verb string) []call {
	f.mu.Lock()
	defer f.mu.Unlock()
	var found []call
	for _, c := range f.calls {
		if c.args[0] == verb {
			found = append(found, c)
		}
	}
	return found
}

func (f *fakeRuntime) verbs() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	verbs := make([]string, len(f.calls))
	for i, c := range f.calls {
		verbs[i] = c.args[0]
	}
	return verbs
}

func validSpec() Spec {
	return Spec{
		Image:       pinned,
		Env:         []string{"HOME=/work/home", "GOFLAGS="},
		Argv:        []string{"go", "test", "./..."},
		Workdir:     "pkg/x",
		Limits:      Limits{MemoryMiB: 3072, CPUs: 4, Pids: 512, TmpfsMiB: 1024, KillAfter: time.Hour},
		Stdin:       strings.NewReader("a tar"),
		StdoutLimit: 1 << 20,
	}
}

func run(t *testing.T, f *fakeRuntime, binary string, s Spec) (Outcome, error) {
	t.Helper()
	return newCLI(binary, f.exec).Run(context.Background(), s)
}

func TestRunBuildsTheIsolationOfADR0028ForBothRuntimes(t *testing.T) {
	for _, binary := range []string{"docker", "podman"} {
		t.Run(binary, func(t *testing.T) {
			f := newFake()
			if _, err := run(t, f, binary, validSpec()); err != nil {
				t.Fatalf("Run: %v", err)
			}
			runs := f.commands("run")
			if len(runs) != 1 || runs[0].name != binary {
				t.Fatalf("want one %s run, got %+v", binary, runs)
			}
			got := runs[0].args
			i := slices.Index(got, "--name")
			if i < 0 || i+1 >= len(got) {
				t.Fatalf("no --name in %q", got)
			}
			name := got[i+1]
			if !strings.HasPrefix(name, "suiteward-sandbox-") {
				t.Errorf("the container name %q does not carry the sandbox prefix", name)
			}
			// Exactly these arguments: nothing else (no --rm, no mount, no published port, no extra capability) may creep in.
			want := []string{
				"run", "--name", name, "-i", "--pull=never", "--network=none", "--read-only",
				"--tmpfs", "/work:rw,exec,nosuid,nodev,size=1024m",
				"--cap-drop=ALL", "--security-opt", "no-new-privileges", "--user", "65534:65534",
				"--pids-limit", "512", "--memory", "3072m", "--memory-swap", "3072m", "--cpus", "4",
				"--env", "HOME=/work/home", "--env", "GOFLAGS=",
				pinned, "sh", "-c", wantScript, "sh", "pkg/x", "go", "test", "./...",
			}
			if !slices.Equal(got, want) {
				t.Errorf("the run arguments differ\n got %q\nwant %q", got, want)
			}
		})
	}
}

func TestRunNamesEveryContainerUniquely(t *testing.T) {
	f := newFake()
	for range 2 {
		s := validSpec()
		if _, err := run(t, f, "docker", s); err != nil {
			t.Fatalf("Run: %v", err)
		}
	}
	runs := f.commands("run")
	if len(runs) != 2 || runs[0].args[2] == runs[1].args[2] {
		t.Fatalf("two runs must have two names: %+v", runs)
	}
}

func TestRunReadsTheStateThenRemovesTheContainer(t *testing.T) {
	f := newFake()
	f.state = `{"Status":"exited","OOMKilled":true,"ExitCode":3,"Error":"","StartedAt":"2026-10-26T21:58:19Z"}`
	out, err := run(t, f, "docker", validSpec())
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if out.ExitCode != 3 || !out.OOMKilled || out.Killed || out.Truncated {
		t.Errorf("the outcome must be the runtime's state: %+v", out)
	}
	if got, want := f.verbs(), []string{"run", "inspect", "rm"}; !slices.Equal(got, want) {
		t.Fatalf("commands %q, want %q", got, want)
	}
	name := f.commands("run")[0].args[2]
	if got, want := f.commands("inspect")[0].args, []string{"inspect", "--type", "container", "--format", "{{json .State}}", name}; !slices.Equal(got, want) {
		t.Errorf("inspect %q, want %q", got, want)
	}
	if got, want := f.commands("rm")[0].args, []string{"rm", "--force", name}; !slices.Equal(got, want) {
		t.Errorf("rm %q, want %q", got, want)
	}
}

func TestRunReportsALegitimateExitStatusThatLooksLikeARuntimeError(t *testing.T) {
	f := newFake()
	f.state = `{"Status":"exited","OOMKilled":false,"ExitCode":126,"Error":"","StartedAt":"2026-10-26T21:58:19Z"}`
	out, err := run(t, f, "docker", validSpec())
	if err != nil || out.ExitCode != 126 {
		t.Fatalf("a container that ran and exited 126 is an outcome, not a runtime error: %+v, %v", out, err)
	}
}

func TestRunKillsTheContainerAfterKillAfter(t *testing.T) {
	f := newFake()
	f.onRun = func(ctx context.Context, _ io.Reader, _ io.Writer) (int, string, error) {
		select {
		case <-f.killed:
			return 137, "", nil
		case <-ctx.Done():
			return -1, "", ctx.Err()
		}
	}
	f.state = `{"Status":"exited","OOMKilled":false,"ExitCode":137,"Error":"","StartedAt":"2026-10-26T21:58:19Z"}`
	s := validSpec()
	s.Limits.KillAfter = 10 * time.Millisecond
	out, err := run(t, f, "docker", s)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !out.Killed || out.ExitCode != 137 {
		t.Errorf("want the executor's own kill reported with the status 137: %+v", out)
	}
	name := f.commands("run")[0].args[2]
	if kills := f.commands("kill"); len(kills) == 0 || !slices.Equal(kills[0].args, []string{"kill", name}) {
		t.Errorf("the container must be killed by name: %+v", kills)
	}
	if len(f.commands("rm")) != 1 {
		t.Errorf("the killed container must still be removed: %q", f.verbs())
	}
}

func TestRunKeepsKillingUntilTheContainerExists(t *testing.T) {
	f := newFake()
	var mu sync.Mutex
	refusals := 2
	f.killExit = func() int {
		mu.Lock()
		defer mu.Unlock()
		if refusals > 0 {
			refusals--
			return 1 // the runtime has not created the container yet
		}
		return 0
	}
	f.onRun = func(ctx context.Context, _ io.Reader, _ io.Writer) (int, string, error) {
		select {
		case <-f.killed:
			return 137, "", nil
		case <-ctx.Done():
			return -1, "", ctx.Err()
		}
	}
	s := validSpec()
	s.Limits.KillAfter = time.Millisecond
	out, err := run(t, f, "docker", s)
	if err != nil || !out.Killed {
		t.Fatalf("a kill that failed once must be tried again: %+v, %v", out, err)
	}
	if n := len(f.commands("kill")); n != 3 {
		t.Errorf("want 3 kill attempts, got %d", n)
	}
}

func TestRunDoesNotKillAContainerThatEndsFirst(t *testing.T) {
	f := newFake()
	out, err := run(t, f, "docker", validSpec())
	if err != nil || out.Killed {
		t.Fatalf("Run: %+v, %v", out, err)
	}
	if kills := f.commands("kill"); len(kills) != 0 {
		t.Errorf("no kill before KillAfter: %+v", kills)
	}
}

func TestRunCapsTheStdout(t *testing.T) {
	for _, tc := range []struct {
		name      string
		limit     int64
		want      string
		truncated bool
	}{
		{"within the cap", 100, "0123456789", false},
		{"exactly the cap", 10, "0123456789", false},
		{"past the cap", 4, "0123", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.onRun = func(_ context.Context, _ io.Reader, stdout io.Writer) (int, string, error) {
				for _, chunk := range []string{"01234", "56789"} {
					if n, err := io.WriteString(stdout, chunk); err != nil || n != len(chunk) {
						t.Errorf("a write must never fail, or the container would block: %d, %v", n, err)
					}
				}
				return 0, "", nil
			}
			s := validSpec()
			s.StdoutLimit = tc.limit
			out, err := run(t, f, "docker", s)
			if err != nil {
				t.Fatalf("Run: %v", err)
			}
			if string(out.Stdout) != tc.want || out.Truncated != tc.truncated {
				t.Errorf("stdout %q truncated %v, want %q %v", out.Stdout, out.Truncated, tc.want, tc.truncated)
			}
		})
	}
}

func TestRunRefusesAWeakerSpecBeforeAnyCommand(t *testing.T) {
	digest := strings.Repeat("a", 64)
	mutations := map[string]func(*Spec){
		"a tag, no digest":                     func(s *Spec) { s.Image = "golang:1.27.3-alpine" },
		"no image":                             func(s *Spec) { s.Image = "" },
		"a short digest":                       func(s *Spec) { s.Image = "golang@sha256:" + digest[:63] },
		"a long digest":                        func(s *Spec) { s.Image = "golang@sha256:" + digest + "0" },
		"an upper-case digest":                 func(s *Spec) { s.Image = "golang@sha256:" + strings.ToUpper(digest) },
		"another algorithm":                    func(s *Spec) { s.Image = "golang@sha512:" + digest },
		"no name":                              func(s *Spec) { s.Image = "@sha256:" + digest },
		"a name that is a flag":                func(s *Spec) { s.Image = "-v@sha256:" + digest },
		"a space in the image":                 func(s *Spec) { s.Image = "gol ang@sha256:" + digest },
		"an empty workdir":                     func(s *Spec) { s.Workdir = "" },
		"an absolute workdir":                  func(s *Spec) { s.Workdir = "/etc" },
		"a workdir above /work":                func(s *Spec) { s.Workdir = "../etc" },
		"a workdir that climbs":                func(s *Spec) { s.Workdir = "a/../../b" },
		"a workdir that is not clean":          func(s *Spec) { s.Workdir = "a//b" },
		"a workdir with a trailing /":          func(s *Spec) { s.Workdir = "a/" },
		"a workdir with a NUL":                 func(s *Spec) { s.Workdir = "a\x00b" },
		"an env entry with no value":           func(s *Spec) { s.Env = []string{"HOME"} },
		"an env entry with no name":            func(s *Spec) { s.Env = []string{"=x"} },
		"an env name with a space":             func(s *Spec) { s.Env = []string{"A B=x"} },
		"an env name that starts with a digit": func(s *Spec) { s.Env = []string{"1A=x"} },
		"an env value with a NUL":              func(s *Spec) { s.Env = []string{"A=b\x00c"} },
		"no argv":                              func(s *Spec) { s.Argv = nil },
		"an empty program":                     func(s *Spec) { s.Argv = []string{""} },
		"a program that is a flag":             func(s *Spec) { s.Argv = []string{"-x"} },
		"no memory limit":                      func(s *Spec) { s.Limits.MemoryMiB = 0 },
		"no cpu limit":                         func(s *Spec) { s.Limits.CPUs = 0 },
		"no pids limit":                        func(s *Spec) { s.Limits.Pids = 0 },
		"no tmpfs size":                        func(s *Spec) { s.Limits.TmpfsMiB = 0 },
		"no kill deadline":                     func(s *Spec) { s.Limits.KillAfter = 0 },
		"a negative memory limit":              func(s *Spec) { s.Limits.MemoryMiB = -1 },
		"no stdout limit":                      func(s *Spec) { s.StdoutLimit = 0 },
		"no stdin":                             func(s *Spec) { s.Stdin = nil },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			s := validSpec()
			mutate(&s)
			_, err := run(t, f, "docker", s)
			if !errors.Is(err, ErrInvalidSpec) {
				t.Errorf("want ErrInvalidSpec, got %v", err)
			}
			if len(f.calls) != 0 {
				t.Errorf("a refused spec must run no command: %q", f.verbs())
			}
		})
	}
}

func TestRunAcceptsTheWorkdirOfTheRoot(t *testing.T) {
	f := newFake()
	s := validSpec()
	s.Workdir = "."
	if _, err := run(t, f, "docker", s); err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestRunMapsARuntimeFailureToErrUnavailable(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*fakeRuntime)
	}{
		{"the runtime could not start the container", func(f *fakeRuntime) {
			f.state = `{"Status":"created","OOMKilled":false,"ExitCode":126,"Error":"OCI runtime error: crun: controller 'cpu' is not available","StartedAt":"0001-01-01T00:00:00Z"}`
		}},
		{"the container never started and reports no error", func(f *fakeRuntime) {
			f.state = `{"Status":"exited","OOMKilled":false,"ExitCode":125,"Error":"","StartedAt":"0001-01-01T00:00:00Z"}`
		}},
		{"the runtime has no such container", func(f *fakeRuntime) { f.state = "" }},
		{"the CLI could not run", func(f *fakeRuntime) {
			f.onRun = func(context.Context, io.Reader, io.Writer) (int, string, error) {
				return -1, "", errors.New("exec: docker: executable file not found in $PATH")
			}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			tc.setup(f)
			if _, err := run(t, f, "podman", validSpec()); !errors.Is(err, ErrUnavailable) {
				t.Errorf("want ErrUnavailable, got %v", err)
			}
		})
	}
}

func TestRunRemovesAContainerThatTheRuntimeCreatedButCouldNotStart(t *testing.T) {
	f := newFake()
	f.state = `{"Status":"created","OOMKilled":false,"ExitCode":126,"Error":"cannot start","StartedAt":"0001-01-01T00:00:00Z"}`
	_, _ = run(t, f, "docker", validSpec())
	if len(f.commands("rm")) != 1 {
		t.Errorf("the created container must be removed: %q", f.verbs())
	}
}

func TestRunFailsClosedWhenItsStandardInputBreaks(t *testing.T) {
	boom := errors.New("the archive stream broke")
	f := newFake()
	var got []byte
	f.onRun = func(_ context.Context, stdin io.Reader, _ io.Writer) (int, string, error) {
		var err error
		got, err = io.ReadAll(stdin)
		return 0, "", err
	}
	s := validSpec()
	s.Stdin = io.MultiReader(strings.NewReader("abc"), iotest.ErrReader(boom))
	_, err := run(t, f, "docker", s)
	if !errors.Is(err, boom) {
		t.Fatalf("the error of the standard input must be the run's: %v", err)
	}
	// A tar that stops at a file boundary is a complete tar to busybox, which would run the command on a part of the tree. The
	// stream therefore ends in a block that no tar reader accepts as a header.
	poison := bytes.Repeat([]byte{0xff}, 512)
	if !bytes.HasPrefix(got, []byte("abc")) || !bytes.HasSuffix(got, poison) || len(got) != 3+512 {
		t.Errorf("the container must receive what was read, then a block that breaks tar: %d bytes", len(got))
	}
	if len(f.commands("rm")) != 1 {
		t.Errorf("the container must be removed: %q", f.verbs())
	}
}

func TestRunRemovesTheContainerOfACancelledRun(t *testing.T) {
	f := newFake()
	ctx, cancel := context.WithCancel(context.Background())
	f.onRun = func(ctx context.Context, _ io.Reader, _ io.Writer) (int, string, error) {
		cancel()
		return -1, "", ctx.Err()
	}
	_, err := newCLI("docker", f.exec).Run(ctx, validSpec())
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want the context's error, got %v", err)
	}
	rm := f.commands("rm")
	if len(rm) != 1 {
		t.Fatalf("the container of a cancelled run must be removed: %q", f.verbs())
	}
	if rm[0].ctx != nil || f.commands("inspect")[0].ctx != nil {
		t.Errorf("the cleanup must not run under the cancelled context")
	}
}

func TestInfoNamesTheRuntimeAndItsVersion(t *testing.T) {
	const dockerJSON = `{"Client":{"Version":"29.9.2"},"Server":{"Platform":{"Name":"Docker Desktop 4.96.1 (244132)"},"Version":"29.9.2","Components":[{"Name":"Engine","Version":"29.9.2"},{"Name":"containerd","Version":"v2.4.2"}]}}`
	const podmanLocal = `{"Client":{"APIVersion":"5.8.2","Version":"5.8.2","OsArch":"linux/amd64"}}`
	const podmanRemote = `{"Client":{"Version":"5.8.2"},"Server":{"Platform":{"Name":"linux/amd64/fedora-42"},"Version":"5.5.0","Components":[{"Name":"Podman Engine","Version":"5.5.0"}]}}`
	for _, tc := range []struct{ name, binary, json, wantName, wantVersion string }{
		{"Docker Desktop", "docker", dockerJSON, "docker", "29.9.2"},
		{"local Podman, which reports only its client", "podman", podmanLocal, "podman", "5.8.2"},
		{"a Podman machine, whose server is the engine", "podman", podmanRemote, "podman", "5.5.0"},
		{"Podman behind a command named docker", "docker", podmanRemote, "podman", "5.5.0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFake()
			f.version = tc.json
			name, version, err := newCLI(tc.binary, f.exec).Info(context.Background())
			if err != nil || name != tc.wantName || version != tc.wantVersion {
				t.Errorf("Info = %q %q %v, want %q %q", name, version, err, tc.wantName, tc.wantVersion)
			}
			if got, want := f.calls[0].args, []string{"version", "--format", "{{json .}}"}; !slices.Equal(got, want) {
				t.Errorf("version command %q, want %q", got, want)
			}
		})
	}
}

func TestInfoRefusesWhatItCannotRead(t *testing.T) {
	for name, json := range map[string]string{
		"a runtime that does not answer":  "",
		"not JSON":                        "Docker version 29.9.2",
		"no version":                      `{"Server":{"Components":[{"Name":"Engine"}]}}`,
		"a version that is not printable": `{"Server":{"Version":"29.9.2\nnew line","Components":[{"Name":"Engine"}]}}`,
		"a version that is too long":      `{"Server":{"Version":"` + strings.Repeat("1", 60) + `","Components":[{"Name":"Engine"}]}}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake()
			f.version = json
			if _, _, err := newCLI("docker", f.exec).Info(context.Background()); !errors.Is(err, ErrUnavailable) {
				t.Errorf("want ErrUnavailable, got %v", err)
			}
		})
	}
}
