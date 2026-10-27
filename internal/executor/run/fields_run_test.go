package run

import (
	"net/http"
	"strings"
	"testing"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
)

// A result that the service would refuse is found before anything runs, and it says why: the executor never posts what comes back as a 400.
func TestAResultTheServiceWouldRefuseIsFoundBeforeAnythingRunsAndSaysWhy(t *testing.T) {
	deep := strings.Repeat(strings.Repeat("d", 100)+"/", 6) + "x" // a package of 606 bytes: over the bound of 512
	for name, tc := range map[string]struct {
		file   string
		reason string
	}{
		"a package over its bound":            {deep + "/x_test.go", reasonTooLarge},
		"a test file with a bidi override":    {"lib" + bidiOverride + "_test.go", reasonSourceMismatch},
		"a test file with a line separator":   {"lib" + lineSeparator + "_test.go", reasonSourceMismatch},
		"a test file with a zero-width space": {"lib" + zeroWidthSpace + "_test.go", reasonSourceMismatch},
	} {
		t.Run(name, func(t *testing.T) {
			s := withTests()
			s.manifest = repo{".suiteward.yml": declaration, tc.file: libTests}
			rt := newRuntime(passing)
			d := start(t, newService(t, s.serve(1)), rt)
			assertBody(t, d.result().body, unavailable(tc.reason, commonSandbox))
			if n := len(rt.invocations()); n != 0 {
				t.Errorf("%d containers ran go test for a result that could not be posted", n)
			}
		})
	}
}

func TestAMarkedFileOrASubmoduleWithAFormatCharacterIsASourceMismatch(t *testing.T) {
	for name, change := range map[string]func(*scenario){
		"a marker's file": func(s *scenario) {
			s.head["a"+zeroWidthSpace+"b.go"] = "package m\n\nimport \"os\"\n\nvar _ = os.Getenv(\"CI\")\n"
		},
		"a submodule": func(s *scenario) { s.headExtra = append(s.headExtra, treeSubmodule("sub"+paragraphSeparator+"mod")) },
	} {
		t.Run(name, func(t *testing.T) {
			s := aReceipt()
			change(s)
			rt := newRuntime(func(c call) (sandbox.Outcome, error) { return detects(c) })
			d := start(t, newService(t, s.serve(3)), rt)
			body := decode(t, d.result().body)
			if body["status"] != "unavailable" || body["reason"] != reasonSourceMismatch {
				t.Errorf("the result is %v; want unavailable for source-mismatch", body)
			}
			if n := len(rt.invocations()); n != 0 {
				t.Errorf("%d containers ran go test for a result that could not be posted", n)
			}
		})
	}
}

// treeSubmodule is a submodule entry of a tree: no archive holds it, and the executor skips it and lists it.
func treeSubmodule(path string) client.TreeEntry {
	return client.TreeEntry{Path: path, Mode: "160000", SHA: headSHA}
}

// GET /jobs/{id}/files/{digest} answers 413 for a listed file over the cap of a file: the file will not be smaller on another attempt, so it
// is too-large (final). A file that the service cannot give for another reason (404, 500) is an archive failure, which is retried; a lost
// lease is dropped, as it is on every route.
func TestAManifestFileThatTheServiceRefusesAsTooLargeIsTooLargeAndOtherFailuresAreArchiveFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		status int
		reason string
	}{
		"413 too-large":       {http.StatusRequestEntityTooLarge, reasonTooLarge},
		"404 not found":       {http.StatusNotFound, reasonArchiveUnavailable},
		"500 the store fails": {http.StatusInternalServerError, reasonArchiveUnavailable},
	} {
		t.Run(name, func(t *testing.T) {
			sv := withTests().serve(1)
			sv.fileStatus = tc.status
			rt := newRuntime(passing)
			d := start(t, newService(t, sv), rt)
			assertBody(t, d.result().body, unavailable(tc.reason, commonSandbox))
			if n := len(rt.invocations()); n != 0 {
				t.Errorf("%d containers ran go test over a manifest that could not be read", n)
			}
		})
	}
}

func TestALeaseLostOnAManifestFilePostsNothing(t *testing.T) {
	sv := withTests().serve(1)
	sv.fileStatus = http.StatusConflict
	d := start(t, newService(t, sv), newRuntime(passing), func(_ *Config, d *driver) { d.sleeps.reached = make(chan struct{}) })
	wait(d, d.sleeps.reached)
	d.noResult()
}
