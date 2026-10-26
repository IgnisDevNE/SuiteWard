package prefetch

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/mod/module"
	"golang.org/x/mod/sumdb/dirhash"
	modzip "golang.org/x/mod/zip"
)

// command is exec.CommandContext. These tests run the go tool of the toolchain that runs them, with arguments of their own, so gosec's G204
// (a subprocess launched with a variable) does not apply.
var command = exec.CommandContext

// The go command, run with the environment of the sandbox (ADR 0028, section 11) and a module proxy that is nothing but the prefetched files,
// builds and tests a module that needs two others. No network is used: the fake proxy of Fetch is on loopback, and go sees only a directory.
//
// Established with this test and a few offline runs of go 1.27: go build, go vet, go test (also as `go test -json -count=1 -shuffle=on -run`)
// and go list ./... need only the .mod and .zip files that go.sum names; the .info file of a version is read only by `go mod download` and by
// `go list -m -json`, which the sandbox does not run, so it is not planned. go does not vendor a go.mod that names no version.

type fixtureModule struct {
	path, version string
	files         map[string]string
}

func (m fixtureModule) publish(t *testing.T) (zipFile []byte, zipHash, modHash string) {
	t.Helper()
	dir := t.TempDir()
	for name, content := range m.files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	var buf bytes.Buffer
	if err := modzip.CreateFromDir(&buf, module.Version{Path: m.path, Version: m.version}, dir); err != nil {
		t.Fatal(err)
	}
	zipHash, err := dirhash.HashDir(dir, m.path+"@"+m.version, dirhash.Hash1)
	if err != nil {
		t.Fatal(err)
	}
	modHash, err = dirhash.Hash1([]string{"go.mod"}, func(string) (io.ReadCloser, error) {
		return io.NopCloser(strings.NewReader(m.files["go.mod"])), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return buf.Bytes(), zipHash, modHash
}

const (
	beeVersion = "v0.0.0-20200101000000-abcdefabcdef"
	aVersion   = "v1.0.0-RC1"
)

// Two modules whose paths and versions need escaping: example.com/Bee (a capital) and v1.0.0-RC1 (capitals in a version).
func fixtureModules() (bee, a fixtureModule) {
	bee = fixtureModule{"example.com/Bee", beeVersion, map[string]string{
		"go.mod": "module example.com/Bee\n\ngo 1.21\n",
		"bee.go": "package bee\n\nfunc Name() string { return \"bee\" }\n",
	}}
	a = fixtureModule{"example.com/a", aVersion, map[string]string{
		"go.mod": "module example.com/a\n\ngo 1.21\n\nrequire example.com/Bee " + beeVersion + "\n",
		"a.go":   "package a\n\nimport bee \"example.com/Bee\"\n\nfunc Name() string { return \"a+\" + bee.Name() }\n",
	}}
	return bee, a
}

const (
	appGoMod = "module example.com/app\n\ngo 1.21\n\nrequire (\n\texample.com/Bee " + beeVersion + " // indirect\n\texample.com/a " + aVersion + "\n)\n"
	appMain  = "package main\n\nimport (\n\t\"fmt\"\n\n\t\"example.com/a\"\n)\n\nfunc main() { fmt.Println(a.Name()) }\n"
	appTest  = "package main\n\nimport (\n\t\"testing\"\n\n\t\"example.com/a\"\n)\n\nfunc TestName(t *testing.T) {\n\tif a.Name() != \"a+bee\" {\n\t\tt.Fatal(a.Name())\n\t}\n}\n"
)

type tarSink struct{ tw *tar.Writer }

func (s tarSink) Add(name string, size int64, r io.Reader) error {
	if err := s.tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: size, Typeflag: tar.TypeReg}); err != nil {
		return err
	}
	_, err := io.Copy(s.tw, r)
	return err
}

