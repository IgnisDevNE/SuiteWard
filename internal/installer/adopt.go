package installer

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
)

var errNotImplemented = errors.New("not implemented")

// Diff is one difference between an installed stack and what the named release would install.
type Diff struct {
	Source string // the unit file or the container
	Field  string // the unit section or the container property
	Want   string // what the release has; empty when the installed stack has something extra
	Got    string // what is installed; empty when the installed stack lacks it
}

// DiffError stops an adoption: something is installed that the installer does not know how to carry over, and it never
// overwrites an installation silently.
type DiffError struct{ Diffs []Diff }

func (e *DiffError) Error() string {
	var b strings.Builder
	b.WriteString("the installed stack differs from the release; nothing was changed:")
	for _, d := range e.Diffs {
		b.WriteString("\n  " + d.Source + " " + d.Field + ": want " + d.Want + ", got " + d.Got)
	}
	return b.String()
}

// Adopted is what an adoption carries over from an installed stack.
type Adopted struct {
	Settings Settings // tier, runtime, ports, job timeout and, from containers, the GitHub values
	Image    string   // the release image that is installed
	EnvFile  string   // units only: the EnvironmentFile path found, which the isolated installer moves
}

// AdoptUnits compares the installed unit files with the units of a named release (reference; nil means the embedded ones of
// this binary). The ports, tier, job timeout, image digest and env-file path are known fields and are carried into the
// settings; any other difference, in any unit, is returned in a *DiffError.
func AdoptUnits(installed, reference map[string][]byte) (Adopted, error) {
	return Adopted{}, errNotImplemented
}

// AdoptContainers does the same for a stack of containers made by deploy/local.ps1 (Podman), from the output of
// `podman inspect` of suiteward-db and suiteward (and suiteward-migrate when it exists). The ports, tier, job timeout,
// GitHub values and image are carried; any other difference from ColocatedStack is a *DiffError.
func AdoptContainers(inspected map[string][]byte) (Adopted, error) {
	return Adopted{}, errNotImplemented
}

// parseMemory reads a --memory value (k, m or g suffix, or plain bytes) as bytes.
func parseMemory(s string) (int64, error) {
	unit := int64(1)
	if n := len(s); n > 0 {
		switch s[n-1] {
		case 'k':
			unit, s = 1<<10, s[:n-1]
		case 'm':
			unit, s = 1<<20, s[:n-1]
		case 'g':
			unit, s = 1<<30, s[:n-1]
		}
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%q is not a memory size (a number with an optional k, m or g)", s)
	}
	return n * unit, nil
}
