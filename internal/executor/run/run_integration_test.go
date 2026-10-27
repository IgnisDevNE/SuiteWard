//go:build sandbox

package run

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

// These tests run a tiny real module through the real sandbox, with every other part of the executor as in production: the real client
// against a fake of the service, the real composition of the tar, the real go test in a real container. The engine is the one that
// SUITEWARD_SANDBOX_TEST_RUNTIME names, docker (the default) or podman; the tests pull the pinned image when it is missing, and remove
// every container they made. The default engine skips them, with the reason, when it is not there. An engine that was asked for fails them
// instead: a skip would prove nothing about it.

// engineExec is exec.Command: the commands of these tests are built from constants and the engine's own name.
var engineExec = exec.Command

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

// engineCommand is a command of the engine under test.
func engineCommand(t *testing.T, args ...string) *exec.Cmd {
	t.Helper()
	engine, _ := testEngine(t)
	return engineExec(engine, args...)
}

func engineRuntime(t *testing.T) sandbox.Runtime {
	t.Helper()
	engine, unavailable := testEngine(t)
	if _, err := exec.LookPath(engine); err != nil {
		unavailable("no %s command", engine)
	}
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
	if engineCommand(t, "image", "inspect", goprofile.Image).Run() != nil {
		if out, err := engineCommand(t, "pull", goprofile.Image).CombinedOutput(); err != nil {
			unavailable("cannot pull the pinned image: %v: %s", err, out)
		}
	}
	return rt
}

// sandboxContainers lists the names of the sandbox containers that exist now, whoever made them.
func sandboxContainers(t *testing.T) []string {
	t.Helper()
	out, err := engineCommand(t, "ps", "--all", "--filter", "name=^"+sandboxPrefix, "--format", "{{.Names}}").Output()
	if err != nil {
		t.Fatalf("ps of the engine: %v", err)
	}
	return strings.Fields(string(out))
}

// watchContainers gives the sandbox containers that exist now, and fails the test, at its end, for each one that it left behind.
func watchContainers(t *testing.T) []string {
	t.Helper()
	before := sandboxContainers(t)
	t.Cleanup(func() {
		for _, name := range sandboxContainers(t) {
			if !slices.Contains(before, name) {
				t.Errorf("the sandbox container %s was left behind", name)
				_ = engineCommand(t, "rm", "--force", name).Run()
			}
		}
	})
	return before
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
func TestAReceiptRunsATinyModuleOnARealEngine(t *testing.T) {
	rt := engineRuntime(t)
	watchContainers(t)

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
	if engine, _ := testEngine(t); !strings.HasPrefix(runtimeName, engine+" ") {
		t.Errorf("the runtime is %q; want %s and its version", runtimeName, engine)
	}
	// The image says its Go, and the pinned reference names it: the two are the same release.
	pinned, releaseTag := goprofile.Image, "golang:"+strings.TrimPrefix(toolchain, "go")+"-" // locals: argOrder reads a constant first as a reversed pair
	if !strings.Contains(pinned, releaseTag) {
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

const sleepingTest = `package tiny

import (
	"testing"
	"time"
)

func TestSleeps(t *testing.T) { time.Sleep(time.Hour) }
`

// After the signal the job in flight has the grace; when it is over the executor kills the container through the sandbox, posts nothing
// and stops cleanly. The test's sleeper ends the grace at once, and the test then looks for the container that the job made.
func TestTheGraceAfterTheSignalKillsTheContainerOnARealEngine(t *testing.T) {
	rt := engineRuntime(t)
	before := watchContainers(t)

	s := canonicalScenario()
	s.head = repo{"go.mod": tinyModule, "lib.go": "package tiny\n"}
	s.manifest = repo{".suiteward.yml": declaration, "lib_test.go": sleepingTest}
	svc := newService(t, s.serve(1))
	d := start(t, svc, rt, gate(graceWait, closed()), func(_ *Config, d *driver) { d.patience = 2 * time.Minute })

	// The first container asks the image for its Go and ends at once; the container of go test is the one that the signal must stop.
	deadline := time.Now().Add(2 * time.Minute)
	for !runsGoTest(t, before, sandboxContainers(t)) {
		if time.Now().After(deadline) {
			t.Fatal("the job never made the container of go test")
		}
		time.Sleep(100 * time.Millisecond)
	}
	d.cancel() // the signal: the grace is over at once, and the work is dropped
	if err := d.stopped(); err != nil {
		t.Errorf("Run = %v; want a clean stop", err)
	}
	d.noResult()
	if left := sandboxContainers(t); len(left) != len(before) {
		t.Errorf("the sandbox containers are %v after the job was dropped; want those that were there before: its container was killed and removed", left)
	}
}

// runsGoTest says whether one of the containers that are not in before has go test among its arguments.
func runsGoTest(t *testing.T, before, now []string) bool {
	for _, name := range now {
		if slices.Contains(before, name) {
			continue
		}
		out, err := engineCommand(t, "inspect", "--type", "container", "--format", "{{json .Args}}", name).Output()
		var args []string
		if err == nil && json.Unmarshal(out, &args) == nil && slices.Contains(args, "-run") {
			return true
		}
	}
	return false
}

const oneTest = "package %s\n\nimport \"testing\"\n\nfunc TestOne(t *testing.T) {}\n"

// A go.mod that the head adds over a test directory of the manifest is the candidate's, and go cannot resolve the package under it: it reports
// a setup failure of the pattern, runs the rest of the invocation, and the manifest's test there reads build-error, which blocks, and not
// excluded, which does not.
func TestAHeadGoModOverATestDirectoryOfTheManifestIsABuildErrorOnARealEngine(t *testing.T) {
	rt := engineRuntime(t)
	watchContainers(t)

	s := canonicalScenario()
	s.head = repo{"go.mod": tinyModule, "lib.go": "package tiny\n", "sub/go.mod": "module example.com/sub\n\ngo 1.21\n"}
	s.manifest = repo{".suiteward.yml": declaration, "lib_test.go": fmt.Sprintf(oneTest, "tiny"), "sub/sub_test.go": fmt.Sprintf(oneTest, "sub")}
	d := start(t, newService(t, s.serve(1)), rt, func(_ *Config, d *driver) { d.patience = 5 * time.Minute })

	body := d.result().body
	sb, _ := decode(t, body)["sandbox"].(map[string]any)
	toolchain, _ := sb["toolchain"].(string)
	runtimeName, _ := sb["runtime"].(string)
	assertBody(t, body, `{
		"status": "complete", "head": {"sha": "`+headSHA+`"}, `+commonExecutor+`,
		"sandbox": {"imageDigest": "sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd", "toolchain": "`+toolchain+`", "runtime": "`+runtimeName+`"},
		"tests": [
			{"package": ".", "name": "TestOne", "file": "lib_test.go", "kind": "declared", "head": ["pass"]},
			{"package": "sub", "name": "TestOne", "file": "sub/sub_test.go", "kind": "declared", "head": ["build-error"]}
		]
	}`)
}
