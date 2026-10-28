package installer

import (
	"errors"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
)

// The sandboxes run with --pull=never, so the Go image has to be in the storage of the engine that runs them. On the co-located tier doctor asks
// that engine; on the isolated tier the storage belongs to the executor's account, which the service account cannot read, and doctor says that the
// host step is what verifies it.

func TestDoctorColocatedChecksTheGoImageOfTheSandboxesThroughTheEngine(t *testing.T) {
	f, s := colocated{}.build(t)
	_, res, _ := runDoctor(t, f, s)
	wantOK(t, res, "executor.image")
	if len(f.find("image inspect "+goprofile.Image)) != 1 {
		t.Errorf("doctor did not ask the engine for the image: %v", f.calls)
	}

	f, s = colocated{over: []rule{{match: "image inspect " + goprofile.Image, err: errors.New("no such image")}}}.build(t)
	code, res, _ := runDoctor(t, f, s)
	if code != 1 {
		t.Errorf("exit %d, want 1", code)
	}
	wantFail(t, res, "executor.image", "Go image")
	if fix := res["executor.image"].Fix; !strings.Contains(fix, "install") {
		t.Errorf("the fix %q must name the install, which pulls it", fix)
	}
}

func TestDoctorIsolatedSaysThatTheHostStepChecksTheGoImage(t *testing.T) {
	f, s := isolatedRig{}.build(t)
	_, res, _ := runDoctor(t, f, s)
	wantOK(t, res, "executor.image")
	for _, want := range []string{ExecutorAccount, "host step", "fingerprint"} {
		if !strings.Contains(res["executor.image"].Detail, want) {
			t.Errorf("the detail %q lacks %q", res["executor.image"].Detail, want)
		}
	}
}
