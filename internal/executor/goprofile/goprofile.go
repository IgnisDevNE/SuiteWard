// Package goprofile is everything Go-specific about an execution run, as pure functions: which tests a tree declares,
// which ones a revision added or changed, the markers of test-aware code, the exact go test command and its environment,
// and how the run's output becomes one observation per test. It runs nothing and reads no file.
package goprofile

// Image is the Go image of every sandbox, pinned by digest. It is the builder image of deploy/Containerfile, so the Go
// that builds the release is the Go that runs the protected tests.
const Image = "docker.io/library/golang:1.27.3-alpine@sha256:f2c872bcee949b7be9c490efd02b48a8a2fbf947cdf533f1c58525ec9d8170fd"

// StateDir is the directory of the sandbox's /work, beside the repository, that holds everything go reads and writes of
// its own: the module proxy, HOME (and so go's env file), TMPDIR, the build cache and the module cache. No path of the
// repository may be under it. The tree is extracted into the same /work, and a file of the tree there would be go's own:
// an env file overrides the empty GOFLAGS and sets GOPRIVATE and the like, and a build cache entry replaces the compiled
// form of a package. The caller refuses such a tree.
const StateDir = ".suiteward"

// TreeDir is the directory of the sandbox's /work that holds the repository.
const TreeDir = ""

// The directories under StateDir that the caller fills, as tar entry names: the prefetched module proxy goes under
// ProxyDir, and a placeholder file in HomeDir and in TmpDir creates them (go stops when TMPDIR does not exist, and tar
// creates the directories of the files it extracts).
const (
	ProxyDir = StateDir + "/proxy"
	HomeDir  = StateDir + "/home"
	TmpDir   = StateDir + "/tmp"
)

// TestID names one top-level test function: Package is the directory of its package relative to the repository root
// ("." for the root), Name the function and File the declaring file, a slash path relative to the root.
type TestID struct{ Package, Name, File string }

// Declared is what a tree declares: the tests the sandbox can run, and the ones it cannot (the build context leaves
// their file out), which are listed and never run.
type Declared struct{ Tests, Excluded []TestID }
