package run

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IgnisDevNE/SuiteWard/internal/executor/goprofile"
)

func argvBytes(tests []goprofile.TestID, vendor bool) int {
	argv, _ := goprofile.Command(tests, 10*time.Minute, vendor)
	n := 0
	for _, a := range argv {
		n += len(a) + 1
	}
	return n
}

func TestFewTestsAreOneInvocation(t *testing.T) {
	tests := []goprofile.TestID{{Package: ".", Name: "TestRoot", File: "root_test.go"}, {Package: "a", Name: "TestA", File: "a/a_test.go"}}
	got := chunks(tests, 10*time.Minute, false)
	if len(got) != 1 || len(got[0]) != 2 {
		t.Errorf("chunks = %v; want one invocation of both tests", got)
	}
}

func TestManyTestsAreSplitSoThatNoInvocationPassesTheArgumentBound(t *testing.T) {
	var tests []goprofile.TestID
	for p := range 20 {
		for n := range 100 {
			tests = append(tests, goprofile.TestID{
				Package: fmt.Sprintf("pkg/p%02d", p), Name: fmt.Sprintf("Test%s%03d", strings.Repeat("N", 40), n), File: fmt.Sprintf("pkg/p%02d/x_test.go", p),
			})
		}
	}
	got := chunks(tests, 10*time.Minute, false)
	if len(got) < 4 {
		t.Fatalf("2000 tests of 47 bytes made %d invocations; they cannot fit fewer", len(got))
	}
	var all []goprofile.TestID
	for i, chunk := range got {
		if n := argvBytes(chunk, false); n > maxArgvBytes {
			t.Errorf("invocation %d has %d bytes of arguments; the bound is %d", i, n, maxArgvBytes)
		}
		all = append(all, chunk...)
	}
	if !slices.Equal(all, tests) {
		t.Errorf("the invocations together hold %d tests, not the %d that were given in the order they were given: each test is in exactly one", len(all), len(tests))
	}
}

func TestAManyPackagesMakeTheArgumentsLongJustAsManyNamesDo(t *testing.T) {
	var tests []goprofile.TestID
	for p := range 600 {
		dir := fmt.Sprintf("a/very/deep/package/directory/number/%04d/%s", p, strings.Repeat("d", 40))
		tests = append(tests, goprofile.TestID{Package: dir, Name: "TestX", File: dir + "/x_test.go"})
	}
	got := chunks(tests, 10*time.Minute, true)
	if len(got) < 2 {
		t.Fatalf("600 long packages made %d invocation", len(got))
	}
	for i, chunk := range got {
		if n := argvBytes(chunk, true); n > maxArgvBytes {
			t.Errorf("invocation %d has %d bytes of arguments", i, n)
		}
	}
}

func TestNoTestsAreNoInvocations(t *testing.T) {
	if got := chunks(nil, time.Minute, false); len(got) != 0 {
		t.Errorf("chunks(nil) = %v", got)
	}
}
