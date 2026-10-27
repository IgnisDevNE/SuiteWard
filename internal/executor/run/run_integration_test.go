//go:build sandbox

package run

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

// This test runs a tiny real module through the real sandbox on Docker, with every other part of the executor as in production: the real
// client against a fake of the service, the real composition of the tar, the real go test in a real container. It skips when the engine is
// not Docker, and pulls the pinned image when it is missing. It never touches a Podman machine, and it removes every container it made.

// docker is exec.Command: the commands of this test are built from constants.
var docker = exec.Command

func dockerRuntime(t *testing.T) sandbox.Runtime {
	t.Helper()
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("no docker command")
	}
	rt := sandbox.NewCLI("docker")
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if name, _, err := rt.Info(ctx); err != nil || name != "docker" {
		t.Skipf("the engine is not Docker: %q, %v", name, err)
	}
	if docker("docker", "image", "inspect", goprofile.Image).Run() != nil {
		if out, err := docker("docker", "pull", goprofile.Image).CombinedOutput(); err != nil {
			t.Skipf("cannot pull the pinned image: %v: %s", err, out)
		}
	}
	return rt
}

// sandboxContainers lists the names of the sandbox containers that exist now, whoever made them.
func sandboxContainers(t *testing.T) []string {
	t.Helper()
	out, err := docker("docker", "ps", "--all", "--filter", "name=^"+sandboxPrefix, "--format", "{{.Names}}").Output()
	if err != nil {
		t.Fatalf("docker ps: %v", err)
	}
	return strings.Fields(string(out))
}

const tinyModule = "module example.com/tiny\n\ngo 1.21\n"

const tinyTests = `package tiny

import "testing"

func TestDetects(t *testing.T) {
	if got := Add(1, 2); got != 3 {
		t.Fatalf("Add(1, 2) = %d", got)
	}
}

func TestInert(t *testing.T) {
	if got := Add(0, 0); got != 0 {
		t.Fatalf("Add(0, 0) = %d", got)
	}
}
`

// A receipt whose revision adds a test that fails on the base and one that passes on both: DETECTS and INERT. The test that fails on the
// base is run three times there, as the reruns of the job say.
func TestAReceiptRunsATinyModuleOnRealDocker(t *testing.T) {
	rt := dockerRuntime(t)
	before := sandboxContainers(t)
	t.Cleanup(func() {
		for _, name := range sandboxContainers(t) {
			if !slices.Contains(before, name) {
				t.Errorf("the sandbox container %s was left behind", name)
				_ = docker("docker", "rm", "--force", name).Run()
			}
		}
	})

	s := receiptScenario()
	s.head = repo{"go.mod": tinyModule, "lib.go": "package tiny\n\nfunc Add(a, b int) int { return a + b }\n", "lib_test.go": tinyTests}
	s.base = repo{"go.mod": tinyModule, "lib.go": "package tiny\n\nfunc Add(a, b int) int { return a - b }\n"}
	s.manifest = repo{".suiteward.yml": declaration}
	d := start(t, newService(t, s.serve(1)), rt, func(_ *Config, d *driver) { d.patience = 10 * time.Minute })

	body := d.result().body
	got := decode(t, body)
	sb, _ := got["sandbox"].(map[string]any)
	toolchain, _ := sb["toolchain"].(string)
	runtimeName, _ := sb["runtime"].(string)
	if !strings.HasPrefix(runtimeName, "docker ") {
		t.Errorf("the runtime is %q; want docker and its version", runtimeName)
	}
	// The image says its Go, and the pinned reference names it: the two are the same release.
	if !strings.Contains(goprofile.Image, "golang:"+strings.TrimPrefix(toolchain, "go")+"-") {
		t.Errorf("the image answered %q for its Go; the pinned reference is %s", toolchain, goprofile.Image)
	}
	assertBody(t, body, `{
		"status": "complete", "head": {"sha": "`+headSHA+`"}, "base": {"sha": "`+baseSHA+`"}, `+commonExecutor+`,
		"sandbox": {"imageDigest": "sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd", "toolchain": "`+toolchain+`", "runtime": "`+runtimeName+`"},
		"tests": [
			{"package": ".", "name": "TestDetects", "file": "lib_test.go", "kind": "added", "head": ["pass"], "base": ["fail", "fail", "fail"]},
			{"package": ".", "name": "TestInert", "file": "lib_test.go", "kind": "added", "head": ["pass"], "base": ["pass"]}
		]
	}`)
}
