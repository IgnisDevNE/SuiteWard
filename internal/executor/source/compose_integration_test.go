//go:build sandbox

package source

import (
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

// These tests join the two packages the way the executor does: the composed tar is the standard input of a real container on
// Docker. They skip when the engine is not there, and pull the pinned image when it is missing.

func dockerForTar(t *testing.T) sandbox.Runtime {
	t.Helper()
	rt := sandbox.NewCLI("docker")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if name, _, err := rt.Info(ctx); err != nil || name != "docker" {
		t.Skipf("no Docker engine: %q, %v", name, err)
	}
	if command("docker", "image", "inspect", pinnedImage(t)).Run() != nil {
		if out, err := command("docker", "pull", pinnedImage(t)).CombinedOutput(); err != nil {
			t.Skipf("cannot pull the pinned image: %v: %s", err, out)
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
		Limits:      sandbox.Limits{MemoryMiB: 256, CPUs: 1, Pids: 128, TmpfsMiB: 64, KillAfter: time.Minute},
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
	rt := dockerForTar(t)
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
	script := `cat a.go; stat -c %a bin/run.sh; readlink lnk; cat "$1"; cat .suiteward.yml; cat proxy/m/@v/v1.zip; echo; ./bin/run.sh`
	stdin := pipeCompose(c, t, map[string]string{"proxy/m/@v/v1.zip": "zipbytes"})
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
	rt := dockerForTar(t)
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
