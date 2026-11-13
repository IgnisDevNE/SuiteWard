package run

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/config"
	"github.com/IgnisDevNE/SuiteWard/internal/domain/execution"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/client"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/sandbox"
	"github.com/IgnisDevNE/SuiteWard/internal/executor/source"
)

// anAttempt is the attempt of a job that the fake service grants, with rt as its container runtime: the executor's own state, without its loop.
func anAttempt(t *testing.T, sc *scenario, rt sandbox.Runtime) *attempt {
	t.Helper()
	svc := newService(t, sc.serve(1))
	api, err := client.New(svc.URL, config.Secret(testToken))
	if err != nil {
		t.Fatal(err)
	}
	lease, err := api.Lease(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(Config{
		API: api, Runtime: rt, Version: "1.2.3", BinarySHA256: testBinary, Log: discardLog(),
		Sweep: func(context.Context) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	a, err := newAttempt(e, lease, "go1.27.3")
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// headSide is the head of the attempt's job as a side whose tree is every file of the commit.
func headSide(t *testing.T, a *attempt) side {
	t.Helper()
	head, err := a.inspect(context.Background(), source.SideHead)
	if err != nil {
		t.Fatal(err)
	}
	return side{which: source.SideHead, tree: treeOf(head.Files, execution.FromHead)}
}

// seen is the observations of each test as words, in the order of the runs.
func seen(runs map[testID][]execution.Observation) map[testID]string {
	out := map[testID]string{}
	for id, observed := range runs {
		words := make([]string, len(observed))
		for i, o := range observed {
			words[i] = o.String()
		}
		out[id] = strings.Join(words, ",")
	}
	return out
}

var (
	testA = testID{Package: "a", Name: "TestA", File: "a/a_test.go"}
	testB = testID{Package: "a", Name: "TestB", File: "a/a_test.go"}
)

// throughTheSeam runs the tests on the head of a canonical job as the attempt does: the profile plans the side, and runs it in the containers
// of the attempt's invoker.
func throughTheSeam(t *testing.T, head repo, rt sandbox.Runtime) map[testID][]execution.Observation {
	t.Helper()
	sc := canonicalScenario()
	sc.head = head
	a := anAttempt(t, sc, rt)
	s := headSide(t, a)
	files := map[string][]byte{}
	if m, ok := head["go.mod"]; ok {
		files["go.mod"] = []byte(m)
	}
	p := a.profile()
	plan, err := p.plan(sideFiles{side: s, files: files}, a.lease.Limits, a.toolchain)
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	runs, err := p.run(context.Background(), a.box(s), plan, []testID{testA, testB})
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return runs
}

// checkGoContainer checks the one container of a Go side: the Go image, the Go command and environment, the tree below src, and then go's own
// directories, and the marker last: the sandbox runs nothing without it.
func checkGoContainer(t *testing.T, c call) {
	t.Helper()
	argv, env := goprofile.Command([]goprofile.TestID{goprofile.TestID(testA), goprofile.TestID(testB)}, 600*time.Second, false)
	if c.Spec.Image != goprofile.Image || c.Spec.Workdir != goprofile.TreeDir || !slices.Equal(c.Spec.Argv, argv) || !slices.Equal(c.Spec.Env, env) {
		t.Errorf("the container is %q in %q with %q and %q; want the Go image in %q with %q and %q",
			c.Spec.Image, c.Spec.Workdir, c.Spec.Argv, c.Spec.Env, goprofile.TreeDir, argv, env)
	}
	tail := c.Entries[len(c.Entries)-3:]
	if want := []string{goprofile.HomeDir + "/.keep", goprofile.TmpDir + "/.keep", sandbox.CompleteMarker}; !slices.Equal(tail, want) {
		t.Errorf("the tar ends with %q; want %q", tail, want)
	}
	if _, ok := c.Files["src/go.mod"]; !ok {
		t.Errorf("the tar holds %q; want the tree below src", c.Entries)
	}
}

func TestGoProfileRunsASideAsBefore(t *testing.T) {
	module := repo{"go.mod": goMod, "a/a_test.go": "package a\n"}

	t.Run("a side that passes runs once in the Go image with the Go command", func(t *testing.T) {
		rt := newRuntime(func(c call) (sandbox.Outcome, error) {
			return goTest(c, func(string, string) string { return "pass" }), nil
		})
		runs := throughTheSeam(t, module, rt)
		if got, want := seen(runs), map[testID]string{testA: "pass", testB: "pass"}; !maps.Equal(got, want) {
			t.Errorf("the observations are %v; want %v", got, want)
		}
		calls := rt.invocations()
		if len(calls) != 1 {
			t.Fatalf("%d containers ran; want 1", len(calls))
		}
		checkGoContainer(t, calls[0])
	})

	t.Run("a test that fails and then passes runs again, alone", func(t *testing.T) {
		n := 0
		rt := newRuntime(func(c call) (sandbox.Outcome, error) {
			n++
			return goTest(c, func(_, name string) string {
				if name == "TestB" && n == 1 {
					return "fail"
				}
				return "pass"
			}), nil
		})
		runs := throughTheSeam(t, module, rt)
		if got, want := seen(runs), map[testID]string{testA: "pass", testB: "fail,pass"}; !maps.Equal(got, want) {
			t.Errorf("the observations are %v; want %v", got, want)
		}
		calls := rt.invocations()
		if len(calls) != 2 {
			t.Fatalf("%d containers ran; want 2", len(calls))
		}
		if got, want := calls[1].Names, []string{"TestB"}; !slices.Equal(got, want) {
			t.Errorf("the rerun names %q; want %q", got, want)
		}
	})

	t.Run("a side without a module reads build-error and runs nothing", func(t *testing.T) {
		rt := newRuntime(func(call) (sandbox.Outcome, error) { return sandbox.Outcome{}, errors.New("no container is to run") })
		runs := throughTheSeam(t, repo{"a/a_test.go": "package a\n"}, rt)
		if got, want := seen(runs), map[testID]string{testA: "build-error", testB: "build-error"}; !maps.Equal(got, want) {
			t.Errorf("the observations are %v; want %v", got, want)
		}
		if n := len(rt.invocations()); n != 0 {
			t.Errorf("%d containers ran; want none", n)
		}
	})
}

// otherProfile is a profile that is not Go: its own image, argv, environment and tar entry. Its parse records what it was given.
type otherProfile struct{ parsed *[]output }

const otherImage = "registry.example.com/images/other:1@sha256:1111111111111111111111111111111111111111111111111111111111111111"

var _ profile = otherProfile{}

func (otherProfile) image() string { return otherImage }

func (otherProfile) plan(sideFiles, client.Limits, string) (sidePlan, error) { return struct{}{}, nil }

func (o otherProfile) run(ctx context.Context, box invoker, _ sidePlan, tests []testID) (map[testID][]execution.Observation, error) {
	out, err := box.invoke(ctx, invocation{
		image: o.image(), argv: []string{"ctest", "--output-on-failure"}, env: []string{"CC=gcc"}, workdir: "work/build",
		extra: func(_ context.Context, app *source.Appender) error {
			return app.Add(".other/seed", 4, strings.NewReader("seed"))
		},
	})
	if err != nil {
		return nil, err
	}
	*o.parsed = append(*o.parsed, out)
	return map[testID][]execution.Observation{tests[0]: {execution.ObservedPass}}, nil
}

func TestInvokeClassifiesAKillForAnyProfile(t *testing.T) {
	tests := []testID{{Package: "t", Name: "Suite.Case", File: "t/case.cpp"}}
	cases := []struct {
		name    string
		outcome sandbox.Outcome
	}{
		{"the memory limit", sandbox.Outcome{OOMKilled: true, Stdout: []byte("report")}},
		{"the status 137", sandbox.Outcome{ExitCode: 137, Stdout: []byte("report")}},
		{"output that was cut", sandbox.Outcome{Truncated: true, Stdout: []byte("report")}},
	}
	for _, tc := range cases {
		t.Run(tc.name+" is resource-limit, and nothing is parsed", func(t *testing.T) {
			rt := newRuntime(func(call) (sandbox.Outcome, error) { return tc.outcome, nil })
			a := anAttempt(t, canonicalScenario(), rt)
			var parsed []output
			_, err := otherProfile{&parsed}.run(context.Background(), a.box(side{which: source.SideHead}), struct{}{}, tests)
			if reason, abandon := classify(context.Background(), err); err == nil || reason != reasonResourceLimit || abandon {
				t.Errorf("the error is %v, which reads (%q, abandon %t); want %q", err, reason, abandon, reasonResourceLimit)
			}
			if len(parsed) != 0 {
				t.Errorf("the profile read %d reports of a killed container", len(parsed))
			}
			calls := rt.invocations()
			if len(calls) != 1 {
				t.Fatalf("%d containers ran; want 1", len(calls))
			}
			c := calls[0]
			if c.Spec.Image != otherImage || !slices.Equal(c.Spec.Argv, []string{"ctest", "--output-on-failure"}) ||
				!slices.Equal(c.Spec.Env, []string{"CC=gcc"}) || c.Spec.Workdir != "work/build" {
				t.Errorf("the container is %q in %q with %q and %q; want the profile's own", c.Spec.Image, c.Spec.Workdir, c.Spec.Argv, c.Spec.Env)
			}
			if want := []string{".other/seed", sandbox.CompleteMarker}; !slices.Equal(c.Entries, want) {
				t.Errorf("the tar holds %q; want the profile's entry and the marker, %q", c.Entries, want)
			}
		})
	}

	t.Run("work that was dropped is the end of the work, whatever the container's status", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		rt := newRuntime(func(call) (sandbox.Outcome, error) {
			cancel() // the deadline came while the container ran, and the runtime reports the status of the container that it killed
			return sandbox.Outcome{ExitCode: 137}, nil
		})
		a := anAttempt(t, canonicalScenario(), rt)
		var parsed []output
		_, err := otherProfile{&parsed}.run(ctx, a.box(side{which: source.SideHead}), struct{}{}, tests)
		var decided *reasonError
		if !errors.Is(err, context.Canceled) || errors.As(err, &decided) {
			t.Errorf("the error is %v; want the dropped work, which says nothing of the tests", err)
		}
		if len(parsed) != 0 {
			t.Errorf("the profile read %d reports of dropped work", len(parsed))
		}
	})

	t.Run("a container that ends on its own is read by its profile", func(t *testing.T) {
		rt := newRuntime(func(call) (sandbox.Outcome, error) {
			return sandbox.Outcome{ExitCode: 1, Stdout: []byte("report")}, nil
		})
		a := anAttempt(t, canonicalScenario(), rt)
		var parsed []output
		if _, err := (otherProfile{&parsed}).run(context.Background(), a.box(side{which: source.SideHead}), struct{}{}, tests); err != nil {
			t.Fatal(err)
		}
		if len(parsed) != 1 || string(parsed[0].stdout) != "report" || parsed[0].exitCode != 1 {
			t.Errorf("the profile read %+v; want one report, status 1", parsed)
		}
	})
}
