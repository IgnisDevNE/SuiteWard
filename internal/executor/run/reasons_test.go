package run

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

// withTests is a canonical job with two declared tests that the runtime passes.
func withTests() *scenario {
	s := canonicalScenario()
	s.head = repo{"go.mod": goMod, "lib.go": "package m\n"}
	s.manifest = repo{".suiteward.yml": declaration, "lib_test.go": libTests}
	return s
}

func passing(c call) (sandbox.Outcome, error) { return goTest(c, pass), nil }

// The sandbox object of an unavailable result: the image and the runtime are the executor's own and always known; the toolchain is known
// once the image has answered.
const sandboxWithout = `"sandbox": {"imageDigest": "sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd", "runtime": "docker 29.1.0"}`

// unavailable is the body of a result that gives up with the reason.
func unavailable(reason, sandboxObject string) string {
	return `{"status": "unavailable", "reason": "` + reason + `", "head": {"sha": "` + headSHA + `"}, ` + commonExecutor + `, ` + sandboxObject + `}`
}

// failure is a way to make the job unavailable, with the reason the contract gives for it.
type failure struct {
	reason  string
	sandbox string // the sandbox object that the result carries
	make    func(t *testing.T) (*served, *fakeRuntime, []func(*Config, *driver))
}

type options = []func(*Config, *driver)

func failures() []failure {
	return []failure{
		{reasonExecutorError, commonSandbox, func(*testing.T) (*served, *fakeRuntime, options) {
			return withTests().serve(1), newRuntime(func(call) (sandbox.Outcome, error) { return sandbox.Outcome{}, errors.New("the runtime broke") }), nil
		}},
		{reasonSandboxUnavailable, commonSandbox, func(*testing.T) (*served, *fakeRuntime, options) {
			rt := newRuntime(func(call) (sandbox.Outcome, error) {
				return sandbox.Outcome{}, fmt.Errorf("%w: a cgroup controller is missing", sandbox.ErrUnavailable)
			})
			return withTests().serve(1), rt, nil
		}},
		{reasonArchiveUnavailable, commonSandbox, func(*testing.T) (*served, *fakeRuntime, options) {
			sv := withTests().serve(1)
			sv.archives["head"] = statusAnswer(http.StatusInternalServerError)
			return sv, newRuntime(passing), nil
		}},
		{reasonGitHubUnavailable, commonSandbox, func(*testing.T) (*served, *fakeRuntime, options) {
			sv := withTests().serve(1)
			sv.archives["head"] = statusAnswer(http.StatusBadGateway)
			return sv, newRuntime(passing), nil
		}},
		{reasonFetchFailed, commonSandbox, func(t *testing.T) (*served, *fakeRuntime, options) {
			s := withTests()
			s.head = repo{"go.mod": goMod + "\nrequire example.com/dep v1.0.0\n", "go.sum": "example.com/dep v1.0.0 h1:abc=\n", "lib.go": "package m\n"}
			proxy := proxyServing(t, nil) // the proxy has no module: it answers 404, as it does for a private one
			return s.serve(1), newRuntime(passing), options{func(c *Config, _ *driver) { c.Proxy = proxy }}
		}},
		{reasonInstallFailed, commonSandbox, func(*testing.T) (*served, *fakeRuntime, options) {
			s := withTests()
			s.head = repo{"go.mod": goMod, "go.sum": "example.com/dep v1.0 h1:abc=\n", "lib.go": "package m\n"} // v1.0 is not a canonical version
			return s.serve(1), newRuntime(passing), nil
		}},
		{reasonResourceLimit, commonSandbox, func(*testing.T) (*served, *fakeRuntime, options) {
			rt := newRuntime(func(c call) (sandbox.Outcome, error) {
				out := goTest(c, pass) // the report says pass: only the out-of-band signal counts
				out.OOMKilled = true
				return out, nil
			})
			return withTests().serve(1), rt, nil
		}},
		{reasonToolchainMismatch, commonSandbox, func(*testing.T) (*served, *fakeRuntime, options) {
			s := withTests()
			s.head = repo{"go.mod": "module example.com/m\n\ngo 1.99\n", "lib.go": "package m\n"}
			return s.serve(1), newRuntime(passing), nil
		}},
		{reasonSourceMismatch, commonSandbox, func(*testing.T) (*served, *fakeRuntime, options) {
			sv := withTests().serve(1)
			tampered := repo{"go.mod": goMod, "lib.go": "package m // not the commit's file\n"}
			sv.archives["head"] = bytesAnswer(tampered.archive(headSHA))
			return sv, newRuntime(passing), nil
		}},
		{reasonTooLarge, commonSandbox, func(*testing.T) (*served, *fakeRuntime, options) {
			sv := withTests().serve(1)
			sv.archives["head"] = statusAnswer(http.StatusRequestEntityTooLarge)
			return sv, newRuntime(passing), nil
		}},
		{reasonProfileUnsupported, sandboxWithout, func(*testing.T) (*served, *fakeRuntime, options) {
			s := withTests()
			s.profile.ID = "python"
			return s.serve(1), newRuntime(passing), nil
		}},
	}
}

