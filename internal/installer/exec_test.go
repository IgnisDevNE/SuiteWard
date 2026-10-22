package installer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func envValue(env []string, key string) (string, bool) {
	for _, kv := range env {
		k, v, _ := strings.Cut(kv, "=")
		if strings.EqualFold(k, key) {
			return v, true
		}
	}
	return "", false
}

func TestChildEnvPutsAnAbsoluteCommandsDirectoryFirstOnPath(t *testing.T) {
	dir := filepath.Join(os.TempDir(), "docker-bin")
	original := strings.Join([]string{"one", "two"}, string(os.PathListSeparator))
	// "Path" is how Windows spells it; the existing spelling is kept and no second PATH is added.
	got := childEnv([]string{"Path=" + original, "FOO=bar"}, filepath.Join(dir, "docker"), []string{"version"})
	path, ok := envValue(got, "PATH")
	if !ok || path != dir+string(os.PathListSeparator)+original {
		t.Fatalf("PATH is %q, want %q first", path, dir)
	}
	if foo, _ := envValue(got, "FOO"); foo != "bar" {
		t.Fatal("another variable was lost")
	}
	count := 0
	for _, kv := range got {
		if k, _, _ := strings.Cut(kv, "="); strings.EqualFold(k, "PATH") {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("%d PATH variables", count)
	}
}

func TestChildEnvLeavesABareCommandNameAlone(t *testing.T) {
	in := []string{"PATH=/usr/bin", "CONTAINER_HOST=ssh://caller"}
	got := childEnv(in, "podman", []string{"info"})
	if strings.Join(got, "|") != strings.Join(in, "|") {
		t.Fatalf("got %v", got)
	}
}

func TestChildEnvDropsContainerHostWhenAConnectionIsSelected(t *testing.T) {
	in := []string{"PATH=/usr/bin", "Container_Host=ssh://caller", "CONTAINER_CONNECTION=other", "FOO=bar"}
	got := childEnv(in, "podman", []string{"--connection", "machine", "info"})
	if _, ok := envValue(got, "CONTAINER_HOST"); ok {
		t.Fatal("CONTAINER_HOST would override the selected connection")
	}
	if _, ok := envValue(got, "FOO"); !ok {
		t.Fatal("another variable was dropped")
	}
}

func TestRuntimesRefuseIncompleteDeps(t *testing.T) {
	complete := newFake("linux").deps()
	for name, mutate := range map[string]func(*Deps){
		"no exec":        func(d *Deps) { d.Exec = nil },
		"no interactive": func(d *Deps) { d.Interactive = nil },
		"no getenv":      func(d *Deps) { d.Getenv = nil },
	} {
		deps := complete
		mutate(&deps)
		if _, err := NewPodman(context.Background(), deps, ""); err == nil {
			t.Errorf("podman, %s: accepted", name)
		}
		if _, err := NewDocker(context.Background(), deps, DockerOptions{}); err == nil {
			t.Errorf("docker, %s: accepted", name)
		}
	}
}