func untar(t *testing.T, archive []byte, dir string) {
	t.Helper()
	tr := tar.NewReader(bytes.NewReader(archive))
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if !filepath.IsLocal(h.Name) {
			t.Fatalf("the tar holds %q, which leaves its directory", h.Name)
		}
		data, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		target := filepath.Join(dir, filepath.FromSlash(h.Name))
		if err := os.MkdirAll(filepath.Dir(target), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, data, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func fileURL(dir string) string {
	p := filepath.ToSlash(dir)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p // C:/dir on Windows
	}
	return "file://" + p
}

func writeApp(t *testing.T, dir, goSum string) {
	t.Helper()
	for name, content := range map[string]string{"go.mod": appGoMod, "go.sum": goSum, "main.go": appMain, "main_test.go": appTest} {
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// goRunner runs the go tool as the sandbox does: no GOFLAGS, no checksum database, the local toolchain, no private-module settings.
type goRunner struct {
	goBin    string
	proxy    string
	modCache string
}

func newGoRunner(t *testing.T, proxy string) *goRunner {
	t.Helper()
	goBin, err := exec.LookPath("go")
	if err != nil {
		t.Fatalf("no go tool to run: %v", err)
	}
	r := &goRunner{goBin: goBin, proxy: proxy, modCache: filepath.Join(t.TempDir(), "modcache")}
	// The module cache is read-only, which t.TempDir cannot remove. This cleanup runs before the removal of the directory.
	t.Cleanup(func() { _, _ = r.run(t, t.TempDir(), "clean", "-modcache") })
	return r
}

func (r *goRunner) run(t *testing.T, dir string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := command(ctx, r.goBin, args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GOPROXY="+r.proxy, "GOFLAGS=", "GOSUMDB=off", "GOWORK=off", "GOTOOLCHAIN=local", "GOENV=off", "GOMODCACHE="+r.modCache,
		"GOPRIVATE=", "GONOSUMDB=", "GONOPROXY=", "GOINSECURE=",
	)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r *goRunner) mustRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := r.run(t, dir, args...)
	if err != nil {
		t.Fatalf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return out
}

func TestPrefetchedProxyLetsGoBuildAndTestOffline(t *testing.T) {
	bee, a := fixtureModules()
	beeZip, beeZipHash, beeModHash := bee.publish(t)
	aZip, aZipHash, aModHash := a.publish(t)
	goSum := strings.Join([]string{
		"example.com/Bee " + beeVersion + " " + beeZipHash,
		"example.com/Bee " + beeVersion + "/go.mod " + beeModHash,
		"example.com/a " + aVersion + " " + aZipHash,
		"example.com/a " + aVersion + "/go.mod " + aModHash,
	}, "\n") + "\n"
	upstream := newFakeProxy(t, map[string][]byte{
		"example.com/!bee/@v/" + beeVersion + ".zip": beeZip,
		"example.com/!bee/@v/" + beeVersion + ".mod": []byte(bee.files["go.mod"]),
		"example.com/a/@v/v1.0.0-!r!c1.zip":          aZip,
		"example.com/a/@v/v1.0.0-!r!c1.mod":          []byte(a.files["go.mod"]),
	})

	plan, err := PlanSide([]byte(appGoMod), []byte(goSum), false, Limits{Modules: 10, Bytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	wantNames(t, plan,
		"example.com/!bee/@v/"+beeVersion+".mod", "example.com/!bee/@v/"+beeVersion+".zip",
		"example.com/a/@v/v1.0.0-!r!c1.mod", "example.com/a/@v/v1.0.0-!r!c1.zip",
	)

	// The files stream from the proxy into a tar, which is extracted to be the sandbox's file:// proxy.
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	stats, err := Fetch(context.Background(), clientFor(t, upstream.Server, ""), upstream.URL, plan, tarSink{tw}, Limits{Modules: 10, Bytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if stats.Files != 4 || stats.Unpacked == 0 {
		t.Errorf("stats = %+v, want 4 files and the unpacked size of two zips", stats)
	}
	proxyDir := filepath.Join(t.TempDir(), "proxy")
	untar(t, archive.Bytes(), proxyDir)

	appDir := filepath.Join(t.TempDir(), "app")
	writeApp(t, appDir, goSum)
	g := newGoRunner(t, fileURL(proxyDir))
	g.mustRun(t, appDir, "build", "./...")
	if out := g.mustRun(t, appDir, "test", "-count=1", "./..."); !strings.Contains(out, "ok") {
		t.Errorf("go test printed no ok:\n%s", out)
	}

	t.Run("a side that vendors fetches nothing and builds with no proxy at all", func(t *testing.T) {
		vendored := filepath.Join(t.TempDir(), "app")
		writeApp(t, vendored, goSum)
		g.mustRun(t, vendored, "mod", "vendor") // from the proxy above, once, to have a vendor/ tree
		if _, err := os.Stat(filepath.Join(vendored, "vendor", "modules.txt")); err != nil {
			t.Fatal(err)
		}
		plan, err := PlanSide([]byte(appGoMod), []byte(goSum), true, Limits{Modules: 10, Bytes: 1 << 20})
		if err != nil || !plan.Vendor || len(plan.Files) != 0 {
			t.Fatalf("plan = %+v, err = %v, want Vendor and no files", plan, err)
		}
		var hits atomic.Int32
		silent := httptest.NewTLSServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { hits.Add(1) }))
		defer silent.Close()
		var vendorTar bytes.Buffer
		stats, err := Fetch(context.Background(), clientFor(t, silent, ""), silent.URL, plan, tarSink{tar.NewWriter(&vendorTar)}, Limits{Modules: 10, Bytes: 1 << 20})
		if err != nil || stats != (Stats{}) || hits.Load() != 0 || vendorTar.Len() != 0 {
			t.Errorf("stats = %+v, err = %v, requests = %d, want nothing fetched", stats, err, hits.Load())
		}
		offline := newGoRunner(t, "off") // GOPROXY=off, and a module cache that holds nothing
		offline.mustRun(t, vendored, "build", "./...")
		if out := offline.mustRun(t, vendored, "test", "-count=1", "./..."); !strings.Contains(out, "ok") {
			t.Errorf("go test of the vendored side printed no ok:\n%s", out)
		}
	})
}