// Every reason that the executor sets (the rows of the reasons table of docs/contracts/runtime.md whose "Set by" is the executor, and
// github-unavailable, which the executor and the service both set) is what the matching failure produces, and the result of it carries
// the executor, the sandbox and no test.
func TestEveryReasonTheExecutorSetsIsProducedByItsFailure(t *testing.T) {
	want := []string{
		"executor-error", "sandbox-unavailable", "archive-unavailable", "github-unavailable", "fetch-failed", "install-failed", "resource-limit",
		"toolchain-mismatch", "source-mismatch", "too-large", "profile-unsupported",
	}
	var covered []string
	for _, f := range failures() {
		covered = append(covered, f.reason)
	}
	slices.Sort(want)
	slices.Sort(covered)
	if !slices.Equal(want, covered) {
		t.Fatalf("the table covers %v; the contract's executor rows are %v", covered, want)
	}
	for _, f := range failures() {
		t.Run(f.reason, func(t *testing.T) {
			sv, rt, opts := f.make(t)
			d := start(t, newService(t, sv), rt, opts...)
			assertBody(t, d.result().body, unavailable(f.reason, f.sandbox))
		})
	}
}

func TestAnOutOfBandKillIsAResourceLimitWhateverTheReportSays(t *testing.T) {
	for name, kill := range map[string]func(*sandbox.Outcome){
		"the memory limit":        func(o *sandbox.Outcome) { o.OOMKilled = true },
		"the status 137":          func(o *sandbox.Outcome) { o.ExitCode = 137 },
		"the executor's own kill": func(o *sandbox.Outcome) { o.Killed = true },
	} {
		t.Run(name, func(t *testing.T) {
			rt := newRuntime(func(c call) (sandbox.Outcome, error) {
				out := goTest(c, pass)
				kill(&out)
				return out, nil
			})
			d := start(t, newService(t, withTests().serve(1)), rt)
			assertBody(t, d.result().body, unavailable(reasonResourceLimit, commonSandbox))
		})
	}
}

// Output past the executor's bound (64 MiB) is never parsed, and it is a resource limit: a candidate causes it, with a test that writes
// without end, and another run would give the same answer.
func TestOutputPastTheBoundIsNeverReadAndIsAResourceLimit(t *testing.T) {
	rt := newRuntime(func(c call) (sandbox.Outcome, error) {
		out := goTest(c, pass) // the report that was kept says pass: a cut report says nothing
		out.Truncated = true
		return out, nil
	})
	d := start(t, newService(t, withTests().serve(1)), rt)
	assertBody(t, d.result().body, unavailable(reasonResourceLimit, commonSandbox))
	if n := len(rt.invocations()); n != 1 {
		t.Errorf("%d invocations; a resource limit is final and nothing is run again", n)
	}
}

func TestAContainerThatFailedBeforeTheProgramIsASandboxFailureNotATestResult(t *testing.T) {
	rt := newRuntime(func(call) (sandbox.Outcome, error) {
		return sandbox.Outcome{}, fmt.Errorf("%w: tar, the completion marker or the work directory failed (status 113)", sandbox.ErrSetupFailed)
	})
	d := start(t, newService(t, withTests().serve(1)), rt)
	assertBody(t, d.result().body, unavailable(reasonSandboxUnavailable, commonSandbox))
}

// Run may return an Outcome and an error when it could not remove the container: the pair is not told from an error with an empty outcome,
// so it is a failure, and the report that came with it is not read.
func TestAnOutcomeThatComesWithAnErrorIsNotTrusted(t *testing.T) {
	rt := newRuntime(func(c call) (sandbox.Outcome, error) {
		return goTest(c, pass), fmt.Errorf("%w: remove the container: it is busy", sandbox.ErrUnavailable)
	})
	d := start(t, newService(t, withTests().serve(1)), rt)
	assertBody(t, d.result().body, unavailable(reasonSandboxUnavailable, commonSandbox))
}

func TestAnImageThatIsNotTheOneThePinnedProfileNamesIsUnsupportedAndNothingRuns(t *testing.T) {
	s := withTests()
	s.profile.Image = "registry.example.com/images/other:1@sha256:1111111111111111111111111111111111111111111111111111111111111111"
	rt := newRuntime(passing)
	d := start(t, newService(t, s.serve(1)), rt)
	assertBody(t, d.result().body, unavailable(reasonProfileUnsupported, sandboxWithout))
	if n := len(rt.invocations()) + rt.probeCount(); n != 0 {
		t.Errorf("%d containers ran for a job whose image the executor does not hold", n)
	}
}

func TestASandboxThatCannotTellItsGoIsUnavailableBeforeAnythingElseRuns(t *testing.T) {
	rt := newRuntime(passing)
	rt.probeErr = fmt.Errorf("%w: the image is not there", sandbox.ErrUnavailable)
	d := start(t, newService(t, withTests().serve(1)), rt)
	assertBody(t, d.result().body, unavailable(reasonSandboxUnavailable, sandboxWithout))
}

