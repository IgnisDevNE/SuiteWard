// Package goprofile is everything Go-specific about an execution run, as pure functions: which tests a tree declares,
// which ones a revision added or changed, the markers of test-aware code, the exact go test command and its environment,
// and how the run's output becomes one observation per test. It runs nothing and reads no file.
package goprofile

// Image is the Go image of every sandbox, pinned by digest. It is the builder image of deploy/Containerfile, so the Go
// that builds the release is the Go that runs the protected tests.
const Image = "docker.io/library/golang:1.27.3-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd"

// The layout of the sandbox's /work, as tar entry names. The runner extracts one tar into /work and then runs the
// process in the Workdir under it.
//
// TreeDir holds the repository, and it is the Workdir: go test runs at the module root, so the packages of Command are
// ./dir from there. StateDir holds everything go reads and writes of its own, outside the tree: the module proxy, HOME,
// TMPDIR, the build cache and the module cache. Neither is inside the other, so no path of the tree can be a file that go
// reads as its own: a go env file under HOME overrides the empty GOFLAGS and sets GOPRIVATE and the like (GOENV=off
// stops go reading one, and the layout does not depend on it), and a build cache entry replaces the compiled form of a
// package.
const (
	TreeDir  = "src"
	StateDir = ".suiteward"
)

// The directories under StateDir that the caller fills: the prefetched module proxy goes under ProxyDir, and a
// placeholder file in HomeDir and in TmpDir creates them (go stops when TMPDIR does not exist, and tar creates the
// directories of the files it extracts).
const (
	ProxyDir = StateDir + "/proxy"
	HomeDir  = StateDir + "/home"
	TmpDir   = StateDir + "/tmp"
)

// TestID names one top-level test function: Package is the directory of its package relative to the repository root
// ("." for the root), Name the function and File the declaring file, a slash path relative to the root.
type TestID struct{ Package, Name, File string }

// The reasons that a declared test is excluded: the build context leaves its file out (a constraint, or a name for
// another system), or its file is under a directory with a go.mod of its own, which the module's invocation cannot reach.
const (
	ReasonBuildConstraint = "build-constraint"
	ReasonNestedModule    = "nested-module"
)

// ExcludedTest is a declared test that the sandbox does not run, and why.
type ExcludedTest struct {
	TestID
	Reason string
}

// Declared is what a tree declares: the tests the sandbox can run, and the ones it cannot, which are listed with their
// reason and never run.
type Declared struct {
	Tests    []TestID
	Excluded []ExcludedTest
	Unparsed []string
}
