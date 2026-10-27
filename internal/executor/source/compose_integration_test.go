//go:build sandbox

package source

import (
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

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

// These tests join the two packages the way the executor does: the composed tar is the standard input of a real container on the engine
// that SUITEWARD_SANDBOX_TEST_RUNTIME names, docker (the default) or podman. They pull the pinned image when it is missing. The default
// engine skips them, with the reason, when it is not there. An engine that was asked for fails them instead: a skip would prove nothing
// about it.

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

func engineForTar(t *testing.T) sandbox.Runtime {
	t.Helper()
	engine, unavailable := testEngine(t)
	rt := sandbox.NewCLI(engine)
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
	return rt
}

func sandboxSpec(t *testing.T, stdin io.Reader, argv ...string) sandbox.Spec {
	t.Helper()
	return sandbox.Spec{
		Image:       pinnedImage(t),
		Env:         []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Argv:        argv,
		Workdir:     "src", // where Compose puts the tree
		Limits:      sandbox.Limits{MemoryMiB: 256, CPUs: 1, Pids: 128, TmpfsMiB: 64},
		Stdin:       stdin,
		StdoutLimit: 1 << 20,
	}
}

// pipeCompose is how the executor wires the two: Compose writes into a pipe that is the container's standard input, and the pipe is
// closed with the error of the composition, so that a failure reaches the sandbox runner as the error of its standard input.
func pipeCompose(c *composition, t *testing.T, modules map[string]string) io.Reader {
	arch, entries := c.build(t)
	pr, pw := io.Pipe()
	go func() {
		app, err := Compose(context.Background(), pw, c.tree, arch, entries, c.files, c.limits)
		for name, body := range modules {
			if err == nil {
				err = app.Add(name, int64(len(body)), strings.NewReader(body))
			}
		}
		if err == nil {
			err = app.Close()
		}
		_ = pw.CloseWithError(err)
	}()
	return pr
}

func TestTheComposedTarIsExtractedByTheSandbox(t *testing.T) {
	rt := engineForTar(t)
	long := strings.Repeat("d", 90) + "/" + strings.Repeat("e", 90) + "/" + strings.Repeat("f", 90) + ".txt" // 276 bytes: too long for ustar, so Go writes a pax header
	files := &manifestFiles{}
	c := &composition{
		head: []member{file("a.go", "package a\n"), executable("bin/run.sh", "#!/bin/sh\necho hi\n"), symlink("lnk", "a.go"), file(long, "long\n")},
		tree: []execution.TreeFile{
			treeFile("a.go", "package a\n", execution.FromHead),
			treeFile("bin/run.sh", "#!/bin/sh\necho hi\n", execution.FromHead),
			treeFile("lnk", "a.go", execution.FromHead),
			treeFile(long, "long\n", execution.FromHead),
			{Path: ".suiteward.yml", Content: files.serve("execution: {}\n"), Source: execution.FromManifest},
		},
		files:  files,
		limits: generous,
	}
	script := `cat a.go; stat -c %a bin/run.sh; readlink lnk; cat "$1"; cat .suiteward.yml; cat ../.suiteward/proxy/m/@v/v1.zip; echo; ./bin/run.sh`
	stdin := pipeCompose(c, t, map[string]string{".suiteward/proxy/m/@v/v1.zip": "zipbytes"})
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	out, err := rt.Run(ctx, sandboxSpec(t, stdin, "sh", "-c", script, "sh", long))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	want := "package a\n755\na.go\nlong\nexecution: {}\nzipbytes\nhi\n"
	if string(out.Stdout) != want || out.ExitCode != 0 {
		t.Errorf("the container saw %q (exit %d), want %q", out.Stdout, out.ExitCode, want)
	}
}

func TestAMismatchStopsTheContainerBeforeArgvStarts(t *testing.T) {
	rt := engineForTar(t)
	c := &composition{
		head: []member{file("a.go", "package evil\n"), file("b.go", "package b\n")},
		tree: []execution.TreeFile{
			treeFile("b.go", "package b\n", execution.FromHead), // first in the stream, whole, at a file boundary
			treeFile("a.go", "package a\n", execution.FromHead),
		},
		files:  &manifestFiles{},
		limits: generous,
	}
	stdin := pipeCompose(c, t, nil)
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	_, err := rt.Run(ctx, sandboxSpec(t, stdin, "sleep", "300"))
	if !errors.Is(err, ErrSourceMismatch) {
		t.Fatalf("want ErrSourceMismatch from the composition, got %v", err)
	}
	if took := time.Since(start); took > 30*time.Second {
		t.Errorf("Argv ran on an unverified tree: the run took %s", took)
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

// wholeTar is the stream the sandbox should get: the composed tree, one module file and the completion marker, ended.
func wholeTar(t *testing.T, c *composition, moduleName, module string) []byte {
	t.Helper()
	arch, entries := c.build(t)
	var out bytes.Buffer
	app, err := Compose(context.Background(), &out, c.tree, arch, entries, c.files, c.limits)
	if err != nil {
		t.Fatalf("Compose: %v", err)
	}
	if err := app.Add(moduleName, int64(len(module)), strings.NewReader(module)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := app.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	return out.Bytes()
}

// A cut stream must never run Argv, wherever the cut falls. busybox tar takes a stream that stops at a block boundary for a finished
// tar, so what keeps a part of the tree from running is the container's refusal to start Argv without the completion marker.
func TestACutStreamNeverStartsArgvWhereverTheCutFalls(t *testing.T) {
	rt := engineForTar(t)
	c := &composition{
		head:   []member{file("a.go", "package a\n")},
		tree:   []execution.TreeFile{treeFile("a.go", "package a\n", execution.FromHead)},
		files:  &manifestFiles{},
		limits: generous,
	}
	module := strings.Repeat("m", 1300) // three blocks of data, the last of them partly filled
	whole := wholeTar(t, c, ".suiteward/proxy/m.zip", module)
	end := len(whole) - 1024 - 512 // the end of the last file: the marker's header and the footer follow
	boom := errors.New("the archive stream broke")
	for name, cut := range map[string]int{
		"at the end of the last file, before the marker":     end,
		"at the start of the last block of the last file":    end - 512,
		"in the middle of the last block of the last file":   end - 100,
		"in the middle of the last file":                     end - 1000,
		"in the middle of the header of the marker":          end + 200,
		"after the first block of the header of the marker":  end + 511,
		"at the start of the header of the last file itself": end - 3*512 - 512,
	} {
		for variant, reader := range map[string]func() io.Reader{
			"the stream ends":   func() io.Reader { return bytes.NewReader(whole[:cut]) },
			"the stream breaks": func() io.Reader { return io.MultiReader(bytes.NewReader(whole[:cut]), iotest.ErrReader(boom)) },
		} {
			t.Run(name+", "+variant, func(t *testing.T) {
				s := sandboxSpec(t, reader(), "sleep", "300") // Argv would run until the context ends if it started
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				_, err := rt.Run(ctx, s)
				switch {
				case variant == "the stream ends" && !errors.Is(err, sandbox.ErrSetupFailed):
					t.Errorf("a stream that ends early is a setup failure, got %v", err)
				case variant != "the stream ends" && !errors.Is(err, boom):
					t.Errorf("a stream that breaks is the run's error, got %v", err)
				}
			})
		}
	}
}
