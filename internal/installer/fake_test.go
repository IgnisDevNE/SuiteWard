package installer

import (
	"context"
	"io"
	"os"
	"strings"
	"testing"
)

// Digest strings are built at run time: scripts/check-pins.ps1 compares every literal name@sha256 copy in the repository.
var (
	testDigest = "sha256:" + strings.Repeat("a", 64)
	testImage  = "ghcr.io/ignisdevne/suiteward@" + testDigest
)

type call struct {
	name  string
	args  []string
	stdin string
}

func (c call) line() string { return strings.Join(c.args, " ") }

type rule struct {
	match string
	out   string
	err   error
}

// fakeEngine records every command of a runtime and answers from rules: the first rule whose text is part of the
// argument line decides; a call no rule matches succeeds with no output.
type fakeEngine struct {
	calls []call // captured calls, in order
	tty   []call // Interactive calls
	rules []rule
	goos  string
	env   map[string]string
	exe   string // the program of this run for Deps.Executable; empty means the test binary
	// idleListing makes a listing of the programs that run (Win32_Process) that no rule answers say that none runs, and end as it should. Only the install
	// harnesses of Windows set it, because a first install lists once. Without it an unruled listing is empty and unfinished, so a code path that lists
	// where it should not fails loudly.
	idleListing bool
}

func newFake(goos string) *fakeEngine { return &fakeEngine{goos: goos, env: map[string]string{}} }

// matches says whether an argument line is the one a rule is for: the text is part of the line, or, with a leading "=", it is the line.
func matches(line, match string) bool {
	if exact, ok := strings.CutPrefix(match, "="); ok {
		return line == exact
	}
	return strings.Contains(line, match)
}

func (f *fakeEngine) on(match, out string) *fakeEngine {
	f.rules = append(f.rules, rule{match: match, out: out})
	return f
}

func (f *fakeEngine) fail(match string, err error) *fakeEngine {
	f.rules = append(f.rules, rule{match: match, err: err})
	return f
}

func (f *fakeEngine) deps() Deps {
	return Deps{
		GOOS:    f.goos,
		Getenv:  func(k string) string { return f.env[k] },
		UserSID: func() (string, error) { return testSID, nil },
		// The test binary: a file that exists, for the commands that hash this program (doctor, the executor's copy).
		Executable: func() (string, error) {
			if f.exe != "" {
				return f.exe, nil
			}
			return os.Executable()
		},
		Exec: func(_ context.Context, name string, args []string, stdin io.Reader) ([]byte, error) {
			c := call{name: name, args: append([]string(nil), args...)}
			if stdin != nil {
				data, err := io.ReadAll(stdin)
				if err != nil {
					return nil, err
				}
				c.stdin = string(data)
			}
			f.calls = append(f.calls, c)
			for _, r := range f.rules {
				if matches(c.line(), r.match) {
					return []byte(r.out), r.err
				}
			}
			if f.idleListing && strings.Contains(c.line(), "Win32_Process") {
				return []byte(listingOf(nil)), nil
			}
			return nil, nil
		},
		Interactive: func(_ context.Context, name string, args []string) error {
			f.tty = append(f.tty, call{name: name, args: append([]string(nil), args...)})
			return nil
		},
	}
}

// lines returns the argument line of every captured call.
func (f *fakeEngine) lines() []string {
	var out []string
	for _, c := range f.calls {
		out = append(out, c.line())
	}
	return out
}

// find returns the captured calls whose argument line contains text.
func (f *fakeEngine) find(text string) []call {
	var out []call
	for _, c := range f.calls {
		if strings.Contains(c.line(), text) {
			out = append(out, c)
		}
	}
	return out
}

// assertNoSecret fails when any argument of any call, captured or interactive, contains one of the values.
func (f *fakeEngine) assertNoSecret(t *testing.T, values ...string) {
	t.Helper()
	for _, c := range append(append([]call(nil), f.calls...), f.tty...) {
		for _, arg := range c.args {
			for _, v := range values {
				if strings.Contains(arg, v) {
					t.Errorf("a secret value reached the argument list of %s: %v", c.name, c.args)
				}
			}
		}
	}
}
