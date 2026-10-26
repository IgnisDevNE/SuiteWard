//go:build sandbox

package source

import (
	"context"
	"errors"
	"io"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

// These tests join the two packages the way the executor does: the composed tar is the standard input of a real container on
// Docker. They skip when the engine is not there and use the image the sandbox tests pull.
const goImage = "docker.io/library/golang:1.27.3-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd"

func dockerForTar(t *testing.T) sandbox.Runtime {
	t.Helper()
	rt := sandbox.NewCLI("docker")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if name, _, err := rt.Info(ctx); err != nil || name != "docker" {
		t.Skipf("no Docker engine: %q, %v", name, err)
	}
	if exec.Command("docker", "image", "inspect", goImage).Run() != nil {
		if out, err := exec.Command("docker", "pull", goImage).CombinedOutput(); err != nil {
			t.Skipf("cannot pull the pinned image: %v: %s", err, out)
		}
	}
	return rt
}

func sandboxSpec(stdin io.Reader, argv ...string) sandbox.Spec {
	return sandbox.Spec{
		Image:       goImage,
		Env:         []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Argv:        argv,
		Workdir:     ".",
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
	long := strings.Repeat("d", 60) + "/" + strings.Repeat("e", 60) + "/" + strings.Repeat("f", 60) + ".txt"
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
	out, err := rt.Run(ctx, sandboxSpec(stdin, "sh", "-c", script, "sh", long))
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
	_, err := rt.Run(ctx, sandboxSpec(stdin, "sleep", "300"))
	if !errors.Is(err, ErrSourceMismatch) {
		t.Fatalf("want ErrSourceMismatch from the composition, got %v", err)
	}
	if took := time.Since(start); took > 30*time.Second {
		t.Errorf("Argv ran on an unverified tree: the run took %s", took)
	}
}
