// Package goprofile is everything Go-specific about an execution run, as pure functions: which tests a tree declares,
// which ones a revision added or changed, the markers of test-aware code, the exact go test command and its environment,
// and how the run's output becomes one observation per test. It runs nothing and reads no file.
package goprofile

import "errors"

// Image is the Go image of every sandbox, pinned by digest. It is the builder image of deploy/Containerfile.
const Image = ""

// The directories of the sandbox's /work that go needs beside the repository, as tar entry names. The caller writes the
// prefetched module proxy under ProxyDir and a placeholder file in HomeDir and in TmpDir: go stops when TMPDIR does not
// exist, and tar creates the directories of the files it extracts.
const (
	ProxyDir = ".suiteward/proxy"
	HomeDir  = ".suiteward/home"
	TmpDir   = ".suiteward/tmp"
)

// TestID names one top-level test function: Package is the directory of its package relative to the repository root
// ("." for the root), Name the function and File the declaring file, a slash path relative to the root.
type TestID struct{ Package, Name, File string }

// Declared is what a tree declares: the tests the sandbox can run, and the ones it cannot (the build context leaves
// their file out), which are listed and never run.
type Declared struct{ Tests, Excluded []TestID }

var errNotImplemented = errors.New("not implemented")