func TestTheToolchainIsWhatTheImageSaysItHasAndIsAskedOnce(t *testing.T) {
	rt := newRuntime(passing)
	rt.goVersion = "go1.27.9"
	d := start(t, newService(t, withTests().serve(1), withTests().serve(2)), rt)
	for range 2 {
		body := decode(t, d.result().body)
		if got := body["sandbox"].(map[string]any)["toolchain"]; got != "go1.27.9" {
			t.Errorf("the toolchain is %v; want what the image answered", got)
		}
	}
	if n := rt.probeCount(); n != 1 {
		t.Errorf("the image was asked %d times for its Go; want once for the life of the executor", n)
	}
}

func TestALeaseLostWhileTheArchiveIsOpenedPostsNothing(t *testing.T) {
	sv := withTests().serve(1)
	sv.archives["head"] = statusAnswer(http.StatusConflict)
	d := start(t, newService(t, sv), newRuntime(passing), func(_ *Config, d *driver) { d.sleeps.reached = make(chan struct{}) })
	wait(d, d.sleeps.reached)
	d.noResult()
}

func TestAStreamThatBreaksShortOfItsLimitIsAGitHubFailureAndOneCutAtItsLimitIsTooLarge(t *testing.T) {
	archive := withTests().head.archive(headSHA)
	for name, tc := range map[string]struct {
		serve  func(http.ResponseWriter)
		limit  int64
		reason string
	}{
		"broken": {func(w http.ResponseWriter) {
			_, _ = w.Write(archive[:len(archive)/2])
			w.(http.Flusher).Flush() // the 200 is sent: from here the status cannot change, and an abort is a broken stream
			panic(http.ErrAbortHandler)
		}, 256 << 20, reasonGitHubUnavailable},
		"cut at the limit": {bytesAnswer(archive), 100, reasonTooLarge},
	} {
		t.Run(name, func(t *testing.T) {
			s := withTests()
			s.limits.ArchiveBytes = tc.limit
			sv := s.serve(1)
			sv.archives["head"] = tc.serve
			d := start(t, newService(t, sv), newRuntime(passing))
			assertBody(t, d.result().body, unavailable(tc.reason, commonSandbox))
		})
	}
}

// The service sends the declaration with its digest in the manifest: a declaration that is not the bytes of that digest, one that does not
// parse, one that a manifest without a declaration comes with, and a manifest with a declaration that did not come are the service's
// bugs, and the executor does not guess.
func TestADeclarationThatDoesNotMatchItsManifestIsAnExecutorError(t *testing.T) {
	for name, job := range map[string]func() *served{
		"other bytes": func() *served {
			sv := withTests().serve(1)
			sv.job.Declaration = []byte("version: 1\n")
			return sv
		},
		"no declaration while the manifest names one": func() *served {
			sv := withTests().serve(1)
			sv.job.Declaration = nil
			return sv
		},
		"bytes that are no declaration": func() *served {
			s := withTests()
			s.manifest[".suiteward.yml"] = "not: [valid"
			return s.serve(1)
		},
		"a declaration while the manifest names none": func() *served {
			s := withTests()
			delete(s.manifest, ".suiteward.yml")
			sv := s.serve(1)
			sv.job.Declaration = []byte(declaration)
			return sv
		},
	} {
		t.Run(name, func(t *testing.T) {
			d := start(t, newService(t, job()), newRuntime(passing))
			assertBody(t, d.result().body, unavailable(reasonExecutorError, commonSandbox))
		})
	}
}

func TestATreeOverHalfTheTmpfsIsTooLargeAndSoIsTheUnpackedSizeOfTheModulesWithIt(t *testing.T) {
	tiny := func(s *scenario) { s.limits.TmpfsMiB, s.limits.MemoryMiB = 1, 1024 } // half of 1 MiB is 512 KiB
	big := withTests()
	tiny(big)
	big.head["blob.bin"] = string(make([]byte, 600<<10))
	d := start(t, newService(t, big.serve(1)), newRuntime(passing))
	assertBody(t, d.result().body, unavailable(reasonTooLarge, commonSandbox))

	zipFile, modFile := moduleZip(t, "example.com/dep", "v1.0.0", 600<<10) // compresses to a few hundred bytes, unpacks to 600 KiB
	proxy := proxyServing(t, map[string][]byte{"example.com/dep/@v/v1.0.0.zip": zipFile, "example.com/dep/@v/v1.0.0.mod": modFile})
	deps := withTests()
	tiny(deps)
	deps.head = repo{"go.mod": goMod + "\nrequire example.com/dep v1.0.0\n", "go.sum": "example.com/dep v1.0.0 h1:abc=\nexample.com/dep v1.0.0/go.mod h1:def=\n", "lib.go": "package m\n"}
	rt := newRuntime(passing)
	d = start(t, newService(t, deps.serve(2)), rt, func(c *Config, _ *driver) { c.Proxy = proxy })
	assertBody(t, d.result().body, unavailable(reasonTooLarge, commonSandbox))
	if n := len(rt.invocations()); n != 0 {
		t.Errorf("%d containers ran go test over modules that pass half of the tmpfs: the check is before go test starts", n)
	}
}
